package connectors

import (
	"context"
	"fmt"
	"sync"
)

type mockObject struct {
	content     []byte
	contentType string
}

// MockStorageClient is an in-memory StorageProviderClient — the default
// backing for the storage connector until a real S3-compatible SDK client is
// wired in via Config.StorageClient. Upload-then-Fetch on the same
// bucket/key round-trips the exact bytes, so it's exercised meaningfully in
// tests, not a pure no-op.
type MockStorageClient struct {
	mu      sync.Mutex
	objects map[string]mockObject
	docRefs map[string]mockObject
	err     error
}

func NewMockStorageClient() *MockStorageClient {
	return &MockStorageClient{
		objects: make(map[string]mockObject),
		docRefs: make(map[string]mockObject),
	}
}

func (m *MockStorageClient) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *MockStorageClient) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects = make(map[string]mockObject)
	m.docRefs = make(map[string]mockObject)
	m.err = nil
}

func objectKey(bucket, key string) string { return bucket + "/" + key }

func (m *MockStorageClient) Fetch(_ context.Context, bucket, key string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, "", m.err
	}
	obj, ok := m.objects[objectKey(bucket, key)]
	if !ok {
		return nil, "", fmt.Errorf("mock storage: object %s/%s not found", bucket, key)
	}
	return obj.content, obj.contentType, nil
}

func (m *MockStorageClient) Upload(_ context.Context, bucket, key string, content []byte, contentType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.objects[objectKey(bucket, key)] = mockObject{content: content, contentType: contentType}
	return nil
}

func (m *MockStorageClient) Delete(_ context.Context, bucket, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	delete(m.objects, objectKey(bucket, key))
	return nil
}

// RegisterDocRef and ResolveDocRef back storageConnector's optional
// DocRefResolver capability (storage.go) — this table only survives for the
// lifetime of one MockStorageClient instance, which is fine for a mock; a
// real SDK's document refs are self-describing and never need this.
func (m *MockStorageClient) RegisterDocRef(ref string, content []byte, contentType string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.docRefs[ref] = mockObject{content: content, contentType: contentType}
}

func (m *MockStorageClient) ResolveDocRef(ref string) ([]byte, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.docRefs[ref]
	return obj.content, obj.contentType, ok
}

var (
	_ StorageProviderClient = (*MockStorageClient)(nil)
	_ DocRefResolver        = (*MockStorageClient)(nil)
	_ docRefRegistrar       = (*MockStorageClient)(nil)
)
