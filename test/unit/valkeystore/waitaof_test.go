package valkeystore_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
)

// slowFsyncNode accepts the create script, then holds WAITAOF for the full
// timeout the client asked for (an fsync that never completes) and reports
// no acknowledgement.
func slowFsyncNode(t *testing.T) string {
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
					switch strings.ToUpper(args[0]) {
					case "WAITAOF":
						ms, _ := strconv.Atoi(args[3])
						time.Sleep(time.Duration(min(ms, 3000)) * time.Millisecond)
						_, _ = conn.Write([]byte("*2\r\n:0\r\n:0\r\n"))
					default: // EVAL: created now
						_, _ = fmt.Fprintf(conn, "*2\r\n:1\r\n:%d\r\n", time.Now().UnixMilli())
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// A WaitAOFTimeout at or beyond the client's read timeout would turn every
// unconfirmed write into an i/o timeout; it is clamped below it.
func TestWaitAOFTimeout_ClampedBelowClientReadTimeout(t *testing.T) {
	t.Parallel()
	c := redis.NewClient(&redis.Options{Addr: slowFsyncNode(t), Protocol: 2, DisableIdentity: true, MaxRetries: -1, ReadTimeout: 400 * time.Millisecond})
	t.Cleanup(func() { _ = c.Close() })

	s := valkeystore.New(c, valkeystore.Options{WaitAOFTimeout: 5 * time.Second})
	start := time.Now()
	_, err := s.Put(context.Background(), docref.Ref{TenantID: "t", Bucket: "b", ObjectKey: "t/x", Size: 1, SHA256: strings.Repeat("a", 64)})
	assert.ErrorIs(t, err, valkeystore.ErrNotPersisted, "the server answered: not confirmed")
	assert.NotContains(t, err.Error(), "timeout", "the client was still reading when WAITAOF returned")
	assert.Less(t, time.Since(start), 400*time.Millisecond)
}

// Options.WaitReplicas asks WAITAOF for replica acknowledgement too; a write
// confirmed by fewer replicas is not reported as stored.
func TestPut_WaitReplicas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	node := func(replicaAcks int) (*redis.Client, func() []string) {
		var waitArgs []string
		var mu sync.Mutex
		c := fakeClient(t, func(args []string) string {
			if args[0] == "WAITAOF" {
				mu.Lock()
				waitArgs = args
				mu.Unlock()
				return fmt.Sprintf("*2\r\n:1\r\n:%d\r\n", replicaAcks)
			}
			return createdReply("1700000000000")
		})
		return c, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return waitArgs
		}
	}

	short, args := node(1)
	_, err := valkeystore.New(short, valkeystore.Options{WaitReplicas: 2}).Put(ctx, sampleRef())
	require.ErrorIs(t, err, valkeystore.ErrNotPersisted)
	assert.ErrorIs(t, err, docref.ErrUnavailable)
	assert.Contains(t, err.Error(), "1 of 2 replicas")
	assert.Equal(t, "2", args()[2], "WAITAOF numreplicas")

	enough, args := node(2)
	_, err = valkeystore.New(enough, valkeystore.Options{WaitReplicas: 2}).Put(ctx, sampleRef())
	require.NoError(t, err)
	assert.Equal(t, []string{"WAITAOF", "1", "2"}, args()[:3])

	local, args := node(0)
	_, err = valkeystore.New(local, valkeystore.Options{}).Put(ctx, sampleRef())
	require.NoError(t, err, "by default only the local AOF is required")
	assert.Equal(t, "0", args()[2])
}
