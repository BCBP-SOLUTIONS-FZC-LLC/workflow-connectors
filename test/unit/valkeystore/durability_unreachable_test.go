package valkeystore_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
)

// closedAddr is an address nothing listens on: a connection is refused.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// Only a server reply refusing CONFIG GET is ErrConfigUnavailable. A replica
// that cannot be reached at all is a failure of its own: with a master that
// merely refuses CONFIG GET (tolerable), the unreachable replica must still
// fail the check — it may be promoted on failover without ever having been
// verified.
func TestCheckDurability_UnreachableReplica_IsNotConfigUnavailable(t *testing.T) {
	t.Parallel()
	master := scriptedNode(t, func(args []string) string {
		if args[0] == "CONFIG" {
			return "-ERR unknown command 'CONFIG'\r\n"
		}
		return "*2\r\n:1\r\n:0\r\n"
	})
	replica := closedAddr(t)
	c := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{master}, Protocol: 2, DisableIdentity: true, MaxRedirects: -1, MaxRetries: -1,
		DialTimeout: time.Second,
		ClusterSlots: func(context.Context) ([]redis.ClusterSlot, error) {
			return []redis.ClusterSlot{{Start: 0, End: 16383, Nodes: []redis.ClusterNode{{Addr: master}, {Addr: replica}}}}, nil
		},
	})
	t.Cleanup(func() { _ = c.Close() })

	err := valkeystore.CheckDurability(context.Background(), c)
	require.Error(t, err)
	assert.NotErrorIs(t, err, valkeystore.ErrConfigUnavailable, "an unreachable replica is not 'verify in the console'")
	_, port, _ := net.SplitHostPort(replica)
	assert.Regexp(t, `replica \S*:`+port+`: CONFIG GET appendonly: `, err.Error())
	var derr *valkeystore.DurabilityError
	assert.NotErrorAs(t, err, &derr, "unreachable, not misconfigured")
}

// The same on a single server whose WAITAOF probe passed but whose CONFIG
// GET times out: the caller's deadline is not ErrConfigUnavailable.
func TestCheckDurability_ConfigGetTimeout_IsNotConfigUnavailable(t *testing.T) {
	t.Parallel()
	c := fakeClient(t, func(args []string) string {
		if args[0] == "CONFIG" {
			time.Sleep(300 * time.Millisecond)
			return configReply(args)
		}
		return "*2\r\n:1\r\n:0\r\n"
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := valkeystore.CheckDurability(ctx, c)
	require.Error(t, err)
	assert.NotErrorIs(t, err, valkeystore.ErrConfigUnavailable)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ErrorContains(t, err, "CONFIG GET ")
}
