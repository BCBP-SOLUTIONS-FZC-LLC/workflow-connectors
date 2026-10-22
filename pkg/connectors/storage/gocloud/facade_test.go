package gocloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

// REG-15: gocloud reports a missing bucket as "not found", and storage
// delete treated every "not found" as already deleted: a delete against a
// bucket that does not exist (a typo, a wrong region) reported success while
// nothing was deleted. Through connectors.New with NewProvider, against a stub
// S3 endpoint. (It belongs to test/unit's regression suite but needs the
// adapter's replaceable S3 constructor.) Not parallel: it swaps that
// package-level constructor.
func TestREG15_StorageDeleteOnMissingBucket_IsAnError(t *testing.T) {
	stub := &s3Stub{}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	orig := newS3Client
	t.Cleanup(func() { newS3Client = orig })
	newS3Client = func(cfg aws.Config) *s3.Client {
		return s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(srv.URL)
			o.UsePathStyle = true
		})
	}
	all, err := connectors.New(connectors.Config{
		InternalToken:    "test-token",
		StorageProviders: map[string]storage.ProviderConstructor{"aws-s3": NewProvider},
	})
	require.NoError(t, err)
	conn := all[registry.TypeStorage]
	input := map[string]any{
		"operation": "delete", "provider": "aws-s3", "bucket": "invoices-typo", "key": "a.pdf",
		"accessKey": "AKIDEXAMPLE", "secretKey": "secret", "region": "us-east-1",
	}
	ctx := connectors.WithTenant(context.Background(), "t")

	stub.headObject, stub.headBucket = http.StatusNotFound, http.StatusNotFound
	_, err = conn.Execute(ctx, input)
	require.ErrorIs(t, err, connectors.ErrUpstream, "a missing bucket is never 'already deleted'")
	assert.Contains(t, err.Error(), "aws-s3 bucket check (HeadBucket)")
	assert.True(t, connectors.IsPermanent(err))
	assert.False(t, connectors.DecideRetry(registry.TypeStorage, err, "").Retry)

	// The object alone missing, in a bucket that exists, is still success.
	stub.headObject, stub.headBucket = http.StatusNotFound, http.StatusOK
	_, err = conn.Execute(ctx, input)
	assert.NoError(t, err)
}
