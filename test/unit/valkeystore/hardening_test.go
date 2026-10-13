package valkeystore_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
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

// A refusal for lack of capacity (a script running past
// busy-reply-threshold, a server at maxclients) means the command was not
// applied: docref.ErrUnavailable, retried, on every operation.
func TestCapacityRefusals_AreUnavailable(t *testing.T) {
	t.Parallel()
	for _, reply := range []string{
		"-BUSY Valkey is busy running a script. You can only call SCRIPT KILL or SHUTDOWN NOSAVE.\r\n",
		"-ERR max number of clients reached\r\n",
	} {
		answer := func([]string) string { return reply }
		for name, client := range map[string]redis.UniversalClient{
			"standalone": fakeClient(t, answer),
			"cluster":    fakeCluster(t, answer),
		} {
			s, ctx := valkeystore.New(client, valkeystore.Options{}), context.Background()
			_, err := s.Put(ctx, sampleRef())
			assert.ErrorIs(t, err, docref.ErrUnavailable, "%s put: %s", name, reply)
			_, _, err = s.Get(ctx, "t", docref.NewID())
			assert.ErrorIs(t, err, docref.ErrUnavailable, "%s get: %s", name, reply)
			assert.ErrorIs(t, s.Delete(ctx, "t", docref.NewID()), docref.ErrUnavailable, "%s delete: %s", name, reply)
		}
	}
}

// No free connection within PoolTimeout: the command never reached Valkey.
func TestPoolTimeout_IsUnavailable(t *testing.T) {
	t.Parallel()
	c := redis.NewClient(&redis.Options{
		Addr: scriptedNode(t, func(args []string) string {
			if args[0] == "PING" {
				return "+PONG\r\n"
			}
			return "-ERR unknown command\r\n"
		}), Protocol: 2, DisableIdentity: true,
		MaxRetries: -1, PoolSize: 1, MaxActiveConns: 1, PoolTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	held := c.Conn() // takes the pool's only connection until closed
	require.NoError(t, held.Ping(ctx).Err())
	t.Cleanup(func() { _ = held.Close() })

	_, _, err := valkeystore.New(c, valkeystore.Options{}).Get(ctx, "t", docref.NewID())
	require.ErrorIs(t, err, redis.ErrPoolTimeout)
	assert.ErrorIs(t, err, docref.ErrUnavailable)
}

// A cluster whose slot map cannot be loaded is unavailable — unless the
// caller's own context ended, which is not the store's failure.
func TestCluster_SlotMapLoadFailure_IsUnavailable(t *testing.T) {
	t.Parallel()
	newCluster := func(loadErr error) *redis.ClusterClient {
		c := redis.NewClusterClient(&redis.ClusterOptions{
			Addrs: []string{"127.0.0.1:1"}, Protocol: 2, DisableIdentity: true, MaxRetries: -1,
			ClusterSlots: func(context.Context) ([]redis.ClusterSlot, error) { return nil, loadErr },
		})
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	s, ctx := valkeystore.New(newCluster(errors.New("all nodes unreachable")), valkeystore.Options{}), context.Background()
	_, err := s.Put(ctx, sampleRef())
	assert.ErrorIs(t, err, docref.ErrUnavailable, "put")
	_, _, err = s.Get(ctx, "t", docref.NewID())
	assert.ErrorIs(t, err, docref.ErrUnavailable, "get")
	assert.ErrorIs(t, s.Delete(ctx, "t", docref.NewID()), docref.ErrUnavailable, "delete")

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err = s.Get(canceled, "t", docref.NewID())
	require.Error(t, err)
	assert.NotErrorIs(t, err, docref.ErrUnavailable, "the caller's cancellation is not a store outage")

	_, _, err = valkeystore.New(newCluster(context.Canceled), valkeystore.Options{}).Get(ctx, "t", docref.NewID())
	require.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, docref.ErrUnavailable)
}

// A WaitAOFTimeout or TTL below Valkey's 1 ms resolution is rounded up: 0
// would make WAITAOF wait forever and the key expire at once.
func TestOptions_SubMillisecondDurations_RoundUp(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var ttlArg, timeoutArg string
	c := fakeClient(t, func(args []string) string {
		mu.Lock()
		defer mu.Unlock()
		if args[0] == "WAITAOF" {
			timeoutArg = args[3]
			return "*2\r\n:1\r\n:0\r\n"
		}
		if args[0] != "EVAL" {
			return "-ERR unknown command\r\n" // HELLO
		}
		ttlArg = args[4] // EVAL script numkeys key ttl ...
		return createdReply("1700000000000")
	})
	_, err := valkeystore.New(c, valkeystore.Options{TTL: time.Microsecond, WaitAOFTimeout: time.Nanosecond}).Put(context.Background(), sampleRef())
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "1", ttlArg, "TTL in ms")
	assert.Equal(t, "1", timeoutArg, "WAITAOF timeout in ms")
}

// A negative WaitReplicas is refused with a clear, non-retryable error
// instead of failing every Put as unavailable.
func TestOptions_NegativeWaitReplicas_IsInvalid(t *testing.T) {
	t.Parallel()
	opts := valkeystore.Options{WaitReplicas: -1}
	require.ErrorIs(t, opts.Validate(), valkeystore.ErrInvalidOptions)
	assert.NoError(t, valkeystore.Options{}.Validate())

	var calls atomic.Int32
	c := fakeClient(t, func([]string) string { calls.Add(1); return "-ERR unexpected\r\n" })
	_, err := valkeystore.New(c, opts).Put(context.Background(), sampleRef())
	require.ErrorIs(t, err, valkeystore.ErrInvalidOptions)
	assert.NotErrorIs(t, err, docref.ErrUnavailable)
	assert.Zero(t, calls.Load(), "nothing is sent")
}

// statefulNode is a fake Valkey node with per-connection state: perConn is
// called once per accepted connection and answers its commands.
func statefulNode(t *testing.T, perConn func() func(args []string) string) string {
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
			go serveStateful(conn, perConn())
		}
	}()
	return ln.Addr().String()
}

func serveStateful(conn net.Conn, reply func(args []string) string) {
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
			header, err := r.ReadString('\n')
			if err != nil {
				return
			}
			size, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "$")))
			buf := make([]byte, size+2)
			if _, err := io.ReadFull(r, buf); err != nil {
				return
			}
			args = append(args, string(buf[:size]))
		}
		args[0] = strings.ToUpper(args[0])
		answer := reply(args)
		if answer == "" { // drop the connection: a node failing mid-command
			return
		}
		if _, err := conn.Write([]byte(answer)); err != nil {
			return
		}
	}
}

// migratingPair is a two-master cluster where the key's slot is migrating:
// each node refuses the key with -ASK pointing at the other, and serves it
// only after ASKING on the same connection. WAITAOF confirms a write only
// when that connection made one, as the real command does. hash is what
// HGETALL returns after ASKING.
type migratingPair struct {
	client *redis.ClusterClient
	waits  atomic.Int32 // WAITAOFs that confirmed a write on their connection
	loads  atomic.Int32 // slot-map loads (CLUSTER SLOTS)
}

func newMigratingPair(t *testing.T, hash string) *migratingPair {
	t.Helper()
	return newMigratingPairWith(t, hash, func(addr string) string { return addr }, "+OK\r\n")
}

// newMigratingPairWith is newMigratingPair whose redirects name the target
// as spell(addr), and whose nodes answer ASKING with askingReply (an error
// reply refuses it; "" drops the connection instead).
func newMigratingPairWith(t *testing.T, hash string, spell func(addr string) string, askingReply string) *migratingPair {
	t.Helper()
	return newMigratingPairOpts(t, hash, spell, askingReply, false)
}

// newMigratingPairOpts is newMigratingPairWith; with serveAnyway the nodes
// serve the command after ASKING even when they answered it with an error.
func newMigratingPairOpts(t *testing.T, hash string, spell func(addr string) string, askingReply string, serveAnyway bool) *migratingPair {
	t.Helper()
	m := &migratingPair{}
	var addrs [2]atomic.Value
	node := func(self int) func() func([]string) string {
		return func() func([]string) string {
			asking, wrote := false, false
			return func(args []string) string {
				wasAsking := asking
				asking = false
				switch args[0] {
				case "ASKING":
					asking = serveAnyway || !strings.HasPrefix(askingReply, "-")
					return askingReply
				case "WAITAOF":
					if !wrote {
						return "*2\r\n:0\r\n:0\r\n"
					}
					m.waits.Add(1)
					return "*2\r\n:1\r\n:0\r\n"
				}
				if !wasAsking {
					return fmt.Sprintf("-ASK 1234 %s\r\n", spell(addrs[1-self].Load().(string)))
				}
				switch args[0] {
				case "HGETALL":
					return hash
				case "EVAL":
					if strings.Contains(args[1], "'DEL'") {
						return ":1\r\n"
					}
					wrote = true
					return createdReply("1700000000000")
				}
				return "-ERR unexpected " + args[0] + "\r\n"
			}
		}
	}
	a, b := statefulNode(t, node(0)), statefulNode(t, node(1))
	addrs[0].Store(a)
	addrs[1].Store(b)
	m.client = redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{a}, Protocol: 2, DisableIdentity: true, MaxRedirects: -1, MaxRetries: -1,
		ClusterSlots: func(context.Context) ([]redis.ClusterSlot, error) {
			m.loads.Add(1)
			return []redis.ClusterSlot{
				{Start: 0, End: 8191, Nodes: []redis.ClusterNode{{Addr: a}}},
				{Start: 8192, End: 16383, Nodes: []redis.ClusterNode{{Addr: b}}},
			}, nil
		},
	})
	t.Cleanup(func() { _ = m.client.Close() })
	return m
}

// A new key in a migrating slot gets -ASK from its source node: Put follows
// it with ASKING, the script and WAITAOF as one pipeline on the target, so
// the WAITAOF confirms the write on its own connection.
func TestCluster_AskRedirect_PutIsFollowedOnOneConnection(t *testing.T) {
	t.Parallel()
	m := newMigratingPair(t, "*0\r\n")
	ref, err := valkeystore.New(m.client, valkeystore.Options{}).Put(context.Background(), sampleRef())
	require.NoError(t, err)
	assert.Equal(t, time.UnixMilli(1700000000000).UTC(), ref.CreatedAt)
	assert.Equal(t, int32(1), m.waits.Load(), "WAITAOF ran on the connection that wrote")
}

func TestCluster_AskRedirect_GetAndDeleteAreFollowed(t *testing.T) {
	t.Parallel()
	id := docref.NewID()
	fields := map[string]string{
		"reference_id": id, "tenant_id": "t", "bucket": "b", "object_key": "t/x", "content_type": "text/plain",
		"size": "1", "sha256": strings.Repeat("a", 64), "created_at": "1700000000000", "updated_at": "1700000000000",
	}
	hash := fmt.Sprintf("*%d\r\n", 2*len(fields))
	for k, v := range fields {
		hash += fmt.Sprintf("$%d\r\n%s\r\n$%d\r\n%s\r\n", len(k), k, len(v), v)
	}
	m := newMigratingPair(t, hash)
	s, ctx := valkeystore.New(m.client, valkeystore.Options{}), context.Background()

	got, found, err := s.Get(ctx, "t", id)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, id, got.ID)
	assert.NoError(t, s.Delete(ctx, "t", id))
}

// An ASK to a node outside the slot map (a new, empty node a slot migrates
// to) cannot be followed: unavailable, retried.
func TestCluster_AskToUnknownNode_IsUnavailable(t *testing.T) {
	t.Parallel()
	c := fakeCluster(t, func([]string) string { return "-ASK 1234 127.0.0.1:1\r\n" })
	s, ctx := valkeystore.New(c, valkeystore.Options{}), context.Background()
	_, err := s.Put(ctx, sampleRef())
	assert.ErrorIs(t, err, docref.ErrUnavailable, "put")
	_, _, err = s.Get(ctx, "t", docref.NewID())
	assert.ErrorIs(t, err, docref.ErrUnavailable, "get")
	assert.ErrorIs(t, s.Delete(ctx, "t", docref.NewID()), docref.ErrUnavailable, "delete")
}

// MOVED after a resharding: the slot map is reloaded and the command is
// retried once on the new master.
func TestCluster_MovedRedirect_RetriesOnNewMaster(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, op := range map[string]func(*valkeystore.Store) error{
		"put": func(s *valkeystore.Store) error { _, err := s.Put(ctx, sampleRef()); return err },
		"get": func(s *valkeystore.Store) error {
			_, found, err := s.Get(ctx, "t", docref.NewID())
			assert.False(t, found)
			return err
		},
		"delete": func(s *valkeystore.Store) error { return s.Delete(ctx, "t", docref.NewID()) },
	} {
		var moved atomic.Bool
		var newAddr atomic.Value
		newMaster := scriptedNode(t, func(args []string) string {
			switch args[0] {
			case "WAITAOF":
				return "*2\r\n:1\r\n:0\r\n"
			case "HGETALL":
				return "*0\r\n"
			case "EVAL":
				return createdReply("1700000000000")
			}
			return "-ERR unknown command\r\n"
		})
		newAddr.Store(newMaster)
		oldMaster := scriptedNode(t, func([]string) string {
			moved.Store(true)
			return fmt.Sprintf("-MOVED 1234 %s\r\n", newAddr.Load())
		})
		c := redis.NewClusterClient(&redis.ClusterOptions{
			Addrs: []string{oldMaster}, Protocol: 2, DisableIdentity: true, MaxRedirects: -1, MaxRetries: -1,
			ClusterSlots: func(context.Context) ([]redis.ClusterSlot, error) {
				owner := oldMaster
				if moved.Load() {
					owner = newMaster
				}
				return []redis.ClusterSlot{{Start: 0, End: 16383, Nodes: []redis.ClusterNode{{Addr: owner}}}}, nil
			},
		})
		t.Cleanup(func() { _ = c.Close() })

		require.NoError(t, op(valkeystore.New(c, valkeystore.Options{})), name)
		assert.True(t, moved.Load(), "%s: the old master redirected", name)
	}
}

// A cluster where one node refuses CONFIG GET and another is not durable is
// a *DurabilityError, never ErrConfigUnavailable: a worker tolerating the
// latter must not start on a non-durable node.
func TestCheckDurability_ConfigUnavailableNeverHidesADurabilityError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	notDurable := fakeClusterWithReplica(t, func(args []string) string {
		if args[0] == "CONFIG" {
			return "-ERR unknown command 'CONFIG'\r\n"
		}
		return "*2\r\n:1\r\n:0\r\n"
	}, func(args []string) string {
		if args[0] == "CONFIG" && args[2] == "appendonly" {
			return "*2\r\n$10\r\nappendonly\r\n$2\r\nno\r\n"
		}
		if args[0] == "CONFIG" {
			return configReply(args)
		}
		return "-ERR unexpected\r\n"
	})
	err := valkeystore.CheckDurability(ctx, notDurable)
	var derr *valkeystore.DurabilityError
	require.ErrorAs(t, err, &derr)
	assert.NotErrorIs(t, err, valkeystore.ErrConfigUnavailable)
	assert.Contains(t, err.Error(), "Valkey configuration cannot be read", "the message still names every failure")

	allBlocked := fakeClusterWithReplica(t, func(args []string) string {
		if args[0] == "CONFIG" {
			return "-ERR unknown command 'CONFIG'\r\n"
		}
		return "*2\r\n:1\r\n:0\r\n"
	}, func([]string) string { return "-ERR unknown command 'CONFIG'\r\n" })
	err = valkeystore.CheckDurability(ctx, allBlocked)
	require.ErrorIs(t, err, valkeystore.ErrConfigUnavailable, "every node only refused CONFIG GET")
	assert.False(t, errors.As(err, &derr))
}
