//go:build integration

package s3content_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/s3content"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// s3Bucket connects to TEST_S3_ENDPOINT (CI and `make test-integration` run
// an S3-compatible server) and creates a fresh bucket.
func s3Bucket(t *testing.T) (*s3.Client, string) {
	t.Helper()
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("CI is set but TEST_S3_ENDPOINT is not: the S3 tests cannot be skipped in CI")
		}
		t.Skip("TEST_S3_ENDPOINT not set (make test-integration)")
	}
	client := s3.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	bucket := "docs-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	_, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	return client, bucket
}

func service(t *testing.T) (*docref.Service, *s3.Client, string) {
	t.Helper()
	client, bucket := s3Bucket(t)
	return docref.NewService(docref.NewMemoryStore(), s3content.New(client, bucket), docref.WithKeyPrefix("docrefs/")), client, bucket
}

func overwrite(t *testing.T, client *s3.Client, bucket, key string, body []byte) {
	t.Helper()
	_, err := client.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body)})
	require.NoError(t, err)
}

func TestStore_PutOpenDelete(t *testing.T) {
	t.Parallel()
	client, bucket := s3Bucket(t)
	s, ctx := s3content.New(client, bucket), context.Background()
	require.NoError(t, s.Check(ctx, "docrefs/"))

	require.NoError(t, s.Put(ctx, "t/a", []byte("\x00\xffbytes"), "application/octet-stream"))
	body, size, err := s.Open(ctx, bucket, "t/a")
	require.NoError(t, err)
	got, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	assert.Equal(t, []byte("\x00\xffbytes"), got)
	assert.Equal(t, int64(7), size)

	require.NoError(t, s.Delete(ctx, bucket, "t/a"))
	require.NoError(t, s.Delete(ctx, bucket, "t/a"), "deleting a missing object is not an error")
	_, _, err = s.Open(ctx, bucket, "t/a")
	assert.ErrorIs(t, err, docref.ErrObjectNotFound)
}

func TestStore_Check_MissingBucket(t *testing.T) {
	t.Parallel()
	client, _ := s3Bucket(t)
	assert.Error(t, s3content.New(client, "no-such-bucket-"+uuid.NewString()[:8]).Check(context.Background(), "docrefs/"))
}

// 1. Upload a document, resolve it from S3.
func TestS3_CreateThenResolve(t *testing.T) {
	t.Parallel()
	svc, client, bucket := service(t)
	ctx := context.Background()
	ref, err := svc.Create(ctx, "tenant-1", "application/pdf", []byte("%PDF-1.7 invoice"))
	require.NoError(t, err)
	assert.Equal(t, bucket, ref.Bucket)

	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(ref.ObjectKey)})
	require.NoError(t, err, "the content is in S3")
	assert.Equal(t, int64(16), aws.ToInt64(head.ContentLength))

	_, content, err := svc.Read(ctx, "tenant-1", ref.ID, 1<<20)
	require.NoError(t, err)
	assert.Equal(t, "%PDF-1.7 invoice", string(content))
}

// 3. A missing S3 object is DocumentSourceMissing.
func TestS3_MissingObject_IsSourceMissing(t *testing.T) {
	t.Parallel()
	svc, client, bucket := service(t)
	ctx := context.Background()
	ref, err := svc.Create(ctx, "tenant-1", "text/plain", []byte("soon gone"))
	require.NoError(t, err)
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(ref.ObjectKey)})
	require.NoError(t, err)

	_, _, err = svc.Read(ctx, "tenant-1", ref.ID, 1<<20)
	assert.ErrorIs(t, err, docref.ErrSourceMissing)
	_, _, err = svc.Open(ctx, "tenant-1", ref.ID)
	assert.ErrorIs(t, err, docref.ErrSourceMissing)
}

// 4. A checksum mismatch (same size, different bytes) is detected.
func TestS3_ChecksumMismatch_IsIntegrityViolation(t *testing.T) {
	t.Parallel()
	svc, client, bucket := service(t)
	ctx := context.Background()
	ref, err := svc.Create(ctx, "tenant-1", "text/plain", []byte("approved"))
	require.NoError(t, err)
	overwrite(t, client, bucket, ref.ObjectKey, []byte("rejected"))

	_, _, err = svc.Read(ctx, "tenant-1", ref.ID, 1<<20)
	assert.ErrorIs(t, err, docref.ErrIntegrityViolation)
}

// 9. Overwriting the S3 object fails validation, whatever its new size.
func TestS3_OverwrittenObject_FailsValidation(t *testing.T) {
	t.Parallel()
	svc, client, bucket := service(t)
	ctx := context.Background()
	ref, err := svc.Create(ctx, "tenant-1", "text/plain", []byte("contract v1"))
	require.NoError(t, err)
	overwrite(t, client, bucket, ref.ObjectKey, []byte("contract v2 with new terms"))

	_, _, err = svc.Read(ctx, "tenant-1", ref.ID, 1<<20)
	assert.ErrorIs(t, err, docref.ErrIntegrityViolation, "size mismatch, detected before the body is read")
}

// 5. A large document resolves as a stream, never held in memory.
func TestS3_LargeDocument_StreamsInBoundedMemory(t *testing.T) {
	svc, _, _ := service(t)
	ctx := context.Background()
	const size = 48 << 20
	ref := func() docref.Ref { // the source bytes go out of scope before measuring
		ref, err := svc.Create(ctx, "tenant-1", "application/octet-stream", bytes.Repeat([]byte("0123456789abcdef"), size/16))
		require.NoError(t, err)
		return ref
	}()

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
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(size/4), "the 48 MiB object is streamed, not buffered")
}

// 6. Concurrent resolution of one document succeeds.
func TestS3_ConcurrentResolution(t *testing.T) {
	t.Parallel()
	svc, _, _ := service(t)
	ctx := context.Background()
	body := bytes.Repeat([]byte("x"), 256<<10)
	ref, err := svc.Create(ctx, "tenant-1", "text/plain", body)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			_, got, err := svc.Read(ctx, "tenant-1", ref.ID, 1<<20)
			assert.NoError(t, err)
			assert.Equal(t, len(body), len(got))
		})
	}
	wg.Wait()
}

func TestS3_Delete_RemovesObject(t *testing.T) {
	t.Parallel()
	svc, client, bucket := service(t)
	ctx := context.Background()
	ref, err := svc.Create(ctx, "tenant-1", "text/plain", []byte("x"))
	require.NoError(t, err)
	require.NoError(t, svc.Delete(ctx, "tenant-1", ref.ID))

	_, err = client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(ref.ObjectKey)})
	assert.Error(t, err, "the object is gone")
}

// A real S3 NoSuchKey, as the SDK returns it, classifies as permanent.
func TestS3_RealNoSuchKey_IsPermanent(t *testing.T) {
	t.Parallel()
	client, bucket := s3Bucket(t)
	_, err := client.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("missing")})
	require.Error(t, err)

	class, reason := shared.ClassifyCause(err)
	assert.Equal(t, shared.ClassPermanent, class)
	assert.Equal(t, "api NoSuchKey", reason)
}

// A missing bucket is an infrastructure fault, not a missing document.
func TestS3_MissingBucket_IsNotSourceMissing(t *testing.T) {
	t.Parallel()
	client, _ := s3Bucket(t)
	s := s3content.New(client, "no-such-bucket-"+uuid.NewString()[:8])
	_, _, err := s.Open(context.Background(), s.Bucket(), "k")
	require.Error(t, err)
	assert.NotErrorIs(t, err, docref.ErrObjectNotFound)
}

// Check confirms a missing object reads as NoSuchKey with these credentials.
func TestStore_Check_ProbesMissingKey(t *testing.T) {
	t.Parallel()
	client, bucket := s3Bucket(t)
	assert.NoError(t, s3content.New(client, bucket).Check(context.Background(), "docrefs/"))
}
