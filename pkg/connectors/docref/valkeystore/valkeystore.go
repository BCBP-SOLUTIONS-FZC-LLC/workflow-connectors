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
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
)

// DefaultWaitAOFTimeout bounds the wait for a write to reach the AOF.
const DefaultWaitAOFTimeout = 2 * time.Second

type Options struct {
	// TTL is how long a ref stays resolvable (docref.DefaultTTL when <= 0).
	// Valkey expiries have millisecond precision: a positive TTL below 1 ms
	// is rounded up to 1 ms.
	TTL time.Duration
	// DisableWaitAOF skips the WAITAOF after each write. Leave it false in
	// production: without it a ref can be lost if Valkey restarts before its
	// next fsync.
	DisableWaitAOF bool
	// WaitAOFTimeout bounds the WAITAOF (DefaultWaitAOFTimeout when <= 0).
	// A positive value below 1 ms is rounded up to 1 ms: WAITAOF takes
	// milliseconds, and 0 would mean "wait forever".
	WaitAOFTimeout time.Duration
	// WaitReplicas is how many replicas must also confirm each write in
	// their own append-only file (WAITAOF 1 <WaitReplicas>) before Put
	// returns. 0 (the default) confirms the primary's AOF only, and a ref
	// acknowledged that way can be lost if the primary fails over before a
	// replica has it. Set it to at most the number of replicas each primary
	// has (with appendonly yes on them), or every Put fails with
	// ErrNotPersisted. Negative is invalid (see Options.Validate) unless
	// DisableWaitAOF is set: WaitReplicas is then ignored.
	WaitReplicas int
}

// ErrInvalidOptions means the Options cannot configure a store.
var ErrInvalidOptions = errors.New("docref: invalid Valkey store options")

// Validate reports options New cannot honour: a negative WaitReplicas (not
// checked with DisableWaitAOF, which ignores WaitReplicas). Call it at
// startup; a Store built from invalid options refuses every Put with the
// same error (never docref.ErrUnavailable, which would be retried).
func (o Options) Validate() error {
	if !o.DisableWaitAOF && o.WaitReplicas < 0 {
		return fmt.Errorf("%w: WaitReplicas is %d, want 0 or more", ErrInvalidOptions, o.WaitReplicas)
	}
	return nil
}

type Store struct {
	client  redis.UniversalClient
	opts    Options
	invalid error        // from Options.Validate: refuses every Put
	masters *masterCache // a pointer keeps Store comparable
}

// New returns a store on client. Invalid options (see Options.Validate) make
// every Put fail with ErrInvalidOptions; New itself never fails.
func New(client redis.UniversalClient, opts Options) *Store {
	switch {
	case opts.TTL <= 0:
		opts.TTL = docref.DefaultTTL
	case opts.TTL < time.Millisecond:
		opts.TTL = time.Millisecond
	}
	switch {
	case opts.WaitAOFTimeout <= 0:
		opts.WaitAOFTimeout = DefaultWaitAOFTimeout
	case opts.WaitAOFTimeout < time.Millisecond:
		opts.WaitAOFTimeout = time.Millisecond
	}
	// WAITAOF blocks server-side for up to WaitAOFTimeout; the client must
	// still be reading then, or every Put fails with an i/o timeout.
	if rt := readTimeout(client); rt > 0 && opts.WaitAOFTimeout > rt-readTimeoutMargin {
		opts.WaitAOFTimeout = max(rt-readTimeoutMargin, minWaitAOFTimeout)
	}
	return &Store{client: client, opts: opts, invalid: opts.Validate(), masters: &masterCache{}}
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
	if s.invalid != nil {
		return docref.Ref{}, fmt.Errorf("docref: put: %w", s.invalid)
	}
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
		started := time.Now()
		node, err := s.nodeFor(ctx, key)
		if err != nil {
			return docref.Ref{}, err
		}
		put, wait := s.putOn(ctx, node, false, key, args)
		if err := put.Err(); err != nil {
			// A cluster redirect: the script did not run. Follow it once,
			// with the WAITAOF still on the same connection as the script.
			if next, asking := s.follow(ctx, err, node, key, started); next != nil {
				put, wait = s.putOn(ctx, next, asking, key, args)
			}
		}
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

// putOn sends the create script and its WAITAOF to node as one pipeline —
// one connection — preceded by ASKING when following an ASK redirect (it
// lets the importing node of a migrating slot serve the key). A refused
// ASKING (a user without +asking: NOPERM) is reported as the put's error,
// so the missing grant surfaces instead of the redirect it causes (see
// askingError).
func (s *Store) putOn(ctx context.Context, node *redis.Client, asking bool, key string, args []any) (put, wait *redis.Cmd) {
	var ask *redis.Cmd
	_, _ = node.Pipelined(ctx, func(p redis.Pipeliner) error {
		if asking {
			ask = p.Do(ctx, "ASKING")
		}
		put = putScript.Eval(ctx, p, []string{key}, args...)
		wait = p.Do(ctx, "WAITAOF", 1, s.opts.WaitReplicas, s.opts.WaitAOFTimeout.Milliseconds())
		return nil
	})
	if ask != nil {
		if err := askingError(ask, put); err != nil {
			put.SetErr(err)
		}
	}
	return put, wait
}

// askingError is the error that replaces cmd's when the ASKING sent before
// it was refused: only a server reply refusing ASKING (NOPERM, LOADING, …)
// and only when cmd itself failed — then cmd's error is the ASK redirect the
// refusal caused, which hides the cause. A connection failure fails both
// commands with the same error, and a cmd that succeeded keeps its result:
// in both cases cmd's own outcome is reported (nil is returned).
func askingError(ask *redis.Cmd, cmd redis.Cmder) error {
	if cmd.Err() == nil {
		return nil
	}
	if _, reply := errors.AsType[redis.Error](ask.Err()); !reply {
		return nil
	}
	return fmt.Errorf("ASKING: %w", ask.Err())
}

// follow returns the node a cluster redirect (MOVED or ASK) sends the
// command to, and whether it must be preceded by ASKING; nil when err is no
// redirect, the client is not a cluster client, or the target cannot be
// found. from is the node that answered with the redirect, key the command's
// key, started when the operation began.
//
// The commands here run on a single node's client, which does not follow
// redirects itself (a cluster pipeline would route the keyless WAITAOF to
// another connection).
//
// MOVED means the client's slot map is stale: it is reloaded once —
// synchronously, and only if the map still names from and no other
// operation's reload started after this one did (one in progress is waited
// for), so a burst of MOVED replies costs one CLUSTER SLOTS — and the
// command goes to the key's master in the new map (no address matching). A
// map that still names from (the reload failed or lags) is not followed.
//
// ASK says nothing about the slot map — one slot is being migrated — so it
// does not reload it: the target is looked up in a cache of the masters'
// addresses, refreshed (one CLUSTER SLOTS) only on a miss and at most once
// per askRefreshInterval. Addresses are compared as host and port
// (net.SplitHostPort, host case-insensitive), against both the address the
// node announces and the one the client dials. An ASK target that owns no
// slot yet — a new, empty node a slot is migrating to — is not in the slot
// map and cannot be reached: refs in that slot are docref.ErrUnavailable
// until the slot's migration completes.
//
// TLS: a TLS-only node (port 0, tls-port set, tls-cluster yes) announces its
// TLS port both in CLUSTER SLOTS and in the redirects it sends a TLS client,
// so the two match. A redirect naming port 0 — the node announces no port
// for this connection's protocol, a misconfiguration — matches no master
// and is docref.ErrUnavailable.
func (s *Store) follow(ctx context.Context, err error, from *redis.Client, key string, started time.Time) (node *redis.Client, asking bool) {
	c, ok := s.client.(*redis.ClusterClient)
	if !ok {
		return nil, false
	}
	if addr, ok := redis.IsAskError(err); ok {
		return s.masters.lookup(ctx, c, addr, started), true
	}
	if _, ok := redis.IsMovedError(err); !ok {
		return nil, false
	}
	if node, err := c.MasterForKey(ctx, key); err == nil && node != from {
		return node, false // another operation's reload already moved the slot
	}
	s.masters.refresh(ctx, c, started, 0)
	node, err = c.MasterForKey(ctx, key)
	if err != nil || node == from {
		return nil, false
	}
	return node, false
}

// askRefreshInterval bounds how often an ASK to an unknown address reloads
// the slot map.
const askRefreshInterval = time.Second

// masterCache maps the cluster's master addresses to their clients, so an
// ASK target is found without a slot-map reload per redirect.
type masterCache struct {
	mu     sync.Mutex // guards every field
	byAddr map[string]*redis.Client
	// loadedAt is when the last successful refresh started (zero: none, or
	// forgotten). A refresh that started after an operation did reflects
	// the cluster as it was when that operation saw a redirect, or later.
	loadedAt time.Time
	// running is closed when the refresh in progress ends; nil when none
	// is: one refresh at a time, the others wait for its result.
	running chan struct{}
	// generation counts forget calls: a refresh that started before one
	// does not store its result.
	generation uint64
}

// lookup returns the master at addr, refreshing the cache on a miss.
func (m *masterCache) lookup(ctx context.Context, c *redis.ClusterClient, addr string, started time.Time) *redis.Client {
	addr = normAddr(addr)
	if node := m.get(addr); node != nil {
		return node
	}
	m.refresh(ctx, c, started, askRefreshInterval)
	return m.get(addr)
}

func (m *masterCache) get(addr string) *redis.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byAddr[addr]
}

// forget empties the cache: the next lookup refreshes it, and a refresh
// already running does not store its (possibly stale) result.
func (m *masterCache) forget() {
	m.mu.Lock()
	m.byAddr, m.loadedAt = nil, time.Time{}
	m.generation++
	m.mu.Unlock()
}

// refresh reloads the slot map (ForEachMaster reloads it synchronously) and
// rebuilds the cache from its masters — unless a refresh that started after
// since succeeded, or the last successful one started less than minAge ago.
//
// One refresh runs at a time; the others wait for it, each only as long as
// its own context allows (a waiter whose context ends returns at once, with
// the cache as it is). A waiter reuses the result of the refresh it waited
// for if that refresh succeeded; if it failed, the waiter tries again.
func (m *masterCache) refresh(ctx context.Context, c *redis.ClusterClient, since time.Time, minAge time.Duration) {
	for {
		m.mu.Lock()
		loadedAt := m.loadedAt
		if loadedAt.After(since) || time.Since(loadedAt) < minAge {
			m.mu.Unlock()
			return
		}
		running := m.running
		if running == nil {
			running = make(chan struct{})
			m.running = running
			generation := m.generation
			m.mu.Unlock()
			m.load(ctx, c, generation, running)
			return
		}
		m.mu.Unlock()
		select {
		case <-running:
		case <-ctx.Done():
			return
		}
		m.mu.Lock()
		succeeded := m.loadedAt.After(loadedAt)
		m.mu.Unlock()
		if succeeded {
			return
		}
	}
}

// load is one refresh: it rebuilds the cache from the masters of a freshly
// loaded slot map and closes done. It records the refresh as fresh
// (loadedAt, the time it started) only if it succeeded.
func (m *masterCache) load(ctx context.Context, c *redis.ClusterClient, generation uint64, done chan struct{}) {
	started := time.Now()
	var mu sync.Mutex
	byAddr := map[string]*redis.Client{}
	defer func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.running = nil
		close(done)
	}()
	// ForEachMaster falls back on the current map when the reload fails
	// (for instance when ctx ends during it); it errs only when there is no
	// map at all or when fn does, which it never does. A reload cut short
	// by ctx therefore still yields the old map: usable for this lookup, but
	// not recorded as fresh, so the next miss reloads again.
	err := c.ForEachMaster(ctx, func(_ context.Context, master *redis.Client) error {
		// NodeAddress is the address the cluster announces (the one in a
		// redirect); Addr may be rewritten from it by the client.
		opt := master.Options()
		mu.Lock()
		defer mu.Unlock()
		byAddr[normAddr(opt.Addr)] = master
		if opt.NodeAddress != "" {
			byAddr[normAddr(opt.NodeAddress)] = master
		}
		return nil
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil || len(byAddr) == 0 || generation != m.generation {
		return
	}
	m.byAddr = byAddr
	if ctx.Err() == nil {
		m.loadedAt = started
	}
}

// normAddr is addr as host:port with a lower-case host, so the same node
// matches however its address is spelt (IPv6 brackets, host case).
func normAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return net.JoinHostPort(strings.ToLower(host), port)
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

// busyPrefixes are server refusals of a command that was not applied, for
// lack of capacity: BUSY (a script or function is running past
// busy-reply-threshold) and a server at maxclients.
// (HasErrorPrefix strips a leading "ERR ", so the second matches "ERR max
// number of clients reached".)
var busyPrefixes = []string{"BUSY ", "max number of clients reached"}

// transient reports a failure after which a retry may succeed: the command
// was refused without being applied, or never reached the server. Besides
// the topology replies above, that is OOM (maxmemory reached under
// noeviction: refused, nothing evicted), NOREPLICAS (min-replicas-to-write
// not met), BUSY, a server at maxclients, and a client connection pool with
// no free connection within PoolTimeout — and, on a cluster, a node client
// the cluster client has closed (a cached ASK target that left the slot
// map). Each is docref.ErrUnavailable, never a missing or permanently failed
// ref.
//
// On a cluster a topology reply other than ASK also reloads the slot map, so
// the retry reaches the key's current master: commands here run on one
// node's client, which does not follow redirects itself (Put, Get and Delete
// follow one redirect, see follow). ASK says nothing about the slot map — a
// slot is being migrated — so it reloads nothing. A standalone or Sentinel
// client has no slot map; Sentinel's failover client finds the new primary
// itself.
//
// A cluster node that fails without any reply — connection refused or
// reset, an i/o timeout: a master that died — reloads the slot map too
// (see nodeFailed). This decides nothing about the class: such errors are
// classified by their cause (shared.ClassifyCause) like any network error.
func (s *Store) transient(ctx context.Context, err error) bool {
	c, cluster := s.client.(*redis.ClusterClient)
	if cluster && nodeFailed(ctx, err) {
		// The cluster client's own commands mark a failing node and reload
		// the slot map; these run on the node's client directly, which does
		// neither, so a dead master would stay in the map until the next
		// periodic reload. ReloadState is asynchronous and deduplicated.
		c.ReloadState(ctx)
		s.masters.forget()
	}
	for _, prefix := range topologyPrefixes {
		// HasErrorPrefix matches a server reply however it is wrapped.
		if redis.HasErrorPrefix(err, prefix) {
			if cluster && prefix != "ASK " {
				c.ReloadState(ctx)
			}
			return true
		}
	}
	for _, prefix := range busyPrefixes {
		if redis.HasErrorPrefix(err, prefix) {
			return true
		}
	}
	if cluster && errors.Is(err, redis.ErrClosed) {
		// A cached node client the cluster client has since closed (the
		// node left the slot map): look the masters up afresh next time.
		s.masters.forget()
		return true
	}
	return redis.IsReadOnlyError(err) || redis.IsOOMError(err) || redis.IsNoReplicasError(err) ||
		redis.IsMaxClientsError(err) || errors.Is(err, redis.ErrPoolTimeout)
}

// nodeFailed reports a failure of the node itself rather than a refusal:
// no server reply in err's chain, and not the caller's own cancellation or
// deadline (which says nothing about the node).
func nodeFailed(ctx context.Context, err error) bool {
	if _, reply := errors.AsType[redis.Error](err); reply {
		return false
	}
	return ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// errUnsupportedClient refuses a client this store cannot address one node of.
var errUnsupportedClient = errors.New("docref: unsupported Valkey client")

// nodeFor returns the client serving key: the client itself on a standalone
// or Sentinel-managed server, the key's master on a cluster. A cluster whose
// slot map cannot be loaded is docref.ErrUnavailable, unless the caller's
// context ended.
func (s *Store) nodeFor(ctx context.Context, key string) (*redis.Client, error) {
	switch c := s.client.(type) {
	case *redis.Client:
		return c, nil
	case *redis.ClusterClient:
		node, err := c.MasterForKey(ctx, key)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return nil, fmt.Errorf("docref: cluster slot map: %w", err)
			}
			return nil, fmt.Errorf("%w: cluster slot map: %w", docref.ErrUnavailable, err)
		}
		return node, nil
	default:
		return nil, fmt.Errorf("%w %T: use *redis.Client (standalone or Sentinel) or *redis.ClusterClient", errUnsupportedClient, s.client)
	}
}

// onNode runs cmd on node, preceded by ASKING on the same connection when
// asking is set. A refused ASKING (NOPERM: the user lacks +asking) becomes
// cmd's error when cmd failed (see askingError).
func onNode[C redis.Cmder](ctx context.Context, node *redis.Client, asking bool, cmd func(redis.Cmdable) C) C {
	if !asking {
		return cmd(node)
	}
	var c C
	var ask *redis.Cmd
	_, _ = node.Pipelined(ctx, func(p redis.Pipeliner) error {
		ask = p.Do(ctx, "ASKING")
		c = cmd(p)
		return nil
	})
	if err := askingError(ask, c); err != nil {
		c.SetErr(err)
	}
	return c
}

// Get reads the ref; another tenant's ref is reported as not found, so its
// existence is not revealed.
func (s *Store) Get(ctx context.Context, tenantID, id string) (docref.Ref, bool, error) {
	// Read from the key's master, as Put writes there: a client configured to
	// read from replicas (ReadOnly, RouteByLatency, RouteRandomly) could
	// otherwise miss a ref written a moment ago and report it not found — a
	// permanent failure for the workflow.
	key := Key(id)
	started := time.Now()
	node, err := s.nodeFor(ctx, key)
	if err != nil {
		return docref.Ref{}, false, err
	}
	hgetall := func(c redis.Cmdable) *redis.MapStringStringCmd { return c.HGetAll(ctx, key) }
	fields, err := hgetall(node).Result()
	if err != nil {
		if next, asking := s.follow(ctx, err, node, key, started); next != nil {
			fields, err = onNode(ctx, next, asking, hgetall).Result()
		}
	}
	if err != nil {
		if s.transient(ctx, err) {
			return docref.Ref{}, false, fmt.Errorf("%w: get: %w", docref.ErrUnavailable, err)
		}
		return docref.Ref{}, false, fmt.Errorf("docref: get: %w", err)
	}
	if len(fields) == 0 {
		return docref.Ref{}, false, nil // missing or expired
	}
	if fields["tenant_id"] != tenantID {
		// Checked before anything else in the hash: another tenant's ref —
		// even a corrupt one — reads as not found, so neither its existence
		// nor its state is revealed.
		return docref.Ref{}, false, nil
	}
	ref, err := decode(fields)
	if err != nil {
		// A hash this store cannot have written: corrupt or forged.
		return docref.Ref{}, false, fmt.Errorf("%w: get %s: %w", docref.ErrIntegrityViolation, id, err)
	}
	if ref.ID != Key(id) {
		return docref.Ref{}, false, nil
	}
	return ref, true, nil
}

// Delete removes the ref if it belongs to tenantID.
func (s *Store) Delete(ctx context.Context, tenantID, id string) error {
	key := Key(id)
	started := time.Now()
	node, err := s.nodeFor(ctx, key)
	if err != nil {
		return err
	}
	err = deleteScript.Run(ctx, node, []string{key}, tenantID).Err()
	if err != nil {
		if next, asking := s.follow(ctx, err, node, key, started); next != nil {
			// EVAL, not EVALSHA: in a pipeline Run cannot fall back on
			// NOSCRIPT, and the target node may not have the script cached.
			err = onNode(ctx, next, asking, func(c redis.Cmdable) *redis.Cmd {
				return deleteScript.Eval(ctx, c, []string{key}, tenantID)
			}).Err()
		}
	}
	if err != nil {
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
