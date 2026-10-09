package docref_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
)

var errBackend = errors.New("backend down")

// faultyStore wraps a MemoryStore and fails the operations it is told to.
type faultyStore struct {
	*docref.MemoryStore
	getErr, deleteErr error
}

func (f *faultyStore) Get(ctx context.Context, tenantID, id string) (docref.Ref, bool, error) {
	if f.getErr != nil {
		return docref.Ref{}, false, f.getErr
	}
	return f.MemoryStore.Get(ctx, tenantID, id)
}

func (f *faultyStore) Delete(ctx context.Context, tenantID, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.MemoryStore.Delete(ctx, tenantID, id)
}

// faultyContent wraps a MemoryContent: it fails the operations it is told to,
// and can serve a replacement body whose length it does not report (size -1),
// as an S3 response without Content-Length does.
type faultyContent struct {
	*docref.MemoryContent
	putErr, openErr, deleteErr error
	unsized                    []byte
}

func (f *faultyContent) Put(ctx context.Context, key string, content []byte, contentType string) error {
	if f.putErr != nil {
		return f.putErr
	}
	return f.MemoryContent.Put(ctx, key, content, contentType)
}

func (f *faultyContent) Open(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	if f.openErr != nil {
		return nil, 0, f.openErr
	}
	if f.unsized != nil {
		return io.NopCloser(bytes.NewReader(f.unsized)), -1, nil
	}
	return f.MemoryContent.Open(ctx, bucket, key)
}

func (f *faultyContent) Delete(ctx context.Context, bucket, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.MemoryContent.Delete(ctx, bucket, key)
}

func newFaultyService(t *testing.T) (*docref.Service, *faultyStore, *faultyContent, docref.Ref) {
	t.Helper()
	refs := &faultyStore{MemoryStore: docref.NewMemoryStore()}
	content := &faultyContent{MemoryContent: docref.NewMemoryContent("docs")}
	svc := docref.NewService(refs, content)
	ref, err := svc.Create(context.Background(), "tenant-1", "text/plain", []byte("hello"))
	require.NoError(t, err)
	return svc, refs, content, ref
}

func TestService_ContentPutFailure_IsTransient(t *testing.T) {
	t.Parallel()
	svc, _, content, _ := newFaultyService(t)
	content.putErr = errBackend
	_, err := svc.Create(context.Background(), "tenant-1", "text/plain", []byte("x"))
	require.ErrorIs(t, err, errBackend)
	assert.False(t, docref.IsResolutionFailure(err))
}

func TestService_InvalidTenant_RefusedOnEveryResolution(t *testing.T) {
	t.Parallel()
	svc, _, _, ref := newFaultyService(t)
	ctx := context.Background()
	_, err := svc.Lookup(ctx, "a/b", ref.ID)
	assert.ErrorIs(t, err, docref.ErrInvalidTenant, "lookup")
	_, _, err = svc.Open(ctx, "a/b", ref.ID)
	assert.ErrorIs(t, err, docref.ErrInvalidTenant, "open")
	assert.ErrorIs(t, svc.Delete(ctx, "a/b", ref.ID), docref.ErrInvalidTenant, "delete: only not-found is swallowed")
}

func TestService_StoreReadFailure_IsTransient(t *testing.T) {
	t.Parallel()
	svc, refs, _, ref := newFaultyService(t)
	refs.getErr = errBackend
	_, err := svc.Lookup(context.Background(), "tenant-1", ref.ID)
	require.ErrorIs(t, err, errBackend)
	assert.False(t, docref.IsResolutionFailure(err))
}

func TestService_ContentOpenFailure_IsTransient(t *testing.T) {
	t.Parallel()
	svc, _, content, ref := newFaultyService(t)
	content.openErr = errBackend
	_, _, err := svc.Open(context.Background(), "tenant-1", ref.ID)
	require.ErrorIs(t, err, errBackend)
	assert.False(t, docref.IsResolutionFailure(err))
}

func TestService_Delete_ReferenceDeleteFailure(t *testing.T) {
	t.Parallel()
	svc, refs, content, ref := newFaultyService(t)
	refs.deleteErr = errBackend
	require.ErrorIs(t, svc.Delete(context.Background(), "tenant-1", ref.ID), errBackend)
	assert.Equal(t, 1, content.Len(), "the object is kept while its reference still resolves")
}

func TestService_Delete_ObjectDeleteFailure_ReferenceIsGone(t *testing.T) {
	t.Parallel()
	svc, _, content, ref := newFaultyService(t)
	content.deleteErr = errBackend
	err := svc.Delete(context.Background(), "tenant-1", ref.ID)
	require.ErrorIs(t, err, errBackend)
	assert.Contains(t, err.Error(), "lifecycle rule")
	_, err = svc.Lookup(context.Background(), "tenant-1", ref.ID)
	assert.ErrorIs(t, err, docref.ErrNotFound)
}

// Without a reported size, the stream itself catches a body that differs in
// length from the reference — and the failure is sticky.
func TestService_Open_UnsizedBody_VerifiedWhileStreaming(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"longer":  "hello, world",
		"shorter": "hel",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc, _, content, ref := newFaultyService(t)
			content.unsized = []byte(body)

			_, r, err := svc.Open(context.Background(), "tenant-1", ref.ID)
			require.NoError(t, err, "nothing to compare before reading")
			defer func() { _ = r.Close() }()
			_, err = io.Copy(io.Discard, r)
			require.ErrorIs(t, err, docref.ErrIntegrityViolation)

			n, err := r.Read(make([]byte, 8))
			assert.Zero(t, n)
			assert.ErrorIs(t, err, docref.ErrIntegrityViolation, "the violation is sticky")
		})
	}
}

func TestMemoryStore_PutIsCreateOnlyUntilExpiry(t *testing.T) {
	t.Parallel()
	refs := docref.NewMemoryStoreWithTTL(time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	refs.SetClock(func() time.Time { return now })
	ctx := context.Background()

	ref, err := refs.Put(ctx, docref.Ref{TenantID: "t"})
	require.NoError(t, err)
	_, err = refs.Put(ctx, docref.Ref{ID: ref.ID, TenantID: "other"})
	require.ErrorIs(t, err, docref.ErrExists)

	now = now.Add(time.Second)
	again, err := refs.Put(ctx, docref.Ref{ID: ref.ID, TenantID: "t"})
	require.NoError(t, err, "an identical retry is the same Put, not a conflict")
	assert.Equal(t, ref, again, "the retry returns the stored ref, original timestamps included")

	now = now.Add(time.Minute)
	_, found, err := refs.Get(ctx, "t", ref.ID)
	require.NoError(t, err)
	assert.False(t, found, "expired at its TTL")
	_, err = refs.Put(ctx, docref.Ref{ID: ref.ID, TenantID: "t"})
	assert.NoError(t, err, "an expired ID can be created again")
}

func TestNewMemoryService_RoundTrip(t *testing.T) {
	t.Parallel()
	svc, ctx := docref.NewMemoryService(), context.Background()
	ref, err := svc.Create(ctx, "tenant-1", "text/plain", []byte("x"))
	require.NoError(t, err)
	_, body, err := svc.Read(ctx, "tenant-1", ref.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, []byte("x"), body)
}
