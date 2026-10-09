package gocloud

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gocloud.dev/blob/memblob"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

type apiErr struct{ code string }

func (e apiErr) Error() string     { return "api error " + e.code }
func (e apiErr) ErrorCode() string { return e.code }

func TestUpstream_S3CodesWin(t *testing.T) {
	t.Parallel()
	for err, want := range map[error]shared.Class{
		&types.NoSuchKey{Message: aws.String("missing")}:      shared.ClassPermanent,
		&types.NoSuchBucket{Message: aws.String("no bucket")}: shared.ClassPermanent,
		apiErr{"AccessDenied"}:                                shared.ClassPermanent,
		apiErr{"InvalidBucketName"}:                           shared.ClassPermanent,
		apiErr{"SlowDown"}:                                    shared.ClassTransient,
		apiErr{"RequestTimeout"}:                              shared.ClassTransient,
		apiErr{"InternalError"}:                               shared.ClassTransient,
		apiErr{"ServiceUnavailable"}:                          shared.ClassTransient,
	} {
		wrapped := upstream("gocloud fetch", fmt.Errorf("blob: %w", err))
		assert.True(t, errors.Is(wrapped, shared.ErrUpstream))
		class, _ := shared.ClassOf(wrapped)
		assert.Equal(t, want, class, err.Error())
	}
}

// A real gocloud error (any provider): its portable code classifies it.
func TestUpstream_GocloudCode(t *testing.T) {
	t.Parallel()
	c := &gocloudStorageClient{bucket: memblob.OpenBucket(nil)}
	defer func() { _ = c.Close() }()

	_, _, err := c.Fetch(context.Background(), "b", "missing", 50<<20)
	require.Error(t, err)
	class, reason := shared.ClassOf(err)
	assert.Equal(t, shared.ClassPermanent, class)
	assert.Equal(t, "gocloud NotFound", reason)
}

// Tenant-supplied names never reach a URL host or path unchecked.
func TestOpenAzureBucket_RejectsNamesThatChangeTheURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ account, container string }{
		{"internal-host:8443/x?", "c0ntainer"},
		{"UPPER", "c0ntainer"},
		{"ab", "c0ntainer"},
		{"account1", "c/../x"},
		{"account1", "c?x=1"},
		{"account1", "a--b"},
		{"account1", "-ab"},
	} {
		_, err := openAzureBucket(context.Background(), tc.container, map[string]any{"azureAccountName": tc.account, "azureAccountKey": "a2V5"})
		assert.ErrorIs(t, err, shared.ErrValidation, "%s / %s", tc.account, tc.container)
	}
}

func TestOpenGCSBucket_RejectsForeignTokenEndpoint(t *testing.T) {
	t.Parallel()
	_, err := openGCSBucket(context.Background(), "bucket", map[string]any{
		"gcpServiceAccountKey": `{"type":"service_account","token_uri":"http://169.254.169.254/latest"}`,
	})
	assert.ErrorIs(t, err, shared.ErrValidation)
}

// An object over the caller's limit is refused from its size, before any of
// it is read into memory.
func TestFetch_OverLimit_RefusedBeforeReading(t *testing.T) {
	t.Parallel()
	c := &gocloudStorageClient{bucket: memblob.OpenBucket(nil)}
	defer func() { _ = c.Close() }()
	ctx := context.Background()
	require.NoError(t, c.bucket.WriteAll(ctx, "big", make([]byte, 2<<20), nil))

	_, _, err := c.Fetch(ctx, "b", "big", 1<<20)
	assert.ErrorIs(t, err, shared.ErrTooLarge)
	assert.ErrorIs(t, err, shared.ErrValidation)
}
