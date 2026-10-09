package valkeystore_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
)

// scriptedNode is a fake Valkey node: reply maps each command (its args, the
// command name upper-cased) to the raw RESP answer.
func scriptedNode(t *testing.T, reply func(args []string) string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveScripted(conn, reply)
		}
	}()
	return ln.Addr().String()
}

func serveScripted(conn net.Conn, reply func(args []string) string) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
		args := make([]string, 0, n)
		for range n {
			header, err := r.ReadString('\n') // $len
			if err != nil {
				return
			}
			size, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "$")))
			buf := make([]byte, size+2) // payload + CRLF; a script body has newlines
			if _, err := io.ReadFull(r, buf); err != nil {
				return
			}
			args = append(args, string(buf[:size]))
		}
		args[0] = strings.ToUpper(args[0])
		if _, err := conn.Write([]byte(reply(args))); err != nil {
			return
		}
	}
}

func fakeClient(t *testing.T, reply func(args []string) string) *redis.Client {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: scriptedNode(t, reply), Protocol: 2, DisableIdentity: true, MaxRetries: -1})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// fakeCluster is a cluster client whose only master (all slots) is the fake
// node, using a static slot map so no CLUSTER command is needed.
func fakeCluster(t *testing.T, reply func(args []string) string) *redis.ClusterClient {
	t.Helper()
	addr := scriptedNode(t, reply)
	c := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{addr}, Protocol: 2, DisableIdentity: true, MaxRedirects: -1, MaxRetries: -1,
		ClusterSlots: func(context.Context) ([]redis.ClusterSlot, error) {
			return []redis.ClusterSlot{{Start: 0, End: 16383, Nodes: []redis.ClusterNode{{Addr: addr}}}}, nil
		},
	})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// otherClient is a UniversalClient that is neither *redis.Client nor
// *redis.ClusterClient (a Ring, or a wrapper); it delegates to inner.
type otherClient struct{ redis.UniversalClient }

func sampleRef() docref.Ref {
	return docref.Ref{TenantID: "t", Bucket: "b", ObjectKey: "t/x", Size: 1, SHA256: strings.Repeat("a", 64)}
}

func configReply(args []string) string {
	switch args[2] {
	case "appendonly":
		return "*2\r\n$10\r\nappendonly\r\n$3\r\nyes\r\n"
	case "appendfsync":
		return "*2\r\n$11\r\nappendfsync\r\n$6\r\nalways\r\n"
	default:
		return "*2\r\n$16\r\nmaxmemory-policy\r\n$10\r\nnoeviction\r\n"
	}
}

func createdReply(ms string) string {
	return "*2\r\n:1\r\n$" + strconv.Itoa(len(ms)) + "\r\n" + ms + "\r\n"
}

func TestNew_ReadTimeoutVariants(t *testing.T) {
	t.Parallel()
	// Construction only inspects options; no connection is made. Each real
	// client is closed by t.Cleanup; the nil-backed otherClient is never closed.
	cluster := redis.NewClusterClient(&redis.ClusterOptions{Addrs: []string{"127.0.0.1:1"}, ReadTimeout: 300 * time.Millisecond})
	noTimeout := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", ReadTimeout: -2})
	zeroAfterInit := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", ReadTimeout: -1})
	t.Cleanup(func() {
		_ = cluster.Close()
		_ = noTimeout.Close()
		_ = zeroAfterInit.Close()
	})
	for _, tc := range []struct {
		name   string
		client redis.UniversalClient
	}{
		{"cluster", cluster},
		{"no read timeout", noTimeout},
		{"zero after init", zeroAfterInit},
		{"other client", otherClient{}},
	} {
		assert.NotNil(t, valkeystore.New(tc.client, valkeystore.Options{}), tc.name)
	}
}

func TestUnsupportedClient_IsRefused(t *testing.T) {
	t.Parallel()
	s, ctx := valkeystore.New(otherClient{}, valkeystore.Options{}), context.Background()
	_, err := s.Put(ctx, sampleRef())
	assert.ErrorContains(t, err, "unsupported Valkey client", "put")
	_, _, err = s.Get(ctx, "t", docref.NewID())
	assert.ErrorContains(t, err, "unsupported Valkey client", "get")
	assert.ErrorContains(t, s.Delete(ctx, "t", docref.NewID()), "unsupported Valkey client", "delete")
	assert.ErrorContains(t, valkeystore.CheckDurability(ctx, otherClient{}), "unsupported Valkey client", "durability")
}

// With WAITAOF disabled the script runs alone (EVALSHA), so any
// UniversalClient works.
func TestPut_WithoutWaitAOF(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	ok := otherClient{fakeClient(t, func(args []string) string {
		if args[0] == "EVALSHA" {
			return createdReply("1700000000000")
		}
		return "-ERR unexpected " + args[0] + "\r\n"
	})}
	ref, err := valkeystore.New(ok, valkeystore.Options{DisableWaitAOF: true}).Put(ctx, sampleRef())
	require.NoError(t, err)
	assert.Equal(t, time.UnixMilli(1700000000000).UTC(), ref.CreatedAt)

	failing := fakeClient(t, func([]string) string { return "-ERR boom\r\n" })
	_, err = valkeystore.New(failing, valkeystore.Options{DisableWaitAOF: true}).Put(ctx, sampleRef())
	require.ErrorContains(t, err, "boom")
	assert.NotErrorIs(t, err, docref.ErrUnavailable, "an unknown error is not reported as transient")
}

func TestPut_OutOfMemory_IsUnavailable(t *testing.T) {
	t.Parallel()
	c := fakeClient(t, func(args []string) string {
		if args[0] == "EVAL" {
			return "-OOM command not allowed when used memory > 'maxmemory'.\r\n"
		}
		return "*2\r\n:0\r\n:0\r\n"
	})
	_, err := valkeystore.New(c, valkeystore.Options{}).Put(context.Background(), sampleRef())
	assert.ErrorIs(t, err, docref.ErrUnavailable)
}

func TestPut_MalformedTimestamp(t *testing.T) {
	t.Parallel()
	c := fakeClient(t, func(args []string) string {
		if args[0] == "EVAL" {
			return createdReply("not-a-number")
		}
		return "*2\r\n:1\r\n:0\r\n"
	})
	_, err := valkeystore.New(c, valkeystore.Options{}).Put(context.Background(), sampleRef())
	assert.ErrorContains(t, err, "timestamp")
}

func TestGetDelete_UnknownError_IsNotTransient(t *testing.T) {
	t.Parallel()
	c := fakeClient(t, func([]string) string { return "-ERR boom\r\n" })
	s, ctx := valkeystore.New(c, valkeystore.Options{}), context.Background()

	_, _, err := s.Get(ctx, "t", docref.NewID())
	require.ErrorContains(t, err, "boom")
	assert.NotErrorIs(t, err, docref.ErrUnavailable)
	err = s.Delete(ctx, "t", docref.NewID())
	require.ErrorContains(t, err, "boom")
	assert.NotErrorIs(t, err, docref.ErrUnavailable)
}

// On a cluster a redirect also reloads the slot map, so the retry reaches
// the key's current master.
func TestCluster_RedirectIsUnavailable(t *testing.T) {
	t.Parallel()
	c := fakeCluster(t, func([]string) string { return "-MOVED 3999 127.0.0.1:1\r\n" })
	s, ctx := valkeystore.New(c, valkeystore.Options{}), context.Background()

	_, err := s.Put(ctx, sampleRef())
	assert.ErrorIs(t, err, docref.ErrUnavailable)
}

func TestCheckDurability_Cluster(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	good := fakeCluster(t, func(args []string) string {
		if args[0] == "CONFIG" {
			return configReply(args)
		}
		return "*2\r\n:1\r\n:0\r\n" // WAITAOF: confirmed locally
	})
	assert.NoError(t, valkeystore.CheckDurability(ctx, good))

	unconfirmed := fakeCluster(t, func(args []string) string {
		if args[0] == "CONFIG" {
			return configReply(args)
		}
		return "*2\r\n:0\r\n:0\r\n" // WAITAOF: not in the AOF
	})
	err := valkeystore.CheckDurability(ctx, unconfirmed)
	var derr *valkeystore.DurabilityError
	require.ErrorAs(t, err, &derr)
	assert.Contains(t, derr.Error(), "did not confirm the local append-only file")
	assert.Regexp(t, `^master \S*:[0-9]+: `, err.Error(), "a cluster failure names the node and its role")
}

func TestCheckDurability_UnconfirmedProbeAndBadConfig_ReportsBoth(t *testing.T) {
	t.Parallel()
	c := fakeClient(t, func(args []string) string {
		if args[0] == "CONFIG" {
			return "*2\r\n$" + strconv.Itoa(len(args[2])) + "\r\n" + args[2] + "\r\n$2\r\nno\r\n"
		}
		return "*0\r\n"
	})
	var derr *valkeystore.DurabilityError
	require.ErrorAs(t, valkeystore.CheckDurability(context.Background(), c), &derr)
	assert.Len(t, derr.Problems, 4, "the probe failure and all three settings")
}

// A server without WAITAOF (or with CONFIG disabled) is checked without a
// real server: the probe result decides how a refused CONFIG GET is reported.
func TestCheckDurability_RefusedCommands(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	allRefused := fakeClient(t, func([]string) string { return "-ERR unknown command\r\n" })
	var derr *valkeystore.DurabilityError
	require.ErrorAs(t, valkeystore.CheckDurability(ctx, allRefused), &derr)
	assert.Contains(t, derr.Problems[0], "WAITAOF refused")

	configBlocked := fakeClient(t, func(args []string) string {
		if args[0] == "WAITAOF" {
			return "*2\r\n:1\r\n:0\r\n"
		}
		return "-ERR unknown command 'CONFIG'\r\n"
	})
	assert.ErrorIs(t, valkeystore.CheckDurability(ctx, configBlocked), valkeystore.ErrConfigUnavailable)
}

func TestPut_WaitAOFRefused_IsNotPersisted(t *testing.T) {
	t.Parallel()
	c := fakeClient(t, func(args []string) string {
		if args[0] == "EVAL" {
			return createdReply("1700000000000")
		}
		return "-ERR unknown command 'WAITAOF'\r\n"
	})
	_, err := valkeystore.New(c, valkeystore.Options{}).Put(context.Background(), sampleRef())
	assert.ErrorIs(t, err, valkeystore.ErrNotPersisted)
}

// fakeClusterWithReplica is a one-shard cluster: master and replica are fake
// nodes answering with their own reply functions.
func fakeClusterWithReplica(t *testing.T, master, replica func(args []string) string) *redis.ClusterClient {
	t.Helper()
	m, r := scriptedNode(t, master), scriptedNode(t, replica)
	c := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{m}, Protocol: 2, DisableIdentity: true, MaxRedirects: -1, MaxRetries: -1,
		ClusterSlots: func(context.Context) ([]redis.ClusterSlot, error) {
			return []redis.ClusterSlot{{Start: 0, End: 16383, Nodes: []redis.ClusterNode{{Addr: m}, {Addr: r}}}}, nil
		},
	})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func durableMaster(args []string) string {
	if args[0] == "CONFIG" {
		return configReply(args)
	}
	return "*2\r\n:1\r\n:0\r\n"
}

// A replica is promoted on failover, so a cluster's replicas must be durable
// stores too: their settings are checked, and a bad one names the replica.
func TestCheckDurability_ClusterReplicas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var probedReplica atomic.Bool
	good := fakeClusterWithReplica(t, durableMaster, func(args []string) string {
		if args[0] != "CONFIG" {
			if args[0] == "WAITAOF" {
				probedReplica.Store(true)
			}
			return "-ERR unexpected\r\n"
		}
		return configReply(args)
	})
	require.NoError(t, valkeystore.CheckDurability(ctx, good))
	assert.False(t, probedReplica.Load(), "a replica gets CONFIG GET only: it has no writes of its own for WAITAOF to confirm")

	noAOF := fakeClusterWithReplica(t, durableMaster, func(args []string) string {
		switch {
		case args[0] != "CONFIG":
			return "-ERR unexpected\r\n"
		case args[2] == "appendonly":
			return "*2\r\n$10\r\nappendonly\r\n$2\r\nno\r\n"
		default:
			return configReply(args)
		}
	})
	err := valkeystore.CheckDurability(ctx, noAOF)
	var derr *valkeystore.DurabilityError
	require.ErrorAs(t, err, &derr)
	assert.Equal(t, []string{`appendonly is "no", want "yes" (AOF persistence)`}, derr.Problems)
	assert.Regexp(t, `^replica \S*:[0-9]+: `, err.Error())

	blocked := fakeClusterWithReplica(t, durableMaster, func([]string) string { return "-ERR unknown command 'CONFIG'\r\n" })
	err = valkeystore.CheckDurability(ctx, blocked)
	assert.ErrorIs(t, err, valkeystore.ErrConfigUnavailable)
	assert.Regexp(t, `^replica `, err.Error())
}

// The create script answers {2, created} when the key already holds exactly
// this ref (a retry after a lost reply): Put succeeds with the original
// timestamp, and WAITAOF is still required.
func TestPut_IdempotentReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	replay := func(wait string) *redis.Client {
		return fakeClient(t, func(args []string) string {
			if args[0] == "WAITAOF" {
				return wait
			}
			return "*2\r\n:2\r\n$13\r\n1600000000000\r\n"
		})
	}
	ref, err := valkeystore.New(replay("*2\r\n:1\r\n:0\r\n"), valkeystore.Options{}).Put(ctx, sampleRef())
	require.NoError(t, err)
	assert.Equal(t, time.UnixMilli(1600000000000).UTC(), ref.CreatedAt)

	_, err = valkeystore.New(replay("*2\r\n:0\r\n:0\r\n"), valkeystore.Options{}).Put(ctx, sampleRef())
	assert.ErrorIs(t, err, valkeystore.ErrNotPersisted, "a replayed create is still confirmed in the AOF")
}
