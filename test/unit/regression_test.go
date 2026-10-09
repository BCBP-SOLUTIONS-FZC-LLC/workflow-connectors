// Regression guards: one test per production defect fixed in this module,
// driven through the public API only, so a refactor inside a package cannot
// silently reintroduce it. IDs are stable; reference them in fixes.
package unit_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

func restCall(t *testing.T, handler http.HandlerFunc, method string, ctx context.Context) (map[string]any, error) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	all, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		Aliases:       aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{Alias: "svc", Method: method, BaseURL: srv.URL, PathTemplate: "/x"}}},
	})
	require.NoError(t, err)
	return all[registry.TypeRestCall].Execute(ctx, map[string]any{"endpointAlias": "svc"})
}

// REG-01: a custom email client's error carrying ErrUpstream made the send
// look retryable — a resend could duplicate the email.
func TestREG01_EmailErrorCarryingErrUpstream_NeverRetried(t *testing.T) {
	t.Parallel()
	email := &scriptedEmail{steps: []emailStep{{err: fmt.Errorf("%w: custom client failure", connectors.ErrUpstream)}}}
	w := newWorld(t, email)

	_, err := w.byType[registry.TypeSendEmail].Execute(tenantCtx(), emailInput())
	require.Error(t, err)
	assert.False(t, errors.Is(err, connectors.ErrUpstream))
	assert.False(t, connectors.AutoRetryAllowed(registry.TypeSendEmail, err, ""))
	assert.ErrorIs(t, err, connectors.ErrDeliveryUnknown, "an unclassified send failure is treated as possibly delivered")
}

// REG-02: Go re-sent x-internal-token and x-departments to a redirect
// target; redirects are now refused, and a 3xx is a permanent failure.
func TestREG02_RestCallRedirect_NotFollowed_Permanent(t *testing.T) {
	t.Parallel()
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	t.Cleanup(target.Close)

	ctx := connectors.WithDepartments(context.Background(), []string{"dept:reviewer"})
	_, err := restCall(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}, http.MethodGet, ctx)

	require.Error(t, err)
	assert.Zero(t, leaked.Load(), "the internal token never reaches the redirect target")
	assert.True(t, connectors.IsPermanent(err))
}

// REG-03: "a@x.com, attacker@y.com" added a recipient on Gmail's raw MIME path.
func TestREG03_EmailAddressMustBeSingle(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	_, err := w.byType[registry.TypeSendEmail].Execute(tenantCtx(), emailInput("receiverEmail", "customer@example.com, attacker@evil.example"))
	assert.ErrorIs(t, err, connectors.ErrValidation)
	assert.Zero(t, w.email.deliveredCount())
}

// REG-04: non-UTF-8 bytes returned inline were mangled in JSON transit.
func TestREG04_InlineBinaryContent_IsBase64(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	png := []byte{0x89, 'P', 'N', 'G', 0xff, 0x00}
	require.NoError(t, w.bucket.Upload(context.Background(), "b", "logo.png", png, "image/png"))

	out, err := w.byType[registry.TypeStorage].Execute(tenantCtx(), map[string]any{
		"operation": "fetch", "provider": "aws-s3", "bucket": "b", "key": "logo.png",
	})
	require.NoError(t, err)
	assert.Equal(t, "base64", out["contentEncoding"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(png), out["content"])
}

// REG-05: a cached Gmail client bound to one sender was reused for another
// sender with the same credentials.
func TestREG05_EmailClientNotSharedAcrossSenders(t *testing.T) {
	t.Parallel()
	var built atomic.Int32
	all, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		SendEmailProviders: map[string]sendemail.ProviderConstructor{
			"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) {
				built.Add(1)
				return sendemail.NewMockSendEmailClient(), nil
			},
		},
	})
	require.NoError(t, err)
	for _, sender := range []string{"billing@example.com", "support@example.com", "billing@example.com"} {
		_, err := all[registry.TypeSendEmail].Execute(tenantCtx(), emailInput("senderEmail", sender, "apiKey", "same-key"))
		require.NoError(t, err)
	}
	assert.Equal(t, int32(2), built.Load(), "one client per sender, reused for the same sender")
}

// REG-06: document refs were worker-local and not tenant-scoped; another
// tenant's ref must never resolve.
func TestREG06_DocumentRefFromAnotherTenant_DoesNotResolve(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	ref, err := w.docRefs.Create(context.Background(), "tenant-other", "application/pdf", []byte("secret"))
	require.NoError(t, err)

	_, err = w.byType[registry.TypeSendEmail].Execute(tenantCtx(), emailInput("attachments", []any{ref.ID}))
	assert.ErrorIs(t, err, docref.ErrNotFound)
	assert.True(t, connectors.IsPermanent(err))
	assert.Zero(t, w.email.deliveredCount())
}

// REG-07: an omitted provider fell back to an in-memory mock that "succeeded"
// and lost the data.
func TestREG07_OmittedProvider_IsValidationNotMock(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	_, err := w.byType[registry.TypeStorage].Execute(tenantCtx(), map[string]any{
		"operation": "upload", "bucket": "b", "key": "k", "content": "x",
	})
	assert.ErrorIs(t, err, connectors.ErrValidation)
}

// REG-08: rest-call without caller identity must fail before any request.
func TestREG08_RestCallWithoutDepartments_NoRequest(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	_, err := restCall(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) }, http.MethodGet, context.Background())
	assert.ErrorIs(t, err, connectors.ErrMissingInternalAuth)
	assert.True(t, connectors.IsPermanent(err))
	assert.Zero(t, hits.Load())
}

// REG-09: every ErrUpstream used to be retryable, so a 404 or an access
// denial looped; only transient errors are retried now.
func TestREG09_UpstreamPermanentFailure_DoesNotLoop(t *testing.T) {
	t.Parallel()
	ctx := connectors.WithDepartments(context.Background(), []string{"dept:reviewer"})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	all, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		Aliases:       aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{Alias: "svc", Method: http.MethodGet, BaseURL: srv.URL, PathTemplate: "/x"}}},
	})
	require.NoError(t, err)

	attempts := runTask(ctx, all[registry.TypeRestCall], http.MethodGet, map[string]any{"endpointAlias": "svc"}, maxAttempts)
	assert.Len(t, attempts, 1)
	assert.Equal(t, int32(1), hits.Load())
	assert.True(t, errors.Is(last(attempts).err, connectors.ErrUpstream))
}

// REG-10: a storage write whose ref metadata failed left an orphaned object.
func TestREG10_FailedRefMetadataWrite_LeavesNoObject(t *testing.T) {
	t.Parallel()
	refs, content := docref.NewMemoryStore(), docref.NewMemoryContent("docs")
	refs.FailPut(errors.New("valkey down"))
	bucket := storage.NewMockStorageClient()
	require.NoError(t, bucket.Upload(context.Background(), "b", "k", []byte("x"), "text/plain"))
	all, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		DocRefs:       docref.NewService(refs, content),
		StorageProviders: map[string]storage.ProviderConstructor{
			"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) { return bucket, nil },
		},
	})
	require.NoError(t, err)

	_, err = all[registry.TypeStorage].Execute(tenantCtx(), map[string]any{
		"operation": "fetch", "provider": "aws-s3", "bucket": "b", "key": "k", "createDocument": true,
	})
	require.Error(t, err)
	assert.Zero(t, content.Len())
}
