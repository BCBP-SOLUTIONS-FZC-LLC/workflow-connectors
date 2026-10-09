// Package valkeystore stores document-reference metadata in Valkey, through
// go-redis (the platform's Valkey client). Every worker replica uses the same
// keys, so all replicas share one view.
//
// Valkey holds metadata only — the S3 location, content type, size and
// SHA-256 — never document content, so its memory grows with the number of
// refs, not with document size. Content lives in S3 (package s3content).
//
// Each ref is one hash at docref:<uuid>, created by an atomic Lua script that
// refuses an existing key, stamps server-side timestamps, writes the fields
// and sets the expiry (PEXPIREAT) in one step. The create is idempotent: a
// retry that finds the identical hash its own lost-reply attempt wrote
// succeeds, while any different existing hash is docref.ErrExists. By default
// Put then runs WAITAOF so it returns only once the write is in the local
// append-only file — a ref handed to the workflow survives a Valkey restart.
// WAITAOF 1 0 confirms the primary's own AOF only; a primary failover can
// still lose the ref unless Options.WaitReplicas asks for replica
// acknowledgement too (see docs/runbooks/document-refs.md, "Failover").
//
// Valkey must be configured for this role (see CheckDurability and
// docs/runbooks/document-refs.md): appendonly yes, appendfsync always or
// everysec, maxmemory-policy noeviction (ref keys carry a TTL, so any
// volatile-* or allkeys-* policy could evict them before they expire).
package valkeystore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
)

// DefaultWaitAOFTimeout bounds the wait for a write to reach the AOF.
const DefaultWaitAOFTimeout = 2 * time.Second

type Options struct {
	// TTL is how long a ref stays resolvable (docref.DefaultTTL when <= 0).
	TTL time.Duration
	// DisableWaitAOF skips the WAITAOF after each write. Leave it false in
	// production: without it a ref can be lost if Valkey restarts before its
	// next fsync.
	DisableWaitAOF bool
	// WaitAOFTimeout bounds the WAITAOF (DefaultWaitAOFTimeout when <= 0).
	WaitAOFTimeout time.Duration
	// WaitReplicas is how many replicas must also confirm each write in
	// their own append-only file (WAITAOF 1 <WaitReplicas>) before Put
	// returns. 0 (the default) confirms the primary's AOF only, and a ref
	// acknowledged that way can be lost if the primary fails over before a
	// replica has it. Set it to at most the number of replicas each primary
	// has (with appendonly yes on them), or every Put fails with
	// ErrNotPersisted. Ignored with DisableWaitAOF.
	WaitReplicas int
}

type Store struct {
	client redis.UniversalClient
	opts   Options
}

func New(client redis.UniversalClient, opts Options) *Store {
	if opts.TTL <= 0 {
		opts.TTL = docref.DefaultTTL
	}
	if opts.WaitAOFTimeout <= 0 {
		opts.WaitAOFTimeout = DefaultWaitAOFTimeout
	}
	// WAITAOF blocks server-side for up to WaitAOFTimeout; the client must
	// still be reading then, or every Put fails with an i/o timeout.
	if rt := readTimeout(client); rt > 0 && opts.WaitAOFTimeout > rt-readTimeoutMargin {
		opts.WaitAOFTimeout = max(rt-readTimeoutMargin, minWaitAOFTimeout)
	}
	return &Store{client: client, opts: opts}
}

const (
	readTimeoutMargin = 250 * time.Millisecond
	minWaitAOFTimeout = 100 * time.Millisecond
)

// readTimeout is the client's effective read timeout (go-redis: 0 means its
// 3 s default, negative means none).
func readTimeout(client redis.UniversalClient) time.Duration {
	var rt time.Duration
	switch c := client.(type) {
	case *redis.Client:
		rt = c.Options().ReadTimeout
	case *redis.ClusterClient:
		rt = c.Options().ReadTimeout
	default:
		return 0
	}
	switch {
	case rt == 0:
		return 3 * time.Second
	case rt < 0:
		return 0
	default:
		return rt
	}
}

// ErrNotPersisted means Valkey accepted a write but did not confirm it in its
// append-only file (and, with Options.WaitReplicas, in that many replicas'
// append-only files) within WaitAOFTimeout.
// It matches docref.ErrUnavailable: a retry may succeed.
var ErrNotPersisted = fmt.Errorf("%w: write not confirmed in the Valkey append-only file", docref.ErrUnavailable)

// Key returns the Valkey key of a ref: docref:<uuid>, the ref ID itself.
func Key(id string) string {
	return docref.Prefix + strings.TrimPrefix(id, docref.Prefix)
}

// Fields every ref hash has. There is no content field: content is in S3.
var Fields = []string{
	"reference_id", "tenant_id", "bucket", "object_key", "content_type",
	"size", "sha256", "created_at", "updated_at",
}

// putScript creates a ref hash only if the key does not exist.
// KEYS[1] key · ARGV[1] TTL in ms · ARGV[2..] field/value pairs.
// Returns {1, created_ms} when it created the hash, {2, created_ms} when the
// key already holds exactly these field values, and {0} when it holds
// anything else.
//
// The {2} case makes Put idempotent: if the reply to a create is lost (the
// connection drops after the script ran), the client's retry finds its own
// write — ref IDs are random UUIDs, so an identical hash can only be this
// Put's — and succeeds instead of reporting ErrExists for a ref that was in
// fact stored. It rewrites updated_at with its current value so the retry's
// connection has a write for the following WAITAOF to confirm: WAITAOF waits
// for the connection's last write, and the AOF is sequential, so confirming
// this write confirms the original one too.
var putScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  local names = {'created_at', 'updated_at'}
  for i = 2, #ARGV, 2 do names[#names + 1] = ARGV[i] end
  local cur = redis.call('HMGET', KEYS[1], unpack(names))
  for i = 2, #ARGV, 2 do
    if cur[2 + i / 2] ~= ARGV[i + 1] then return {0} end
  end
  if not cur[1] or not cur[2] then return {0} end
  redis.call('HSET', KEYS[1], 'updated_at', cur[2])
  return {2, cur[1]}
end
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local exp = now + tonumber(ARGV[1])
redis.call('HSET', KEYS[1],
  'created_at', string.format('%.0f', now),
  'updated_at', string.format('%.0f', now),
  unpack(ARGV, 2))
redis.call('PEXPIREAT', KEYS[1], string.format('%.0f', exp))
return {1, string.format('%.0f', now)}
`)

func (s *Store) Put(ctx context.Context, ref docref.Ref) (docref.Ref, error) {
	if ref.ID == "" {
		ref.ID = docref.NewID()
	}
	key := Key(ref.ID)
	args := []any{
		s.opts.TTL.Milliseconds(),
		"reference_id", ref.ID,
		"tenant_id", ref.TenantID,
		"bucket", ref.Bucket,
		"object_key", ref.ObjectKey,
		"content_type", ref.ContentType,
		"size", ref.Size,
		"sha256", ref.SHA256,
	}

	var res []any
	var err error
	if s.opts.DisableWaitAOF {
		res, err = putScript.Run(ctx, s.client, []string{key}, args...).Slice()
		if err != nil {
			return docref.Ref{}, s.putError(ctx, err)
		}
	} else {
		// WAITAOF confirms only the writes made on its own connection, so the
		// script and the WAITAOF go out as one pipeline on the key's node: a
		// pipeline runs on a single connection. Issued separately on a pooled
		// client, WAITAOF could land on another connection and confirm
		// nothing.
		node, err := nodeFor(ctx, s.client, key)
		if err != nil {
			return docref.Ref{}, err
		}
		var put, wait *redis.Cmd
		_, _ = node.Pipelined(ctx, func(p redis.Pipeliner) error {
			put = putScript.Eval(ctx, p, []string{key}, args...)
			wait = p.Do(ctx, "WAITAOF", 1, s.opts.WaitReplicas, s.opts.WaitAOFTimeout.Milliseconds())
			return nil
		})
		if res, err = put.Slice(); err != nil {
			return docref.Ref{}, s.putError(ctx, err)
		}
		if created, _ := res[0].(int64); created != 0 {
			acks, err := wait.Int64Slice()
			if err != nil {
				return docref.Ref{}, fmt.Errorf("%w: %w", ErrNotPersisted, err)
			}
			if len(acks) < 2 || acks[0] < 1 {
				return docref.Ref{}, ErrNotPersisted
			}
			if acks[1] < int64(s.opts.WaitReplicas) {
				return docref.Ref{}, fmt.Errorf("%w: %d of %d replicas confirmed", ErrNotPersisted, acks[1], s.opts.WaitReplicas)
			}
		}
	}

	if created, _ := res[0].(int64); created == 0 {
		return docref.Ref{}, docref.ErrExists
	}
	createdMS, err := strconv.ParseInt(fmt.Sprint(res[1]), 10, 64)
	if err != nil {
		return docref.Ref{}, fmt.Errorf("docref: put: timestamp: %w", err)
	}
	ref.CreatedAt = time.UnixMilli(createdMS).UTC()
	ref.UpdatedAt = ref.CreatedAt
	return ref, nil
}

func (s *Store) putError(ctx context.Context, err error) error {
	if s.transient(ctx, err) {
		return fmt.Errorf("%w: put: %w", docref.ErrUnavailable, err)
	}
	return fmt.Errorf("docref: put: %w", err)
}

// topologyPrefixes are server replies meaning the node addressed is not (or
// not yet) the one to serve the key: a cluster redirect (MOVED, ASK), a slot
// in migration or a cluster without quorum (TRYAGAIN, CLUSTERDOWN), a node
// still loading its dataset (LOADING), a node demoted to replica by a
// failover (READONLY), or a replica cut off from its primary (MASTERDOWN).
var topologyPrefixes = []string{"MOVED ", "ASK ", "TRYAGAIN", "CLUSTERDOWN", "LOADING", "READONLY", "MASTERDOWN"}

// transient reports a server refusal after which a retry may succeed: the
// command was refused without being applied. Besides the topology replies
// above, that is OOM (maxmemory reached under noeviction: refused, nothing
// evicted) and NOREPLICAS (min-replicas-to-write not met). Each is
// docref.ErrUnavailable, never a missing or permanently failed ref.
//
// On a cluster a topology reply also reloads the slot map, so the retry
// reaches the key's current master: commands here run on one node's client,
// which does not follow redirects itself. A standalone or Sentinel client
// has no slot map; Sentinel's failover client finds the new primary itself.
func (s *Store) transient(ctx context.Context, err error) bool {
	msg := err.Error()
	for _, prefix := range topologyPrefixes {
		if strings.HasPrefix(msg, prefix) {
			if c, ok := s.client.(*redis.ClusterClient); ok {
				c.ReloadState(ctx)
			}
			return true
		}
	}
	return redis.IsReadOnlyError(err) || redis.IsOOMError(err) || redis.IsNoReplicasError(err)
}

// nodeFor returns the client serving key: the client itself on a standalone
// or Sentinel-managed server, the key's master on a cluster.
func nodeFor(ctx context.Context, client redis.UniversalClient, key string) (*redis.Client, error) {
	switch c := client.(type) {
	case *redis.Client:
		return c, nil
	case *redis.ClusterClient:
		return c.MasterForKey(ctx, key)
	default:
		return nil, fmt.Errorf("docref: unsupported Valkey client %T: use *redis.Client (standalone or Sentinel) or *redis.ClusterClient", client)
	}
}

// Get reads the ref; another tenant's ref is reported as not found, so its
// existence is not revealed.
func (s *Store) Get(ctx context.Context, tenantID, id string) (docref.Ref, bool, error) {
	// Read from the key's master, as Put writes there: a client configured to
	// read from replicas (ReadOnly, RouteByLatency, RouteRandomly) could
	// otherwise miss a ref written a moment ago and report it not found — a
	// permanent failure for the workflow.
	node, err := nodeFor(ctx, s.client, Key(id))
	if err != nil {
		return docref.Ref{}, false, err
	}
	fields, err := node.HGetAll(ctx, Key(id)).Result()
	if err != nil {
		if s.transient(ctx, err) {
			return docref.Ref{}, false, fmt.Errorf("%w: get: %w", docref.ErrUnavailable, err)
		}
		return docref.Ref{}, false, fmt.Errorf("docref: get: %w", err)
	}
	if len(fields) == 0 {
		return docref.Ref{}, false, nil // missing or expired
	}
	ref, err := decode(fields)
	if err != nil {
		// A hash this store cannot have written: corrupt or forged.
		return docref.Ref{}, false, fmt.Errorf("%w: get %s: %w", docref.ErrIntegrityViolation, id, err)
	}
	if ref.TenantID != tenantID || ref.ID != Key(id) {
		return docref.Ref{}, false, nil
	}
	return ref, true, nil
}

// Delete removes the ref if it belongs to tenantID.
func (s *Store) Delete(ctx context.Context, tenantID, id string) error {
	node, err := nodeFor(ctx, s.client, Key(id))
	if err != nil {
		return err
	}
	if err := deleteScript.Run(ctx, node, []string{Key(id)}, tenantID).Err(); err != nil {
		if s.transient(ctx, err) {
			return fmt.Errorf("%w: delete: %w", docref.ErrUnavailable, err)
		}
		return fmt.Errorf("docref: delete: %w", err)
	}
	return nil
}

// deleteScript deletes KEYS[1] only when its tenant_id is ARGV[1].
var deleteScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'tenant_id') == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

func decode(f map[string]string) (docref.Ref, error) {
	size, err1 := strconv.ParseInt(f["size"], 10, 64)
	created, err2 := strconv.ParseInt(f["created_at"], 10, 64)
	updated, err3 := strconv.ParseInt(f["updated_at"], 10, 64)
	if err := errors.Join(err1, err2, err3); err != nil {
		return docref.Ref{}, fmt.Errorf("malformed ref hash: %w", err)
	}
	return docref.Ref{
		ID:          f["reference_id"],
		TenantID:    f["tenant_id"],
		Bucket:      f["bucket"],
		ObjectKey:   f["object_key"],
		ContentType: f["content_type"],
		Size:        size,
		SHA256:      f["sha256"],
		CreatedAt:   time.UnixMilli(created).UTC(),
		UpdatedAt:   time.UnixMilli(updated).UTC(),
	}, nil
}

var _ docref.Store = (*Store)(nil)
