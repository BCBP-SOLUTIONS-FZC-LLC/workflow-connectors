package docref

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"
)

// MemoryStore keeps ref metadata in one process. It is for unit tests only:
// other replicas cannot see its refs and a restart loses them. Production
// uses valkeystore.
type MemoryStore struct {
	mu      sync.Mutex
	refs    map[string]memRef // keyed by id
	ttl     time.Duration
	now     func() time.Time
	failPut error
}

type memRef struct {
	ref     Ref
	expires time.Time
}

func NewMemoryStore() *MemoryStore { return NewMemoryStoreWithTTL(DefaultTTL) }

// NewMemoryStoreWithTTL is NewMemoryStore with a TTL; ttl <= 0 means
// DefaultTTL, as for the Valkey store.
func NewMemoryStoreWithTTL(ttl time.Duration) *MemoryStore {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &MemoryStore{refs: make(map[string]memRef), ttl: ttl, now: time.Now}
}

// SetClock replaces the clock, for expiry tests.
func (m *MemoryStore) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

// FailPut makes every Put return err (nil restores normal behaviour).
func (m *MemoryStore) FailPut(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failPut = err
}

func (m *MemoryStore) Put(_ context.Context, ref Ref) (Ref, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPut != nil {
		return Ref{}, m.failPut
	}
	if ref.ID == "" {
		ref.ID = NewID()
	}
	now := m.now()
	if existing, ok := m.refs[ref.ID]; ok && now.Before(existing.expires) {
		stored := existing.ref
		stored.CreatedAt, stored.UpdatedAt = ref.CreatedAt, ref.UpdatedAt
		if stored == ref {
			return existing.ref, nil // a retry of this same Put
		}
		return Ref{}, ErrExists
	}
	ref.CreatedAt, ref.UpdatedAt = now, now
	m.refs[ref.ID] = memRef{ref: ref, expires: now.Add(m.ttl)}
	return ref, nil
}

func (m *MemoryStore) Get(_ context.Context, tenantID, id string) (Ref, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.refs[id]
	if !ok || !m.now().Before(r.expires) || r.ref.TenantID != tenantID {
		return Ref{}, false, nil
	}
	return r.ref, true, nil
}

func (m *MemoryStore) Delete(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.refs[id]; ok && r.ref.TenantID == tenantID {
		delete(m.refs, id)
	}
	return nil
}

// Overwrite replaces a stored ref's metadata as-is, for tampering tests.
func (m *MemoryStore) Overwrite(ref Ref) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refs[ref.ID] = memRef{ref: ref, expires: m.now().Add(m.ttl)}
}

var _ Store = (*MemoryStore)(nil)

// MemoryContent keeps document content in one process, for unit tests only.
// Production uses s3content.
type MemoryContent struct {
	mu      sync.Mutex
	bucket  string
	objects map[string][]byte // keyed by bucket + "/" + key
}

func NewMemoryContent(bucket string) *MemoryContent {
	return &MemoryContent{bucket: bucket, objects: make(map[string][]byte)}
}

func (c *MemoryContent) Bucket() string { return c.bucket }

func (c *MemoryContent) Put(_ context.Context, key string, content []byte, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.objects[c.bucket+"/"+key] = bytes.Clone(content)
	return nil
}

func (c *MemoryContent) Open(_ context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.objects[bucket+"/"+key]
	if !ok {
		return nil, 0, ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
}

func (c *MemoryContent) Delete(_ context.Context, bucket, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.objects, bucket+"/"+key)
	return nil
}

// Len reports how many objects are stored.
func (c *MemoryContent) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.objects)
}

var _ ContentStore = (*MemoryContent)(nil)

// NewMemoryService is a Service over in-memory stores, for unit tests.
func NewMemoryService() *Service {
	return NewService(NewMemoryStore(), NewMemoryContent("test-documents"))
}
