package gocloud

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

// The retry decision for the gocloud adapter's errors, end to end: the
// adapter's own client (over a failing blob driver, with the provider's
// bucket check) behind connectors.New's storage connector, then
// connectors.DecideRetry as the worker calls it. The adapter's classification
// is covered in classify_test.go and delete_test.go; this pins what the
// worker does with it.
func TestRetryDecision_GocloudErrorsThroughTheFacade(t *testing.T) {
	t.Parallel()
	bucketOK := func(context.Context) error { return nil }
	s3BucketGone := func(context.Context) error { return apiErr{"NotFound"} }
	gcsBucketGone := func(context.Context) error {
		return &googleapi.Error{Code: http.StatusNotFound, Message: "The specified bucket does not exist."}
	}
	nxdomain := &net.DNSError{Err: "no such host", Name: "nosuchacct.blob.core.windows.net", IsNotFound: true}

	type row struct {
		provider, operation string
		driver              *fakeDriver
		objectMissing       func(context.Context, error) (bool, error)
		class               connectors.ErrorClass
		reason              string
		retry               bool
		errContains         string
	}
	for name, r := range map[string]row{
		// Azure and GCS statuses the shared classifier cannot read.
		"azure 408": {provider: "azure-blob", operation: "upload", driver: &fakeDriver{openWriterErr: &azcore.ResponseError{StatusCode: 408}},
			class: connectors.ClassTransient, reason: "azure http 408", retry: true},
		"azure 429": {provider: "azure-blob", operation: "upload", driver: &fakeDriver{openWriterErr: &azcore.ResponseError{StatusCode: 429}},
			class: connectors.ClassTransient, reason: "azure http 429", retry: true},
		"azure 503": {provider: "azure-blob", operation: "upload", driver: &fakeDriver{openWriterErr: &azcore.ResponseError{StatusCode: 503, ErrorCode: "ServerBusy"}},
			class: connectors.ClassTransient, reason: "azure http 503", retry: true},
		"gcs 408": {provider: "gcp-gcs", operation: "upload", driver: &fakeDriver{commitErr: &googleapi.Error{Code: 408}},
			class: connectors.ClassTransient, reason: "gcs http 408", retry: true},
		"gcs 429": {provider: "gcp-gcs", operation: "upload", driver: &fakeDriver{commitErr: &googleapi.Error{Code: 429}},
			class: connectors.ClassTransient, reason: "gcs http 429", retry: true},
		"gcs 500": {provider: "gcp-gcs", operation: "upload", driver: &fakeDriver{commitErr: &googleapi.Error{Code: 500}},
			class: connectors.ClassTransient, reason: "gcs http 500", retry: true},
		// An unresolvable Azure account host: usually a misspelt account, but
		// a private-endpoint DNS outage looks the same. Unknown: not retried.
		"azure NXDOMAIN": {provider: "azure-blob", operation: "upload", driver: &fakeDriver{openWriterErr: fmt.Errorf("dial: %w", nxdomain)},
			class: connectors.ClassUnknown, reason: "azure account host not found (NXDOMAIN)"},
		// A delete whose bucket is missing is a failure, never "already deleted".
		"s3 delete, bucket missing": {provider: "aws-s3", operation: "delete", driver: &fakeDriver{deleteErr: apiErr{"NotFound"}},
			objectMissing: s3ObjectMissing(s3BucketGone), class: connectors.ClassPermanent, errContains: "aws-s3 bucket check (HeadBucket)"},
		"s3 delete, 403": {provider: "aws-s3", operation: "delete", driver: &fakeDriver{deleteErr: statusErr{http.StatusForbidden}},
			objectMissing: s3ObjectMissing(bucketOK), class: connectors.ClassPermanent, reason: "http 403", errContains: "s3:ListBucket"},
		"gcs delete, bucket missing": {provider: "gcp-gcs", operation: "delete", driver: &fakeDriver{deleteErr: &googleapi.Error{Code: http.StatusNotFound}},
			objectMissing: gcsObjectMissing(gcsBucketGone), class: connectors.ClassPermanent, reason: "gcs http 404", errContains: "gcp-gcs bucket check (objects.list)"},
		"azure delete, container missing": {provider: "azure-blob", operation: "delete",
			driver:        &fakeDriver{deleteErr: &azcore.ResponseError{StatusCode: http.StatusNotFound, ErrorCode: "ContainerNotFound"}},
			objectMissing: azureObjectMissing, class: connectors.ClassPermanent, reason: "azure http 404"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := fakeClient(t, r.driver)
			client.objectMissing = r.objectMissing
			all, err := connectors.New(connectors.Config{
				InternalToken: "test-token",
				StorageProviders: map[string]storage.ProviderConstructor{
					r.provider: func(context.Context, map[string]any) (storage.ProviderClient, error) { return client, nil },
				},
			})
			require.NoError(t, err)

			_, err = all[registry.TypeStorage].Execute(connectors.WithTenant(context.Background(), "t"), map[string]any{
				"operation": r.operation, "provider": r.provider, "bucket": "bucket-01", "key": "k", "content": "x",
			})
			require.ErrorIs(t, err, connectors.ErrUpstream)
			assert.NotErrorIs(t, err, connectors.ErrValidation)
			if r.errContains != "" {
				assert.Contains(t, err.Error(), r.errContains)
			}
			class, reason := connectors.ClassOf(err)
			assert.Equal(t, r.class, class, "reason %q: %v", reason, err)
			if r.reason != "" {
				assert.Equal(t, r.reason, reason)
			}
			d := connectors.DecideRetry(registry.TypeStorage, err, "")
			assert.Equal(t, r.retry, d.Retry, "%+v", d)
			assert.Equal(t, r.class, d.Class)
		})
	}
}

// The delete check's own transient failure (the bucket check got a 503)
// keeps its class through the facade: retried, never reported as deleted.
func TestRetryDecision_DeleteBucketCheckUnavailable_IsRetried(t *testing.T) {
	t.Parallel()
	client := fakeClient(t, &fakeDriver{deleteErr: &googleapi.Error{Code: http.StatusNotFound}})
	client.objectMissing = gcsObjectMissing(func(context.Context) error { return &googleapi.Error{Code: http.StatusServiceUnavailable} })
	all, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		StorageProviders: map[string]storage.ProviderConstructor{
			"gcp-gcs": func(context.Context, map[string]any) (storage.ProviderClient, error) { return client, nil },
		},
	})
	require.NoError(t, err)
	_, err = all[registry.TypeStorage].Execute(connectors.WithTenant(context.Background(), "t"), map[string]any{
		"operation": "delete", "provider": "gcp-gcs", "bucket": "b", "key": "k",
	})
	require.Error(t, err)
	var gErr *googleapi.Error
	require.True(t, errors.As(err, &gErr))
	assert.Equal(t, http.StatusServiceUnavailable, gErr.Code)
	assert.True(t, connectors.DecideRetry(registry.TypeStorage, err, "").Retry)
}
