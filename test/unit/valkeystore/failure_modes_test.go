package valkeystore_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
)

// killableNode is a fake Valkey node that can die: kill closes its listener
// and every open connection, as a crashed master does.
type killableNode struct {
	addr  string
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
	dead  bool
}

func newKillableNode(t *testing.T, reply func(args []string) string) *killableNode {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	k := &killableNode{addr: ln.Addr().String(), ln: ln}
	t.Cleanup(k.kill)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			k.mu.Lock()
			if k.dead {
				k.mu.Unlock()
				_ = conn.Close()
				return
			}
			k.conns = append(k.conns, conn)
			k.mu.Unlock()
			go serveScripted(conn, reply)
		}
	}()
	return k
}

func (k *killableNode) kill() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.dead = true
	_ = k.ln.Close()
	for _, c := range k.conns {
		_ = c.Close()
	}
}

func port(addr string) string {
	_, p, _ := net.SplitHostPort(addr)
	return p
}

// healthyNode serves every store command.
func healthyNode(args []string) string {
	switch args[0] {
	case "HGETALL":
		return "*0\r\n"
	case "WAITAOF":
		return "*2\r\n:1\r\n:0\r\n"
	case "EVAL", "EVALSHA":
		if strings.Contains(args[1], "'DEL'") {
			return ":0\r\n"
		}
		return createdReply("1700000000000")
	}
	return "-ERR unexpected " + args[0] + "\r\n"
}

// A master that dies (no reply at all: connection reset, then refused)
// makes the store reload the cluster client's slot map at once: the store
// runs its commands on the node's client directly, which never marks the
// node failing, so without this the dead master would stay in the map until
// go-redis's periodic reload (60 s by default). Once the reload has run, the
// next operation reaches the new master.
func TestCluster_DeadMaster_ReloadsSlotMap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	newMaster := scriptedNode(t, healthyNode)
	old := newKillableNode(t, healthyNode)
	var dead atomic.Bool
	var loads atomic.Int32
	c := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{old.addr}, Protocol: 2, DisableIdentity: true, MaxRedirects: -1, MaxRetries: -1,
		DialTimeout: time.Second,
		ClusterSlots: func(context.Context) ([]redis.ClusterSlot, error) {
			loads.Add(1)
			owner := old.addr
			if dead.Load() { // the replica took over
				owner = newMaster
			}
			return []redis.ClusterSlot{{Start: 0, End: 16383, Nodes: []redis.ClusterNode{{Addr: owner}}}}, nil
		},
	})
	t.Cleanup(func() { _ = c.Close() })
	s := valkeystore.New(c, valkeystore.Options{})

	_, _, err := s.Get(ctx, "t", docref.NewID())
	require.NoError(t, err, "served by the original master")
	before := loads.Load()

	old.kill()
	dead.Store(true)
	_, _, err = s.Get(ctx, "t", docref.NewID())
	require.Error(t, err, "the dead master fails the operation")
	require.Eventually(t, func() bool {
		node, err := c.MasterForKey(ctx, "any")
		// go-redis may respell a loopback host: compare the ports.
		return loads.Load() > before && err == nil && port(node.Options().Addr) == port(newMaster)
	}, 2*time.Second, 10*time.Millisecond, "the failure reloaded the slot map without waiting for the periodic reload")

	// The reload has run: every operation now reaches the new master.
	_, err = s.Put(ctx, sampleRef())
	require.NoError(t, err, "put on the new master")
	_, _, err = s.Get(ctx, "t", docref.NewID())
	require.NoError(t, err, "get on the new master")
	require.NoError(t, s.Delete(ctx, "t", docref.NewID()), "delete on the new master")
}

// A server reply (the node is alive and answered) or the caller's own
// cancellation says nothing about the node: no reload.
func TestCluster_ReplyOrCancellation_DoesNotReload(t *testing.T) {
	t.Parallel()
	var loads atomic.Int32
	addr := scriptedNode(t, func([]string) string { return "-ERR boom\r\n" })
	c := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{addr}, Protocol: 2, DisableIdentity: true, MaxRedirects: -1, MaxRetries: -1,
		ClusterSlots: func(context.Context) ([]redis.ClusterSlot, error) {
			loads.Add(1)
			return []redis.ClusterSlot{{Start: 0, End: 16383, Nodes: []redis.ClusterNode{{Addr: addr}}}}, nil
		},
	})
	t.Cleanup(func() { _ = c.Close() })
	s := valkeystore.New(c, valkeystore.Options{})

	_, _, err := s.Get(context.Background(), "t", docref.NewID())
	require.ErrorContains(t, err, "boom")
	_, err = c.MasterForKey(context.Background(), "x") // the slot map is loaded
	require.NoError(t, err)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = s.Get(cancelled, "t", docref.NewID())
	require.ErrorIs(t, err, context.Canceled)
	time.Sleep(300 * time.Millisecond) // a reload would be scheduled at once
	assert.Equal(t, int32(1), loads.Load(), "only the initial load")
}

// askCluster is a cluster where the key's master (src) answers every keyed
// command with -ASK to target. slots is called for each slot-map load with
// the call number (1 = the initial load): it fails the load, or says
// whether target is in the map (owning slot 0; src owns the rest).
func askCluster(t *testing.T, slots func(ctx context.Context, call int32) (withTarget bool, err error)) (*redis.ClusterClient, *atomic.Int32) {
	t.Helper()
	target := statefulNode(t, func() func([]string) string {
		return func(args []string) string {
			if args[0] == "ASKING" {
				return "+OK\r\n"
			}
			return healthyNode(args)
		}
	})
	src := scriptedNode(t, func([]string) string { return "-ASK 1234 " + target + "\r\n" })
	var calls atomic.Int32
	c := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{src}, Protocol: 2, DisableIdentity: true, MaxRedirects: -1, MaxRetries: -1,
		ClusterSlots: func(ctx context.Context) ([]redis.ClusterSlot, error) {
			withTarget, err := slots(ctx, calls.Add(1))
			if err != nil {
				return nil, err
			}
			if !withTarget {
				return []redis.ClusterSlot{{Start: 0, End: 16383, Nodes: []redis.ClusterNode{{Addr: src}}}}, nil
			}
			return []redis.ClusterSlot{
				{Start: 0, End: 0, Nodes: []redis.ClusterNode{{Addr: target}}},
				{Start: 1, End: 16383, Nodes: []redis.ClusterNode{{Addr: src}}},
			}, nil
		},
	})
	t.Cleanup(func() { _ = c.Close() })
	return c, &calls
}

// A refresh of the masters cache cut short (here by its caller's deadline;
// go-redis then falls back on the old slot map) is not recorded as fresh:
// the next ASK to an unknown node refreshes again at once instead of
// failing for the next second.
func TestCluster_FailedMastersRefresh_IsNotFresh(t *testing.T) {
	t.Parallel()
	c, calls := askCluster(t, func(ctx context.Context, call int32) (bool, error) {
		switch call {
		case 1:
			return false, nil
		case 2: // the first refresh: slow, outlives its caller
			<-ctx.Done()
			return false, ctx.Err()
		default:
			return true, nil
		}
	})
	s := valkeystore.New(c, valkeystore.Options{})

	short, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err := s.Get(short, "t", docref.NewID())
	require.ErrorIs(t, err, docref.ErrUnavailable, "the ASK target could not be found")
	require.Equal(t, int32(2), calls.Load())

	_, _, err = s.Get(context.Background(), "t", docref.NewID())
	require.NoError(t, err, "the next ASK refreshed again and found the target")
	assert.Equal(t, int32(3), calls.Load())
}

// An operation waiting for another's masters-cache refresh waits only as
// long as its own context allows: it returns promptly when its deadline
// passes, while the slow refresh it was waiting for carries on.
func TestCluster_RefreshWaiter_HonoursItsContext(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	c, calls := askCluster(t, func(_ context.Context, call int32) (bool, error) {
		switch call {
		case 1:
			return false, nil
		case 2:
			close(entered)
			select {
			case <-release:
			case <-time.After(5 * time.Second): // never hang the test
				return false, errors.New("never released")
			}
		}
		return true, nil
	})
	s := valkeystore.New(c, valkeystore.Options{})

	first := make(chan error, 1)
	go func() {
		_, _, err := s.Get(context.Background(), "t", docref.NewID())
		first <- err
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := s.Get(ctx, "t", docref.NewID())
	elapsed := time.Since(start)
	close(release)
	require.ErrorIs(t, err, docref.ErrUnavailable)
	assert.Less(t, elapsed, time.Second, "the waiter returned at its deadline, not after the refresh")
	require.NoError(t, <-first, "the slow refresh completed for its own caller")
	assert.Equal(t, int32(2), calls.Load(), "the waiter that gave up started no refresh of its own")

	_, _, err = s.Get(context.Background(), "t", docref.NewID())
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "the completed refresh is reused")
}

// A connection that drops during the ASKING pipeline fails both commands with
// the same network error: that error is reported as the command's own, not
// relabelled as a refused ASKING.
func TestCluster_AskingConnectionDropped_KeepsCommandError(t *testing.T) {
	t.Parallel()
	m := newMigratingPairWith(t, "*0\r\n", func(addr string) string { return addr }, "")
	s, ctx := valkeystore.New(m.client, valkeystore.Options{}), context.Background()

	_, err := s.Put(ctx, sampleRef())
	require.Error(t, err, "put")
	assert.NotContains(t, err.Error(), "ASKING", "put")
	_, _, err = s.Get(ctx, "t", docref.NewID())
	require.Error(t, err, "get")
	assert.NotContains(t, err.Error(), "ASKING", "get")
	err = s.Delete(ctx, "t", docref.NewID())
	require.Error(t, err, "delete")
	assert.NotContains(t, err.Error(), "ASKING", "delete")
}

// A node that answers ASKING with an error yet serves the command: the
// command's own result stands.
func TestCluster_AskingRefusedButCommandServed_KeepsResult(t *testing.T) {
	t.Parallel()
	m := newMigratingPairOpts(t, "*0\r\n", func(addr string) string { return addr }, "-ERR odd\r\n", true)
	s, ctx := valkeystore.New(m.client, valkeystore.Options{}), context.Background()

	_, err := s.Put(ctx, sampleRef())
	require.NoError(t, err, "put")
	_, found, err := s.Get(ctx, "t", docref.NewID())
	require.NoError(t, err, "get")
	assert.False(t, found)
	require.NoError(t, s.Delete(ctx, "t", docref.NewID()), "delete")
}

// Only a reply refusing CONFIG GET itself — NOPERM, an unknown (disabled or
// renamed) command or subcommand — is ErrConfigUnavailable. Any other reply
// from a replica (WRONGPASS: the node lacks the worker's ACL user; LOADING,
// BUSY, maxclients, anything else) means it could not be checked: a failure,
// which a worker tolerating ErrConfigUnavailable must not start on.
func TestCheckDurability_OnlyConfigRefusalsAreConfigUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for reply, unavailable := range map[string]bool{
		"-NOPERM User docref-worker has no permissions to run the 'config|get' command\r\n":            true,
		"-ERR unknown command 'CONFIG', with args beginning with: 'GET' 'appendonly' \r\n":             true,
		"-ERR unknown subcommand 'GET'. Try CONFIG HELP.\r\n":                                          true,
		"-WRONGPASS invalid username-password pair or user is disabled.\r\n":                           false,
		"-LOADING Valkey is loading the dataset in memory\r\n":                                         false,
		"-BUSY Valkey is busy running a script. You can only call SCRIPT KILL or SHUTDOWN NOSAVE.\r\n": false,
		"-ERR max number of clients reached\r\n":                                                       false,
		"-ERR boom\r\n":                                                                                false,
	} {
		c := fakeClusterWithReplica(t, durableMaster, func(args []string) string {
			if args[0] == "CONFIG" {
				return reply
			}
			return "-ERR unexpected\r\n"
		})
		err := valkeystore.CheckDurability(ctx, c)
		require.Error(t, err, reply)
		assert.Regexp(t, `^replica `, err.Error(), reply)
		if unavailable {
			assert.ErrorIs(t, err, valkeystore.ErrConfigUnavailable, reply)
			continue
		}
		assert.NotErrorIs(t, err, valkeystore.ErrConfigUnavailable, reply)
		var derr *valkeystore.DurabilityError
		assert.NotErrorAs(t, err, &derr, "%s: not checked, not misconfigured", reply)
		assert.Contains(t, err.Error(), strings.TrimSpace(strings.TrimPrefix(reply, "-")), reply)
	}
}

// The same failure as it really happens: the replica has no ACL user for
// the worker (ACLs are per node), so the connection's AUTH is refused with
// WRONGPASS before CONFIG GET is even sent.
func TestCheckDurability_ReplicaWithoutWorkerUser_Fails(t *testing.T) {
	t.Parallel()
	master := scriptedNode(t, func(args []string) string {
		switch args[0] {
		case "HELLO": // RESP2-only: go-redis falls back on AUTH
			return "-ERR unknown command 'HELLO'\r\n"
		case "AUTH":
			return "+OK\r\n"
		}
		return durableMaster(args)
	})
	replica := scriptedNode(t, func(args []string) string {
		switch args[0] {
		case "HELLO", "AUTH":
			return "-WRONGPASS invalid username-password pair or user is disabled.\r\n"
		}
		return configReply(args)
	})
	c := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{master}, Protocol: 2, DisableIdentity: true, MaxRedirects: -1, MaxRetries: -1,
		Username: "docref-worker", Password: "secret",
		ClusterSlots: func(context.Context) ([]redis.ClusterSlot, error) {
			return []redis.ClusterSlot{{Start: 0, End: 16383, Nodes: []redis.ClusterNode{{Addr: master}, {Addr: replica}}}}, nil
		},
	})
	t.Cleanup(func() { _ = c.Close() })

	err := valkeystore.CheckDurability(context.Background(), c)
	require.Error(t, err)
	assert.NotErrorIs(t, err, valkeystore.ErrConfigUnavailable)
	assert.Contains(t, err.Error(), "WRONGPASS")
	assert.Regexp(t, `^replica `, err.Error())
}

// Operations that wait for another's masters-cache refresh reuse it when it
// succeeds: one slot-map load for all of them.
func TestCluster_RefreshWaiter_ReusesSuccessfulRefresh(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	c, calls := askCluster(t, func(_ context.Context, call int32) (bool, error) {
		switch call {
		case 1:
			return false, nil
		case 2:
			close(entered)
			<-release
		}
		return true, nil
	})
	s := valkeystore.New(c, valkeystore.Options{})

	errs := make(chan error, 2)
	get := func() {
		_, _, err := s.Get(context.Background(), "t", docref.NewID())
		errs <- err
	}
	go get()
	<-entered
	go get()
	time.Sleep(50 * time.Millisecond) // the second operation is waiting
	close(release)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	assert.Equal(t, int32(2), calls.Load(), "the initial load and one refresh for both")
}

// A waiter whose awaited refresh failed (its caller's context ended) does not
// reuse it: it refreshes again itself.
func TestCluster_RefreshWaiter_RetriesFailedRefresh(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	c, calls := askCluster(t, func(ctx context.Context, call int32) (bool, error) {
		switch call {
		case 1:
			return false, nil
		case 2:
			close(entered)
			<-ctx.Done()
			return false, ctx.Err()
		}
		return true, nil
	})
	s := valkeystore.New(c, valkeystore.Options{})

	short, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	first := make(chan error, 1)
	go func() {
		_, _, err := s.Get(short, "t", docref.NewID())
		first <- err
	}()
	<-entered
	_, _, err := s.Get(context.Background(), "t", docref.NewID())
	require.NoError(t, err, "the waiter refreshed again after the failed refresh")
	require.ErrorIs(t, <-first, docref.ErrUnavailable)
	assert.Equal(t, int32(3), calls.Load())
}

// A refresh that yields no master at all is not stored: the ASK target is
// not found, unavailable (retried).
func TestCluster_RefreshWithoutMasters_IsUnavailable(t *testing.T) {
	t.Parallel()
	target := scriptedNode(t, healthyNode)
	src := scriptedNode(t, func([]string) string { return "-ASK 1234 " + target + "\r\n" })
	var calls atomic.Int32
	c := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{src}, Protocol: 2, DisableIdentity: true, MaxRedirects: -1, MaxRetries: -1,
		ClusterSlots: func(context.Context) ([]redis.ClusterSlot, error) {
			if calls.Add(1) == 1 {
				return []redis.ClusterSlot{{Start: 0, End: 16383, Nodes: []redis.ClusterNode{{Addr: src}}}}, nil
			}
			return []redis.ClusterSlot{}, nil
		},
	})
	t.Cleanup(func() { _ = c.Close() })
	_, _, err := valkeystore.New(c, valkeystore.Options{}).Get(context.Background(), "t", docref.NewID())
	assert.ErrorIs(t, err, docref.ErrUnavailable)
	assert.Equal(t, int32(2), calls.Load())
}
