package valkeystore_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
)

// movingNode answers every command with a MOVED redirect, as a cluster node
// does for a slot that has moved during resharding.
func movingNode(t *testing.T) string {
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
			go func() {
				defer func() { _ = conn.Close() }()
				r := bufio.NewReader(conn)
				for {
					// One RESP array per command: *N then N bulk strings.
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
					for range n {
						header, err := r.ReadString('\n') // $len
						if err != nil {
							return
						}
						size, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "$")))
						if _, err := io.CopyN(io.Discard, r, int64(size+2)); err != nil {
							return
						}
					}
					_, _ = conn.Write([]byte("-MOVED 3999 127.0.0.1:1\r\n"))
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// A redirect means the command did not run: it is reported as the store
// being temporarily unavailable (transient), never as a missing or
// permanently failed ref.
func TestRedirect_IsUnavailable(t *testing.T) {
	t.Parallel()
	c := redis.NewClient(&redis.Options{Addr: movingNode(t), Protocol: 2, DisableIdentity: true, MaxRetries: -1})
	t.Cleanup(func() { _ = c.Close() })
	s, ctx := valkeystore.New(c, valkeystore.Options{}), context.Background()

	_, err := s.Put(ctx, docref.Ref{TenantID: "t", Bucket: "b", ObjectKey: "t/x", Size: 1, SHA256: strings.Repeat("a", 64)})
	assert.ErrorIs(t, err, docref.ErrUnavailable, "put")
	_, _, err = s.Get(ctx, "t", docref.NewID())
	assert.ErrorIs(t, err, docref.ErrUnavailable, "get")
	assert.ErrorIs(t, s.Delete(ctx, "t", docref.NewID()), docref.ErrUnavailable, "delete")
}

// Every refusal after which a retry may succeed — a topology change (a
// failover demoting the node, a replica cut off from its primary, a node
// still loading), a full Valkey, or too few replicas for
// min-replicas-to-write — is docref.ErrUnavailable on every operation, on a
// standalone and on a cluster client: never a missing or permanently failed
// ref.
func TestTransientRefusals_AreUnavailable(t *testing.T) {
	t.Parallel()
	for _, reply := range []string{
		"-READONLY You can't write against a read only replica.\r\n",
		"-MASTERDOWN Link with MASTER is down and replica-serve-stale-data is set to 'no'.\r\n",
		"-NOREPLICAS Not enough good replicas to write.\r\n",
		"-LOADING Valkey is loading the dataset in memory\r\n",
		"-TRYAGAIN Multiple keys request during rehashing of slot\r\n",
		"-CLUSTERDOWN The cluster is down\r\n",
		"-OOM command not allowed when used memory > 'maxmemory'.\r\n",
	} {
		answer := func([]string) string { return reply }
		for name, client := range map[string]redis.UniversalClient{
			"standalone": fakeClient(t, answer),
			"cluster":    fakeCluster(t, answer),
		} {
			s, ctx := valkeystore.New(client, valkeystore.Options{}), context.Background()
			_, err := s.Put(ctx, sampleRef())
			assert.ErrorIs(t, err, docref.ErrUnavailable, "%s put: %s", name, reply)
			_, err = valkeystore.New(client, valkeystore.Options{DisableWaitAOF: true}).Put(ctx, sampleRef())
			assert.ErrorIs(t, err, docref.ErrUnavailable, "%s put without WAITAOF: %s", name, reply)
			_, _, err = s.Get(ctx, "t", docref.NewID())
			assert.ErrorIs(t, err, docref.ErrUnavailable, "%s get: %s", name, reply)
			assert.ErrorIs(t, s.Delete(ctx, "t", docref.NewID()), docref.ErrUnavailable, "%s delete: %s", name, reply)
		}
	}
}
