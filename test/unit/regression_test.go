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
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail/sendgrid"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage/gocloud"
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

	ctx := connectors.WithActor(connectors.WithTenant(context.Background(), "tenant-1"), "principal-sub")
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
	ctx := connectors.WithActor(connectors.WithTenant(context.Background(), "tenant-1"), "principal-sub")
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

// ---- Fixes on the production-readiness branch -----------------------------
//
// REG-13 (google-drive delete and a same-name folder) and REG-15 (storage
// delete on a missing bucket) need the adapters' unexported fakes and live
// next to them, still driven through connectors.New:
// pkg/connectors/storage/googledrive/facade_test.go and
// pkg/connectors/storage/gocloud/facade_test.go.

// REG-11: the default internal transport used http.ProxyFromEnvironment, so
// x-internal-token and x-departments went through any HTTP(S)_PROXY egress
// proxy. Not parallel: it sets the environment. ProxyFromEnvironment reads
// the environment once per process, so this is the first sequential test of
// the package (no earlier test can have read it).
func TestREG11_InternalCall_IgnoresHTTPSProxy(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxied.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(proxy.Close)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	// A host a proxy would carry (loopback is never proxied) that resolves
	// nowhere: sent directly, the call fails; sent through the proxy, the
	// proxy would answer it.
	all, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		Aliases: aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{
			Alias: "svc", Method: http.MethodGet, BaseURL: "http://svc.invalid", PathTemplate: "/x", Timeout: 5 * time.Second,
		}}},
	})
	require.NoError(t, err)
	ctx := connectors.WithActor(connectors.WithTenant(context.Background(), "tenant-1"), "principal-sub")
	_, err = all[registry.TypeRestCall].Execute(ctx, map[string]any{"endpointAlias": "svc"})

	require.Error(t, err, "the call went directly to its (unresolvable) host")
	assert.Zero(t, proxied.Load(), "the internal token never reaches the proxy")
}

type wrappingTransport struct{ next http.RoundTripper }

func (w wrappingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return w.next.RoundTrip(r)
}

// REG-12: with http.DefaultTransport wrapped (otelhttp, httpmock), New and
// every provider client built inside Execute panicked on an unchecked
// *http.Transport assertion. Not parallel: it swaps a process global.
func TestREG12_WrappedDefaultTransport_NewAndExecuteDoNotPanic(t *testing.T) {
	saved := http.DefaultTransport
	http.DefaultTransport = wrappingTransport{next: saved}
	t.Cleanup(func() { http.DefaultTransport = saved })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }))
	t.Cleanup(srv.Close)
	var all map[string]connectors.Connector
	require.NotPanics(t, func() {
		var err error
		all, err = connectors.New(connectors.Config{
			InternalToken:      "test-token",
			Aliases:            aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{Alias: "svc", Method: http.MethodGet, BaseURL: srv.URL, PathTemplate: "/x"}}},
			StorageProviders:   map[string]storage.ProviderConstructor{"aws-s3": gocloud.NewProvider},
			SendEmailProviders: map[string]sendemail.ProviderConstructor{"sendgrid": sendgrid.NewProvider},
		})
		require.NoError(t, err)
	})

	ctx := connectors.WithActor(tenantCtx(), "principal-sub")
	out, err := all[registry.TypeRestCall].Execute(ctx, map[string]any{"endpointAlias": "svc"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, out["status"])

	// The provider clients are built, then the call stops on its cancelled
	// context before any request leaves the process.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	assert.NotPanics(t, func() {
		out, err := all[registry.TypeSendEmail].Execute(cancelled, emailInput("apiKey", "SG.test"))
		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, "not_delivered", out["deliveryOutcome"])
	})
	assert.NotPanics(t, func() {
		_, err := all[registry.TypeStorage].Execute(cancelled, map[string]any{
			"operation": "fetch", "provider": "aws-s3", "bucket": "b", "key": "k",
			"accessKey": "AKIDEXAMPLE", "secretKey": "secret", "region": "us-east-1",
		})
		require.Error(t, err)
	})
}

// REG-14: attachments were resolved before the messageKey was reserved, so a
// redelivered job for an email already accepted failed (permanently) once its
// attachment refs had expired, instead of succeeding as the duplicate it is.
func TestREG14_DuplicateOfAcceptedEmail_SucceedsAfterAttachmentRefExpired(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	ref, err := w.docRefs.Create(context.Background(), "tenant-1", "application/pdf", []byte("%PDF invoice"))
	require.NoError(t, err)
	input := emailInput("messageKey", "invoice-42", "attachments", []any{ref.ID})
	conn := w.byType[registry.TypeSendEmail]

	first, err := conn.Execute(tenantCtx(), input)
	require.NoError(t, err)
	require.NoError(t, w.docRefs.Delete(context.Background(), "tenant-1", ref.ID), "the ref expires")

	again, err := conn.Execute(tenantCtx(), input)
	require.NoError(t, err)
	assert.Equal(t, true, again["duplicate"])
	assert.Equal(t, false, again["sent"])
	assert.Equal(t, "accepted", again["deliveryOutcome"])
	assert.Equal(t, first["messageId"], again["messageId"], "the accepted attempt's message ID")
	assert.NotNil(t, again["sentAt"])
	assert.Equal(t, 1, w.email.deliveredCount())
}

// REG-16: a rest-call 2xx response over 10 MiB was reported as invalid input
// with no output, so a POST the service had processed looked like bad input
// and the worker lost its status and method.
func TestREG16_RestCallOversized2xx_KeepsStatusAndMethod(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	p := restCallTo(t, func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("X-Request-Id", "r-1")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(make([]byte, shared.MaxResponseBytes+1))
	}, aliasconfig.Endpoint{Method: http.MethodPost, PathTemplate: "/orders"}, map[string]any{})

	require.Error(t, p.err)
	assert.NotErrorIs(t, p.err, connectors.ErrValidation)
	require.NotNil(t, p.out)
	assert.Equal(t, http.StatusCreated, p.out["status"])
	assert.Equal(t, http.MethodPost, p.out["method"])
	assert.Equal(t, true, p.out["bodyTruncated"])
	assert.Equal(t, "r-1", p.out["headers"].(map[string]any)["X-Request-Id"])
	assert.False(t, connectors.AutoRetryAllowed(registry.TypeRestCall, p.err, p.out["method"].(string)))
	assert.Equal(t, int32(1), hits.Load())
}

// REG-17: the strict alias decoder refused a file ending in an empty YAML
// document (a trailing "---"), so the worker could not start.
func TestREG17_AliasFileWithTrailingDocumentMarker_Loads(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "aliases.yaml")
	yaml := fmt.Sprintf("version: 1\nrestCall:\n  - alias: svc\n    method: GET\n    baseURL: %s\n    pathTemplate: /x\n---\n# generated\n", srv.URL)
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))

	cfg, err := aliasconfig.Load(path)
	require.NoError(t, err)
	all, err := connectors.New(connectors.Config{InternalToken: "test-token", Aliases: cfg})
	require.NoError(t, err)
	out, err := all[registry.TypeRestCall].Execute(connectors.WithActor(connectors.WithTenant(context.Background(), "tenant-1"), "principal-sub"), map[string]any{"endpointAlias": "svc"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, out["status"])
}

// REG-18: a ";" in an alias's fixed query was dropped by Go's query parser,
// so a fixed key after it could be supplied by the caller and sent twice;
// New now refuses the alias at startup.
func TestREG18_SemicolonInAliasQuery_RefusedByNew(t *testing.T) {
	t.Parallel()
	_, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		Aliases: aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{
			Alias: "svc", Method: http.MethodGet, BaseURL: "http://svc.internal", PathTemplate: "/x?a=1;scope=all",
		}}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), ";")
}

// REG-19: storage and send-email "provider" were optional in the registry
// though nothing defaults it, and registry.All() listed chat-notify, which no
// authored step could ever run. chat-notify still has a policy (Lookup).
func TestREG19_RegistryProviderRequired_AndOnlyThreeAuthorableTypes(t *testing.T) {
	t.Parallel()
	all := registry.All()
	assert.Len(t, all, 3)
	assert.NotContains(t, all, registry.TypeChatNotify)
	for _, typ := range []string{registry.TypeStorage, registry.TypeSendEmail} {
		var provider *registry.Field
		for i, f := range all[typ].Inputs {
			if f.Name == "provider" {
				provider = &all[typ].Inputs[i]
			}
		}
		require.NotNil(t, provider, typ)
		assert.True(t, provider.Required, "%s provider is required", typ)
	}
	built, err := connectors.New(connectors.Config{InternalToken: "test-token"})
	require.NoError(t, err)
	assert.Contains(t, built, registry.TypeChatNotify, "New still builds chat-notify")
	def, ok := registry.Lookup(registry.TypeChatNotify)
	require.True(t, ok)
	assert.Equal(t, registry.RetryPolicyUnsafe, def.Retry)
}

// noisyIDEmail returns a provider message ID holding NUL and invalid UTF-8.
type noisyIDEmail struct{}

func (noisyIDEmail) Send(context.Context, sendemail.EmailMessage) (string, error) {
	return "id-\x00-\xff-1", nil
}

// REG-20: the send-email output messageId was returned raw while the intent
// recorded a cleaned one, so a worker storing outputs as jsonb failed on NUL
// and a duplicate returned a different ID than the original send.
func TestREG20_EmailOutputMessageID_IsStorable(t *testing.T) {
	t.Parallel()
	intents := sendintent.NewMemoryStore()
	all, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		SendEmailProviders: map[string]sendemail.ProviderConstructor{
			"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) { return noisyIDEmail{}, nil },
		},
		SendIntents: intents,
	})
	require.NoError(t, err)
	conn := all[registry.TypeSendEmail]

	out, err := conn.Execute(tenantCtx(), emailInput("messageKey", "k"))
	require.NoError(t, err)
	id, _ := out["messageId"].(string)
	assert.True(t, utf8.ValidString(id))
	assert.NotContains(t, id, "\x00")
	dup, err := conn.Execute(tenantCtx(), emailInput("messageKey", "k"))
	require.NoError(t, err)
	assert.Equal(t, id, dup["messageId"], "the duplicate returns the same ID as the send")
}
