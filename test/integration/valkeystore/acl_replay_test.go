//go:build integration

package valkeystore_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
)

// runbookACL returns the worker ACL rules exactly as the runbook documents
// them: the code line starting "ACL SETUSER docref-worker on".
func runbookACL(t *testing.T) []string {
	t.Helper()
	doc, err := os.ReadFile("../../../docs/runbooks/document-refs.md")
	require.NoError(t, err)
	const head = "ACL SETUSER docref-worker "
	for line := range strings.SplitSeq(string(doc), "\n") {
		if rules, ok := strings.CutPrefix(strings.TrimSpace(line), head); ok {
			return strings.Fields(rules)
		}
	}
	t.Fatalf("docs/runbooks/document-refs.md has no %q line", head)
	return nil
}

// The runbook's worker ACL is complete: a user created with exactly those
// rules runs every store operation, including the commands the Lua scripts
// call (Valkey checks those against the same user).
func TestACL_DocumentedRulesSuffice(t *testing.T) {
	t.Parallel()
	admin := sharedClient(t)
	ctx := context.Background()

	user, password := "docref-worker-"+uuid.NewString()[:8], uuid.NewString()
	args := []any{"ACL", "SETUSER", user}
	for _, rule := range runbookACL(t) {
		args = append(args, strings.ReplaceAll(rule, "<password>", password))
	}
	require.NoError(t, admin.Do(ctx, args...).Err(), "the documented rules are valid ACL syntax")
	t.Cleanup(func() { _ = admin.Do(context.Background(), "ACL", "DELUSER", user).Err() })

	worker := redis.NewClient(&redis.Options{Addr: admin.Options().Addr, Username: user, Password: password})
	t.Cleanup(func() { _ = worker.Close() })
	require.NoError(t, worker.Ping(ctx).Err())

	require.NoError(t, valkeystore.CheckDurability(ctx, worker))
	tn := tenant()
	for name, opts := range map[string]valkeystore.Options{
		"with WAITAOF":    {},
		"without WAITAOF": {DisableWaitAOF: true}, // EVALSHA, then EVAL on NOSCRIPT
	} {
		s := valkeystore.New(worker, opts)
		ref, err := s.Put(ctx, meta(tn))
		require.NoError(t, err, name)

		_, err = s.Put(ctx, ref)
		require.NoError(t, err, "%s: an identical retry (the idempotent path)", name)
		other := meta(tn)
		other.ID = ref.ID
		_, err = s.Put(ctx, other)
		require.ErrorIs(t, err, docref.ErrExists, name)

		got, found, err := s.Get(ctx, tn, ref.ID)
		require.NoError(t, err, name)
		require.True(t, found, name)
		assert.Equal(t, ref.ObjectKey, got.ObjectKey, name)

		require.NoError(t, s.Delete(ctx, tn, ref.ID), name)
		_, found, err = s.Get(ctx, tn, ref.ID)
		require.NoError(t, err, name)
		assert.False(t, found, name)
	}

	// The rules are a fence, not just a list: nothing else is allowed.
	assert.True(t, redis.IsPermissionError(worker.FlushAll(ctx).Err()), "FLUSHALL is denied")
	assert.True(t, redis.IsPermissionError(worker.Get(ctx, "not-a-ref").Err()), "keys outside docref:* are denied")
}

// lossyProxy forwards one command at a time to Valkey and relays each reply,
// except that the first create script (EVAL/EVALSHA) that Valkey executes
// successfully has its reply dropped and the client connection closed: the
// write is applied but the client never learns it.
type lossyProxy struct {
	upstream string
	mu       sync.Mutex
	dropped  bool
	scripts  int
}

func (p *lossyProxy) serve(t *testing.T) string {
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
			go p.relay(conn)
		}
	}()
	return ln.Addr().String()
}

func (p *lossyProxy) relay(client net.Conn) {
	defer func() { _ = client.Close() }()
	server, err := net.Dial("tcp", p.upstream)
	if err != nil {
		return
	}
	defer func() { _ = server.Close() }()
	fromClient, fromServer := bufio.NewReader(client), bufio.NewReader(server)
	for {
		var cmd strings.Builder
		name, err := readValue(fromClient, &cmd)
		if err != nil {
			return
		}
		if _, err := io.WriteString(server, cmd.String()); err != nil {
			return
		}
		var reply strings.Builder
		if _, err := readValue(fromServer, &reply); err != nil {
			return
		}
		if name == "EVAL" || name == "EVALSHA" {
			p.mu.Lock()
			p.scripts++
			drop := !p.dropped && !strings.HasPrefix(reply.String(), "-")
			p.dropped = p.dropped || drop
			p.mu.Unlock()
			if drop {
				return // applied server-side; the reply is lost with the connection
			}
		}
		if _, err := io.WriteString(client, reply.String()); err != nil {
			return
		}
	}
}

// readValue copies one RESP2 value from r to out and returns the upper-cased
// first bulk string of an array (the command name).
func readValue(r *bufio.Reader, out *strings.Builder) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	out.WriteString(line)
	body := strings.TrimSpace(line[1:])
	switch line[0] {
	case '$':
		n, _ := strconv.Atoi(body)
		if n < 0 {
			return "", nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		out.Write(buf)
		return strings.ToUpper(string(buf[:n])), nil
	case '*':
		n, _ := strconv.Atoi(body)
		first := ""
		for i := range n {
			s, err := readValue(r, out)
			if err != nil {
				return "", err
			}
			if i == 0 {
				first = s
			}
		}
		return first, nil
	default: // + - :
		return "", nil
	}
}

// A create whose reply is lost after Valkey applied it is retried by the
// client on a new connection; the retry finds this call's own write and
// succeeds, rather than reporting ErrExists for a ref that was stored.
func TestPut_LostReply_RetryIsIdempotent(t *testing.T) {
	t.Parallel()
	direct := sharedClient(t)
	ctx, tn := context.Background(), tenant()

	for name, opts := range map[string]valkeystore.Options{
		"with WAITAOF":    {},
		"without WAITAOF": {DisableWaitAOF: true},
	} {
		proxy := &lossyProxy{upstream: direct.Options().Addr}
		c := redis.NewClient(&redis.Options{Addr: proxy.serve(t), Protocol: 2, DisableIdentity: true, MaxRetries: 3, MinRetryBackoff: time.Millisecond})
		s := valkeystore.New(c, opts)

		want := meta(tn)
		put, err := s.Put(ctx, want)
		require.NoError(t, err, "%s: the retry finds its own write", name)
		_ = c.Close()

		proxy.mu.Lock()
		dropped, scripts := proxy.dropped, proxy.scripts
		proxy.mu.Unlock()
		require.True(t, dropped, "%s: the first reply was lost", name)
		require.GreaterOrEqual(t, scripts, 2, "%s: the client retried", name)

		got, found, err := valkeystore.New(direct, valkeystore.Options{}).Get(ctx, tn, want.ID)
		require.NoError(t, err, name)
		require.True(t, found, name)
		assert.Equal(t, got.CreatedAt, put.CreatedAt, "%s: the retry reports the original write's timestamp", name)
		assert.Equal(t, want.ObjectKey, got.ObjectKey, name)
	}
}

// A different hash at the same key is still refused, through the same
// replay path, and is left untouched.
func TestPut_ExistingDifferentRef_IsStillErrExists(t *testing.T) {
	t.Parallel()
	c := sharedClient(t)
	ctx, tn := context.Background(), tenant()
	s := valkeystore.New(c, valkeystore.Options{})
	first, err := s.Put(ctx, meta(tn))
	require.NoError(t, err)
	before, err := c.HGetAll(ctx, valkeystore.Key(first.ID)).Result()
	require.NoError(t, err)

	for field, mutate := range map[string]func(*docref.Ref){
		"tenant_id":    func(r *docref.Ref) { r.TenantID = tenant() },
		"object_key":   func(r *docref.Ref) { r.ObjectKey += "x" },
		"sha256":       func(r *docref.Ref) { r.SHA256 = strings.Repeat("0", 64) },
		"size":         func(r *docref.Ref) { r.Size++ },
		"bucket":       func(r *docref.Ref) { r.Bucket += "x" },
		"content_type": func(r *docref.Ref) { r.ContentType = "text/plain" },
	} {
		other := first
		mutate(&other)
		_, err := s.Put(ctx, other)
		assert.ErrorIs(t, err, docref.ErrExists, field)
	}
	after, err := c.HGetAll(ctx, valkeystore.Key(first.ID)).Result()
	require.NoError(t, err)
	assert.Equal(t, before, after, "an existing ref is never modified")
	assert.Equal(t, fmt.Sprint(first.CreatedAt.UnixMilli()), after["updated_at"])
}
