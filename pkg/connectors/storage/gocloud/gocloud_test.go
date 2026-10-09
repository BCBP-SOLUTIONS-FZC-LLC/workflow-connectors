package gocloud

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gocloud.dev/blob"
	_ "gocloud.dev/blob/memblob"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
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

	content, contentType, err := client.Fetch(ctx, "ignored-bucket-name", "k1", 50<<20)
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

	_, _, err := client.Fetch(ctx, "b", "k1", 50<<20)
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrUpstream))

	require.NoError(t, client.Delete(ctx, "b", "k1"), "deleting it again is idempotent")
	require.NoError(t, client.Delete(ctx, "b", "never-uploaded"))
}

func TestGocloudStorageClient_FetchMissingKey_IsUpstreamError(t *testing.T) {
	t.Parallel()

	client := newTestGocloudStorageClient(t)
	_, _, err := client.Fetch(context.Background(), "b", "never-uploaded", 50<<20)
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrUpstream))
}

func TestNewGocloudStorageClient_UnsupportedProvider(t *testing.T) {
	t.Parallel()

	_, err := newGocloudStorageClient(context.Background(), "google-drive", "bucket", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestOpenS3Bucket_MissingCredentials_IsValidationError(t *testing.T) {
	t.Parallel()

	_, err := openS3Bucket(context.Background(), "bucket", map[string]any{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestOpenAzureBucket_MissingCredentials_IsValidationError(t *testing.T) {
	t.Parallel()

	_, err := openAzureBucket(context.Background(), "container", map[string]any{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestOpenGCSBucket_MissingCredentials_IsValidationError(t *testing.T) {
	t.Parallel()

	_, err := openGCSBucket(context.Background(), "bucket", map[string]any{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestOpenGCSBucket_NonServiceAccountCredential_IsRejected(t *testing.T) {
	externalAccount := `{"type":"external_account","audience":"//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/q","subject_token_type":"urn:ietf:params:oauth:token-type:jwt","token_url":"https://sts.googleapis.com/v1/token","credential_source":{"file":"/etc/passwd"}}`

	_, err := openGCSBucket(context.Background(), "bucket", map[string]any{"gcpServiceAccountKey": externalAccount})

	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation), "a rejected credential type is permanent, not retryable")
	assert.False(t, errors.Is(err, shared.ErrUpstream))
}

func TestOpenS3Bucket_MissingRegion_IsValidationError(t *testing.T) {
	t.Parallel()

	_, err := openS3Bucket(context.Background(), "bucket", map[string]any{"accessKey": "a", "secretKey": "s"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestOpenS3Bucket_BuildsFromTenantValuesWithWorkerAWSEnvSet(t *testing.T) {
	t.Setenv("AWS_REGION", "eu-west-9")
	t.Setenv("AWS_ENDPOINT_URL", "http://worker-endpoint.invalid")

	client, err := openS3Bucket(context.Background(), "bucket", map[string]any{"accessKey": "a", "secretKey": "s", "region": "us-east-1"})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestGocloudStorageClient_FetchOverLimit_IsValidationError(t *testing.T) {
	t.Parallel()

	client := newTestGocloudStorageClient(t)
	ctx := context.Background()
	big := make([]byte, shared.MaxObjectBytes+1)
	w, err := client.bucket.NewWriter(ctx, "big", nil)
	require.NoError(t, err)
	_, err = w.Write(big)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	_, _, err = client.Fetch(ctx, "", "big", 50<<20)
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestGocloudStorageClient_Close_ReleasesBucket(t *testing.T) {
	t.Parallel()

	bucket, err := blob.OpenBucket(context.Background(), "mem://")
	require.NoError(t, err)
	client := &gocloudStorageClient{bucket: bucket, httpClient: newTransportClient()}

	var closer io.Closer = client
	require.NoError(t, closer.Close())
	_, _, err = client.Fetch(context.Background(), "", "k", 50<<20)
	assert.Error(t, err, "a closed bucket cannot be used")
}
