package valkeystore_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
)

// ASK says nothing about the slot map: a stream of operations on a
// migrating slot costs one slot-map load to find the importing node, not one
// per redirect.
func TestCluster_AskRedirect_DoesNotReloadPerRedirect(t *testing.T) {
	t.Parallel()
	m := newMigratingPair(t, "*0\r\n")
	s, ctx := valkeystore.New(m.client, valkeystore.Options{}), context.Background()
	const ops = 20
	for range ops {
		_, err := s.Put(ctx, sampleRef())
		require.NoError(t, err)
		_, _, err = s.Get(ctx, "t", docref.NewID())
		require.NoError(t, err)
		require.NoError(t, s.Delete(ctx, "t", docref.NewID()))
	}
	assert.LessOrEqual(t, m.loads.Load(), int32(2), "the initial load and one to find the ASK target, for %d redirects", 3*ops)
}

// A redirect naming a node by a different spelling of its address (host
// case) still finds it: addresses are compared as host and port.
func TestCluster_AskRedirect_AddressCaseInsensitive(t *testing.T) {
	t.Parallel()
	m := newMigratingPairWith(t, "*0\r\n", func(addr string) string {
		// 127.0.0.1:port → [::FFFF:127.0.0.1]:port would change the
		// address; only the host's case may differ, so spell the IPv4
		// literal as is — the normalisation is covered white-box — and
		// check the redirect is followed.
		return strings.ToUpper(addr)
	}, "+OK\r\n")
	_, err := valkeystore.New(m.client, valkeystore.Options{}).Put(context.Background(), sampleRef())
	require.NoError(t, err)
}

// A user without +asking gets NOPERM for ASKING: the refusal itself is
// reported — not the ASK redirect it causes, and not as transient, since a
// retry cannot succeed until the ACL is fixed.
func TestCluster_AskingRefused_IsReported(t *testing.T) {
	t.Parallel()
	const noperm = "-NOPERM User docref-worker has no permissions to run the 'asking' command\r\n"
	m := newMigratingPairWith(t, "*0\r\n", func(addr string) string { return addr }, noperm)
	s, ctx := valkeystore.New(m.client, valkeystore.Options{}), context.Background()

	_, err := s.Put(ctx, sampleRef())
	require.ErrorContains(t, err, "ASKING: NOPERM", "put")
	assert.NotErrorIs(t, err, docref.ErrUnavailable, "put")
	_, _, err = s.Get(ctx, "t", docref.NewID())
	require.ErrorContains(t, err, "ASKING: NOPERM", "get")
	assert.NotErrorIs(t, err, docref.ErrUnavailable, "get")
	err = s.Delete(ctx, "t", docref.NewID())
	require.ErrorContains(t, err, "ASKING: NOPERM", "delete")
	assert.NotErrorIs(t, err, docref.ErrUnavailable, "delete")
}

// A burst of MOVED replies after a resharding costs one synchronous
// slot-map reload, not one per operation: an operation that started before
// another's reload finished reuses it.
func TestCluster_MovedBurst_ReloadsOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var moved atomic.Bool
	var loads atomic.Int32
	newMaster := scriptedNode(t, func(args []string) string {
		if args[0] == "HGETALL" {
			return "*0\r\n"
		}
		return "-ERR unexpected\r\n"
	})
	oldMaster := scriptedNode(t, func([]string) string {
		moved.Store(true)
		return fmt.Sprintf("-MOVED 1234 %s\r\n", newMaster)
	})
	c := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{oldMaster}, Protocol: 2, DisableIdentity: true, MaxRedirects: -1, MaxRetries: -1,
		ClusterSlots: func(context.Context) ([]redis.ClusterSlot, error) {
			loads.Add(1)
			owner := oldMaster
			if moved.Load() {
				owner = newMaster
			}
			return []redis.ClusterSlot{{Start: 0, End: 16383, Nodes: []redis.ClusterNode{{Addr: owner}}}}, nil
		},
	})
	t.Cleanup(func() { _ = c.Close() })
	_, err := c.MasterForKey(ctx, "warm-up") // the initial slot map
	require.NoError(t, err)

	s := valkeystore.New(c, valkeystore.Options{})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			<-start
			_, _, err := s.Get(ctx, "t", docref.NewID())
			assert.NoError(t, err)
		})
	}
	close(start)
	wg.Wait()
	assert.Equal(t, int32(2), loads.Load(), "the initial load and one reload for the whole burst")
}

// An ASK naming an address that is not host:port matches no master:
// unavailable, retried.
func TestCluster_AskToMalformedAddress_IsUnavailable(t *testing.T) {
	t.Parallel()
	c := fakeCluster(t, func([]string) string { return "-ASK 1234 nonsense\r\n" })
	_, _, err := valkeystore.New(c, valkeystore.Options{}).Get(context.Background(), "t", docref.NewID())
	assert.ErrorIs(t, err, docref.ErrUnavailable)
}

// A node client the cluster client has closed (a cached ASK target that left
// the slot map, or the whole client at shutdown) is unavailable, retried —
// never a permanent failure.
func TestCluster_ClosedNodeClient_IsUnavailable(t *testing.T) {
	t.Parallel()
	c := fakeCluster(t, func(args []string) string { return "*0\r\n" })
	s, ctx := valkeystore.New(c, valkeystore.Options{}), context.Background()
	_, _, err := s.Get(ctx, "t", docref.NewID())
	require.NoError(t, err)
	require.NoError(t, c.Close())
	_, _, err = s.Get(ctx, "t", docref.NewID())
	assert.ErrorIs(t, err, docref.ErrUnavailable)
	assert.ErrorIs(t, err, redis.ErrClosed)
}

// With DisableWaitAOF, WaitReplicas is ignored — a negative value included.
func TestOptions_NegativeWaitReplicas_IgnoredWithoutWaitAOF(t *testing.T) {
	t.Parallel()
	opts := valkeystore.Options{DisableWaitAOF: true, WaitReplicas: -1}
	require.NoError(t, opts.Validate())
	c := fakeClient(t, func(args []string) string {
		if args[0] == "EVALSHA" {
			return createdReply("1700000000000")
		}
		return "-ERR unexpected " + args[0] + "\r\n"
	})
	_, err := valkeystore.New(c, opts).Put(context.Background(), sampleRef())
	assert.NoError(t, err)
}

// A transient refusal of ASKING (a node still loading) is matched however it
// is wrapped: unavailable, retried.
func TestCluster_AskingTransientRefusal_IsUnavailable(t *testing.T) {
	t.Parallel()
	m := newMigratingPairWith(t, "*0\r\n", func(addr string) string { return addr }, "-LOADING Valkey is loading the dataset in memory\r\n")
	_, err := valkeystore.New(m.client, valkeystore.Options{}).Put(context.Background(), sampleRef())
	require.ErrorContains(t, err, "ASKING: LOADING")
	assert.ErrorIs(t, err, docref.ErrUnavailable)
}
