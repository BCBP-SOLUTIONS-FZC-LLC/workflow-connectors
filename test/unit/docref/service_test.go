package docref_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
)

func newService() (*docref.Service, *docref.MemoryStore, *docref.MemoryContent) {
	refs, content := docref.NewMemoryStore(), docref.NewMemoryContent("docs")
	return docref.NewService(refs, content, docref.WithKeyPrefix("docrefs/")), refs, content
}

func TestService_CreateThenRead(t *testing.T) {
	t.Parallel()
	svc, _, content := newService()
	ctx := context.Background()

	ref, err := svc.Create(ctx, "tenant-1", "application/pdf", []byte("%PDF\x00\xff"))
	require.NoError(t, err)
	assert.True(t, docref.IsRef(ref.ID))
	assert.Equal(t, "docs", ref.Bucket)
	assert.Equal(t, "docrefs/tenant-1/"+ref.ID[len(docref.Prefix):], ref.ObjectKey)
	assert.Equal(t, int64(6), ref.Size)
	assert.Equal(t, docref.Checksum([]byte("%PDF\x00\xff")), ref.SHA256)
	assert.Equal(t, 1, content.Len(), "the content is written to the content store")

	got, body, err := svc.Read(ctx, "tenant-1", ref.ID, 1<<20)
	require.NoError(t, err)
	assert.Equal(t, []byte("%PDF\x00\xff"), body)
	assert.Equal(t, ref, got)
}

func TestService_MissingReference_IsNotFound(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService()
	_, _, err := svc.Read(context.Background(), "tenant-1", docref.NewID(), 1<<20)
	assert.ErrorIs(t, err, docref.ErrNotFound)
	assert.True(t, docref.IsResolutionFailure(err))
}

func TestService_MissingObject_IsSourceMissing(t *testing.T) {
	t.Parallel()
	svc, _, content := newService()
	ctx := context.Background()
	ref, err := svc.Create(ctx, "tenant-1", "text/plain", []byte("hello"))
	require.NoError(t, err)
	require.NoError(t, content.Delete(ctx, ref.Bucket, ref.ObjectKey))

	_, _, err = svc.Read(ctx, "tenant-1", ref.ID, 1<<20)
	assert.ErrorIs(t, err, docref.ErrSourceMissing)
}

func TestService_OverwrittenObject_IsIntegrityViolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, replacement := range map[string]string{
		"same size, different bytes": "HELLO",
		"shorter":                    "hi",
		"longer":                     "hello, world",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc, _, content := newService()
			ref, err := svc.Create(ctx, "tenant-1", "text/plain", []byte("hello"))
			require.NoError(t, err)
			require.NoError(t, content.Put(ctx, ref.ObjectKey, []byte(replacement), "text/plain"))

			_, _, err = svc.Read(ctx, "tenant-1", ref.ID, 1<<20)
			assert.ErrorIs(t, err, docref.ErrIntegrityViolation)
		})
	}
}

// A stream reports the violation at the end of the object, in place of EOF.
func TestService_Open_ChecksumMismatchSurfacesAtEOF(t *testing.T) {
	t.Parallel()
	svc, _, content := newService()
	ctx := context.Background()
	ref, err := svc.Create(ctx, "tenant-1", "text/plain", []byte("hello"))
	require.NoError(t, err)
	require.NoError(t, content.Put(ctx, ref.ObjectKey, []byte("HELLO"), "text/plain"))

	_, body, err := svc.Open(ctx, "tenant-1", ref.ID)
	require.NoError(t, err, "same size: nothing to detect before reading")
	defer func() { _ = body.Close() }()
	_, err = io.Copy(io.Discard, body)
	assert.ErrorIs(t, err, docref.ErrIntegrityViolation)
}

func TestService_TenantIsolation(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService()
	ctx := context.Background()
	ref, err := svc.Create(ctx, "tenant-a", "text/plain", []byte("tenant a only"))
	require.NoError(t, err)

	_, _, err = svc.Read(ctx, "tenant-b", ref.ID, 1<<20)
	assert.ErrorIs(t, err, docref.ErrNotFound, "another tenant's ref does not exist for it")
}

// A metadata record pointing at another tenant's object is refused before
// any object is fetched.
func TestService_ForgedReference_NeverReachesAnotherTenantsObject(t *testing.T) {
	t.Parallel()
	svc, refs, _ := newService()
	ctx := context.Background()
	victim, err := svc.Create(ctx, "tenant-a", "text/plain", []byte("secret"))
	require.NoError(t, err)
	own, err := svc.Create(ctx, "tenant-b", "text/plain", []byte("mine"))
	require.NoError(t, err)

	forged := own
	forged.ObjectKey, forged.Size, forged.SHA256 = victim.ObjectKey, victim.Size, victim.SHA256
	refs.Overwrite(forged)
	_, _, err = svc.Read(ctx, "tenant-b", own.ID, 1<<20)
	assert.ErrorIs(t, err, docref.ErrIntegrityViolation)

	forged = own
	forged.Bucket = "another-bucket"
	refs.Overwrite(forged)
	_, _, err = svc.Read(ctx, "tenant-b", own.ID, 1<<20)
	assert.ErrorIs(t, err, docref.ErrIntegrityViolation)
}

func TestService_InvalidTenant_IsRefused(t *testing.T) {
	t.Parallel()
	svc, _, content := newService()
	for _, tenant := range []string{"", "a/b", "..", `a\b`} {
		_, err := svc.Create(context.Background(), tenant, "text/plain", []byte("x"))
		assert.ErrorIs(t, err, docref.ErrInvalidTenant, tenant)
	}
	assert.Zero(t, content.Len())
}

func TestService_Read_RefusesOversizeBeforeDownloading(t *testing.T) {
	t.Parallel()
	svc, _, content := newService()
	ctx := context.Background()
	ref, err := svc.Create(ctx, "tenant-1", "text/plain", []byte("0123456789"))
	require.NoError(t, err)
	require.NoError(t, content.Delete(ctx, ref.Bucket, ref.ObjectKey))

	_, _, err = svc.Read(ctx, "tenant-1", ref.ID, 9)
	assert.ErrorIs(t, err, docref.ErrTooLarge, "decided from metadata alone: the missing object is never requested")
}

func TestService_FailedMetadataWrite_RemovesTheObject(t *testing.T) {
	t.Parallel()
	svc, refs, content := newService()
	refs.FailPut(errors.New("valkey down"))
	_, err := svc.Create(context.Background(), "tenant-1", "text/plain", []byte("x"))
	require.Error(t, err)
	assert.False(t, docref.IsResolutionFailure(err), "a store outage is transient")
	assert.Zero(t, content.Len(), "no orphaned object is left behind")
}

func TestService_Delete_RemovesReferenceAndObject(t *testing.T) {
	t.Parallel()
	svc, _, content := newService()
	ctx := context.Background()
	ref, err := svc.Create(ctx, "tenant-1", "text/plain", []byte("x"))
	require.NoError(t, err)

	require.NoError(t, svc.Delete(ctx, "tenant-2", ref.ID), "another tenant's delete is a no-op")
	assert.Equal(t, 1, content.Len())
	require.NoError(t, svc.Delete(ctx, "tenant-1", ref.ID))
	require.NoError(t, svc.Delete(ctx, "tenant-1", ref.ID), "deleting twice is not an error")
	assert.Zero(t, content.Len())
	_, _, err = svc.Read(ctx, "tenant-1", ref.ID, 1<<20)
	assert.ErrorIs(t, err, docref.ErrNotFound)
}

func TestService_ConcurrentResolution(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService()
	ctx := context.Background()
	ref, err := svc.Create(ctx, "tenant-1", "text/plain", bytes.Repeat([]byte("abc"), 10_000))
	require.NoError(t, err)

	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			_, body, err := svc.Read(ctx, "tenant-1", ref.ID, 1<<20)
			assert.NoError(t, err)
			assert.Len(t, body, 30_000)
		})
	}
	wg.Wait()
}

// generatedContent serves one large object generated on the fly, so the test
// itself never holds it in memory.
type generatedContent struct {
	size int64
}

func (generatedContent) Bucket() string { return "docs" }
func (generatedContent) Put(context.Context, string, []byte, string) error {
	return nil
}
func (g generatedContent) Open(context.Context, string, string) (io.ReadCloser, int64, error) {
	return io.NopCloser(io.LimitReader(&pattern{}, g.size)), g.size, nil
}
func (generatedContent) Delete(context.Context, string, string) error { return nil }

// pattern yields byte(offset % 251), independent of read sizes.
type pattern struct{ off int64 }

func (r *pattern) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte((r.off + int64(i)) % 251)
	}
	r.off += int64(len(p))
	return len(p), nil
}

func TestService_Open_StreamsLargeDocumentInBoundedMemory(t *testing.T) {
	const size = 256 << 20 // 256 MiB
	h := sha256.New()
	_, err := io.Copy(h, io.LimitReader(&pattern{}, size))
	require.NoError(t, err)

	refs := docref.NewMemoryStore()
	svc := docref.NewService(refs, generatedContent{size: size})
	ctx := context.Background()
	ref, err := refs.Put(ctx, docref.Ref{TenantID: "tenant-1", Bucket: "docs", ContentType: "application/octet-stream", Size: size, SHA256: hex.EncodeToString(h.Sum(nil))})
	require.NoError(t, err)
	ref.ObjectKey = "tenant-1/" + ref.ID[len(docref.Prefix):]
	refs.Overwrite(ref)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, body, err := svc.Open(ctx, "tenant-1", ref.ID)
	require.NoError(t, err)
	n, err := io.Copy(io.Discard, body)
	require.NoError(t, err, "verified at EOF")
	require.NoError(t, body.Close())
	runtime.ReadMemStats(&after)

	assert.Equal(t, int64(size), n)
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(8<<20), "a 256 MiB document resolves through a small buffer")
}

// A forged or corrupt record with an impossible size or checksum is refused
// before anything is read — never a panic, never served.
func TestService_MalformedMetadata_IsIntegrityViolation(t *testing.T) {
	t.Parallel()
	svc, refs, _ := newService()
	ctx := context.Background()
	ref, err := svc.Create(ctx, "tenant-1", "text/plain", []byte("x"))
	require.NoError(t, err)

	for name, mutate := range map[string]func(*docref.Ref){
		"negative size":    func(r *docref.Ref) { r.Size = -1 << 20 },
		"short checksum":   func(r *docref.Ref) { r.SHA256 = "abc" },
		"non-hex checksum": func(r *docref.Ref) { r.SHA256 = strings.Repeat("z", 64) },
	} {
		forged := ref
		mutate(&forged)
		refs.Overwrite(forged)
		_, _, err := svc.Read(ctx, "tenant-1", ref.ID, 1<<20)
		assert.ErrorIs(t, err, docref.ErrIntegrityViolation, name)
	}
}

// Only an ID the service issued is looked up; anything else is not found.
func TestService_MalformedID_IsNotFound(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService()
	for _, id := range []string{"docref:not-a-uuid", "docref:", "something-else", "docref:" + strings.Repeat("a", 36)} {
		_, _, err := svc.Read(context.Background(), "tenant-1", id, 1<<20)
		assert.ErrorIs(t, err, docref.ErrNotFound, id)
	}
}

func TestService_TenantIDsThatCannotScopeAKey_AreRefused(t *testing.T) {
	t.Parallel()
	svc, _, content := newService()
	for _, tenant := range []string{"bad\x00tenant", "line\nbreak", "\xff\xfe", strings.Repeat("t", 256)} {
		_, err := svc.Create(context.Background(), tenant, "text/plain", []byte("x"))
		assert.ErrorIs(t, err, docref.ErrInvalidTenant, "%q", tenant)
	}
	assert.Zero(t, content.Len())
}

func TestMemoryStore_ZeroTTLMeansDefault(t *testing.T) {
	t.Parallel()
	refs := docref.NewMemoryStoreWithTTL(0)
	ref, err := refs.Put(context.Background(), docref.Ref{TenantID: "t"})
	require.NoError(t, err)
	_, found, err := refs.Get(context.Background(), "t", ref.ID)
	require.NoError(t, err)
	assert.True(t, found, "as for valkeystore, a zero TTL means the 24 h default, not immediate expiry")
}

// A record must point at exactly the object Create wrote for its tenant and
// ID. A key elsewhere under the tenant's own prefix — another of the tenant's
// documents, or a nested path — is tampering, refused on every resolution
// path before S3 is touched.
func TestService_ObjectKeyNotDerivedFromID_IsIntegrityViolation(t *testing.T) {
	t.Parallel()
	svc, refs, content := newService()
	ctx := context.Background()
	other, err := svc.Create(ctx, "tenant-1", "text/plain", []byte("other document"))
	require.NoError(t, err)
	ref, err := svc.Create(ctx, "tenant-1", "text/plain", []byte("own"))
	require.NoError(t, err)

	for name, key := range map[string]string{
		"another document of the tenant": other.ObjectKey,
		"nested under the tenant prefix": "docrefs/tenant-1/x/" + strings.TrimPrefix(ref.ID, docref.Prefix),
		"no key prefix":                  "tenant-1/" + strings.TrimPrefix(ref.ID, docref.Prefix),
	} {
		forged := ref
		forged.ObjectKey, forged.Size, forged.SHA256 = key, other.Size, other.SHA256
		refs.Overwrite(forged)

		_, err := svc.Lookup(ctx, "tenant-1", ref.ID)
		assert.ErrorIs(t, err, docref.ErrIntegrityViolation, "lookup: %s", name)
		_, _, err = svc.Open(ctx, "tenant-1", ref.ID)
		assert.ErrorIs(t, err, docref.ErrIntegrityViolation, "open: %s", name)
		_, _, err = svc.Read(ctx, "tenant-1", ref.ID, 1<<20)
		assert.ErrorIs(t, err, docref.ErrIntegrityViolation, "read: %s", name)
		assert.ErrorIs(t, svc.Delete(ctx, "tenant-1", ref.ID), docref.ErrIntegrityViolation, "delete: %s", name)
		assert.Equal(t, 2, content.Len(), "delete: %s: no object removed", name)
	}
	_, _, err = svc.Read(ctx, "tenant-1", other.ID, 1<<20)
	assert.NoError(t, err, "the other document is untouched")
}
