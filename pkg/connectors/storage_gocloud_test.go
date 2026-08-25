package connectors

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gocloud.dev/blob"
	_ "gocloud.dev/blob/memblob"
)

func newTestGocloudStorageClient(t *testing.T) *gocloudStorageClient {
	t.Helper()
	bucket, err := blob.OpenBucket(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bucket.Close() })
	return &gocloudStorageClient{bucket: bucket}
}

func TestGocloudStorageClient_UploadThenFetch_RoundTrips(t *testing.T) {
	t.Parallel()

	client := newTestGocloudStorageClient(t)
	ctx := context.Background()

	require.NoError(t, client.Upload(ctx, "ignored-bucket-name", "k1", []byte("hello"), "text/plain"))

	content, contentType, err := client.Fetch(ctx, "ignored-bucket-name", "k1")
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), content)
	assert.Equal(t, "text/plain", contentType)
}

func TestGocloudStorageClient_Delete(t *testing.T) {
	t.Parallel()

	client := newTestGocloudStorageClient(t)
	ctx := context.Background()

	require.NoError(t, client.Upload(ctx, "b", "k1", []byte("x"), "text/plain"))
	require.NoError(t, client.Delete(ctx, "b", "k1"))

	_, _, err := client.Fetch(ctx, "b", "k1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUpstream))
}

func TestGocloudStorageClient_FetchMissingKey_IsUpstreamError(t *testing.T) {
	t.Parallel()

	client := newTestGocloudStorageClient(t)
	_, _, err := client.Fetch(context.Background(), "b", "never-uploaded")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUpstream))
}

func TestNewGocloudStorageClient_UnsupportedProvider(t *testing.T) {
	t.Parallel()

	_, err := newGocloudStorageClient(context.Background(), "google-drive", "bucket", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrValidation))
}

func TestOpenS3Bucket_MissingCredentials_IsValidationError(t *testing.T) {
	t.Parallel()

	_, err := openS3Bucket(context.Background(), "bucket", map[string]any{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrValidation))
}

func TestOpenAzureBucket_MissingCredentials_IsValidationError(t *testing.T) {
	t.Parallel()

	_, err := openAzureBucket(context.Background(), "container", map[string]any{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrValidation))
}

func TestOpenGCSBucket_MissingCredentials_IsValidationError(t *testing.T) {
	t.Parallel()

	_, err := openGCSBucket(context.Background(), "bucket", map[string]any{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrValidation))
}
