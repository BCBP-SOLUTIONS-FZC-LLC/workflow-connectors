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

type MockStorageClient struct {
	mu      sync.Mutex
	objects map[string]mockObject
	err     error
}

func NewMockStorageClient() *MockStorageClient {
	return &MockStorageClient{
		objects: make(map[string]mockObject),
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

var _ StorageProviderClient = (*MockStorageClient)(nil)
