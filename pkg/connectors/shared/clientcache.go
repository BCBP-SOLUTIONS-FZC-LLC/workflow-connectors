package shared

import (
	"io"
	"sync"
	"sync/atomic"
)

// ClientCache holds provider clients keyed by credential set, with
// reference-counted lifetimes so a client is closed only once nobody uses it.
//
// Every Acquire returns a ClientHandle that holds one reference; Release gives
// it back. Reset and Invalidate retire entries: a retired entry leaves the map
// at once, so no new caller can acquire it, but its client is closed only when
// its last handle is released (immediately, if it has none). A caller
// therefore never receives a closed client, and an in-flight call never loses
// its client to another goroutine's reset.
//
// A client is closed by calling Close when it implements io.Closer; other
// clients are simply dropped. Close errors are ignored: the client is being
// discarded and there is no caller left to report them to.
type ClientCache[C any] struct {
	mu      sync.Mutex
	entries map[string]*clientEntry[C]
	limit   int
	clock   uint64 // advances on every Acquire, for least-recently-used eviction
}

type clientEntry[C any] struct {
	client   C
	refs     int
	retired  bool
	closed   bool
	lastUsed uint64
}

// NewClientCache returns a cache holding at most limit entries. Adding an entry
// to a full cache retires the least recently acquired one, so a busy worker
// with more credential sets than the limit keeps its hot clients.
func NewClientCache[C any](limit int) *ClientCache[C] {
	return &ClientCache[C]{entries: make(map[string]*clientEntry[C]), limit: limit}
}

// Acquire returns a handle to the client cached under key, calling build to
// create one on a miss. build runs without the cache lock held. When two
// callers miss the same key at once, the first stored client wins and the
// other is closed unused. The caller must Release the handle:
//
//	h, err := cache.Acquire(key, build)
//	if err != nil { return err }
//	defer h.Release()
//	use(h.Client())
func (c *ClientCache[C]) Acquire(key string, build func() (C, error)) (*ClientHandle[C], error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		e.refs++
		c.clock++
		e.lastUsed = c.clock
		c.mu.Unlock()
		return &ClientHandle[C]{cache: c, entry: e}, nil
	}
	c.mu.Unlock()

	client, err := build()
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		e.refs++
		c.mu.Unlock()
		closeClient(client)
		return &ClientHandle[C]{cache: c, entry: e}, nil
	}
	var toClose []C
	if len(c.entries) >= c.limit {
		if client, ok := c.evictOldestLocked(); ok {
			toClose = append(toClose, client)
		}
	}
	c.clock++
	e := &clientEntry[C]{client: client, refs: 1, lastUsed: c.clock}
	c.entries[key] = e
	c.mu.Unlock()

	closeAll(toClose)
	return &ClientHandle[C]{cache: c, entry: e}, nil
}

// Reset retires every cached client. Clients with no handle outstanding are
// closed now; the rest are closed when their last handle is released.
func (c *ClientCache[C]) Reset() {
	c.mu.Lock()
	toClose := c.retireAllLocked()
	c.mu.Unlock()
	closeAll(toClose)
}

// Invalidate retires the client cached under key, if any, the same way Reset
// retires all of them.
func (c *ClientCache[C]) Invalidate(key string) {
	c.mu.Lock()
	var toClose []C
	if e, ok := c.entries[key]; ok {
		delete(c.entries, key)
		if client, ok := c.retireLocked(e); ok {
			toClose = append(toClose, client)
		}
	}
	c.mu.Unlock()
	closeAll(toClose)
}

// Len reports how many clients are cached (retired ones excluded).
func (c *ClientCache[C]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// evictOldestLocked retires the least recently acquired entry. An entry in
// use is still only closed when its last handle is released.
func (c *ClientCache[C]) evictOldestLocked() (C, bool) {
	var oldestKey string
	var oldest *clientEntry[C]
	for key, e := range c.entries {
		if oldest == nil || e.lastUsed < oldest.lastUsed {
			oldestKey, oldest = key, e
		}
	}
	if oldest == nil {
		var zero C
		return zero, false
	}
	delete(c.entries, oldestKey)
	return c.retireLocked(oldest)
}

func (c *ClientCache[C]) retireAllLocked() []C {
	var toClose []C
	for key, e := range c.entries {
		delete(c.entries, key)
		if client, ok := c.retireLocked(e); ok {
			toClose = append(toClose, client)
		}
	}
	return toClose
}

// retireLocked marks e retired and reports whether it must be closed now
// because no handle holds it. It never schedules a close twice.
func (c *ClientCache[C]) retireLocked(e *clientEntry[C]) (C, bool) {
	e.retired = true
	if e.refs == 0 && !e.closed {
		e.closed = true
		return e.client, true
	}
	var zero C
	return zero, false
}

func (c *ClientCache[C]) release(e *clientEntry[C]) {
	c.mu.Lock()
	e.refs--
	mustClose := e.refs == 0 && e.retired && !e.closed
	if mustClose {
		e.closed = true
	}
	client := e.client
	c.mu.Unlock()

	if mustClose {
		closeClient(client)
	}
}

// ClientHandle is one reference to a cached client. Release it exactly when
// the call that acquired it is finished; Release is safe to call more than
// once, and only the first call counts.
type ClientHandle[C any] struct {
	cache    *ClientCache[C]
	entry    *clientEntry[C]
	released atomic.Bool
}

// Client returns the client. It panics after Release: using a client after
// giving back its reference could race with its Close.
func (h *ClientHandle[C]) Client() C {
	if h.released.Load() {
		panic("shared: ClientHandle.Client called after Release")
	}
	return h.entry.client
}

func (h *ClientHandle[C]) Release() {
	if h.released.CompareAndSwap(false, true) {
		h.cache.release(h.entry)
	}
}

func closeClient[C any](client C) {
	if closer, ok := any(client).(io.Closer); ok {
		_ = closer.Close()
	}
}

func closeAll[C any](clients []C) {
	for _, client := range clients {
		closeClient(client)
	}
}
