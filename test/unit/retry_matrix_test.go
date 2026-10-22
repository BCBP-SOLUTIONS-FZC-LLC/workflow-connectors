// The retry decision matrix: every connector type × every kind of error ×
// the HTTP methods that matter, against the rules documented in
// docs/runbooks/retry-semantics.md. A new connector type or retry policy
// fails TestRetryMatrix_CoversEveryRegistryType (authorable types) and
// TestRetryMatrix_CoversEveryBuiltType (every type New builds, chat-notify
// included) until it is added here.
package unit_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/chatnotify"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sqlquery"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

// errorKinds are one representative error per class and delivery outcome.
var errorKinds = map[string]error{
	"transient":                fmt.Errorf("%w: %w", connectors.ErrUpstream, shared.HTTPStatusError(http.StatusServiceUnavailable, errors.New("503"))),
	"permanent":                fmt.Errorf("%w: %w", connectors.ErrUpstream, shared.HTTPStatusError(http.StatusNotFound, errors.New("404"))),
	"unknown":                  fmt.Errorf("%w: provider said something new", connectors.ErrUpstream),
	"validation":               fmt.Errorf("%w: bucket is required", connectors.ErrValidation),
	"not delivered, transient": sendemail.ClassifyStatus("sendgrid", http.StatusTooManyRequests, errors.New("throttled")),
	"not delivered, permanent": sendemail.ClassifyStatus("sendgrid", http.StatusBadRequest, errors.New("invalid to")),
	"delivery unknown":         sendemail.ClassifyTransport("sendgrid", fmt.Errorf("read: %w", context.DeadlineExceeded)),
}

type row struct {
	connectorType, kind, method string
	retry                       bool
	rule                        string
}

var matrix = []row{
	// storage: safe — every transient error.
	{registry.TypeStorage, "transient", "", true, "transient"},
	{registry.TypeStorage, "permanent", "", false, "permanent"},
	{registry.TypeStorage, "unknown", "", false, "unknown"},
	{registry.TypeStorage, "validation", "", false, "permanent"},

	// send-email: not-delivered — only a transient failure nothing received.
	{registry.TypeSendEmail, "not delivered, transient", "", true, "transient, not delivered"},
	{registry.TypeSendEmail, "not delivered, permanent", "", false, "permanent"},
	{registry.TypeSendEmail, "delivery unknown", "", false, "unknown"},
	{registry.TypeSendEmail, "transient", "", false, "may have been delivered"},
	{registry.TypeSendEmail, "validation", "", false, "permanent"},

	// rest-call: conditional — transient and an idempotent method.
	{registry.TypeRestCall, "transient", http.MethodGet, true, "transient"},
	{registry.TypeRestCall, "transient", http.MethodPut, true, "transient"},
	{registry.TypeRestCall, "transient", http.MethodDelete, true, "transient"},
	{registry.TypeRestCall, "transient", http.MethodPost, false, "non-idempotent method"},
	{registry.TypeRestCall, "transient", http.MethodPatch, false, "non-idempotent method"},
	{registry.TypeRestCall, "permanent", http.MethodGet, false, "permanent"},
	{registry.TypeRestCall, "unknown", http.MethodGet, false, "unknown"},

	// chat-notify: unsafe — never.
	{registry.TypeChatNotify, "transient", "", false, "policy unsafe"},
	{registry.TypeChatNotify, "permanent", "", false, "permanent"},

	// An unregistered type is never retried.
	{"no-such-type", "transient", "", false, "unknown connector type"},
}

func TestRetryMatrix(t *testing.T) {
	t.Parallel()
	for _, r := range matrix {
		err, ok := errorKinds[r.kind]
		require.Truef(t, ok, "unknown error kind %q", r.kind)
		d := connectors.DecideRetry(r.connectorType, err, r.method)
		name := fmt.Sprintf("%s/%s/%s", r.connectorType, r.kind, r.method)
		assert.Equal(t, r.retry, d.Retry, name)
		assert.Equal(t, r.rule, d.Rule, name)
		assert.Equal(t, d.Retry, connectors.AutoRetryAllowed(r.connectorType, err, r.method), name)
	}
}

// Permanent and unknown errors are never retried, by any connector type,
// with any method: nothing can loop on them.
func TestRetryMatrix_PermanentAndUnknownNeverRetry(t *testing.T) {
	t.Parallel()
	for typ := range registry.All() {
		for _, kind := range []string{"permanent", "unknown", "validation", "not delivered, permanent", "delivery unknown"} {
			for _, method := range []string{"", http.MethodGet, http.MethodPost} {
				d := connectors.DecideRetry(typ, errorKinds[kind], method)
				assert.Falsef(t, d.Retry, "%s/%s/%s", typ, kind, method)
			}
		}
	}
}

func TestRetryMatrix_CoversEveryRegistryType(t *testing.T) {
	t.Parallel()
	covered := map[string]bool{}
	for _, r := range matrix {
		covered[r.connectorType] = true
	}
	known := map[registry.RetryPolicy]bool{
		registry.RetryPolicySafe: true, registry.RetryPolicyUnsafe: true,
		registry.RetryPolicyConditional: true, registry.RetryPolicyNotDelivered: true,
	}
	for typ, def := range registry.All() {
		assert.Truef(t, covered[typ], "connector type %q has no rows in the retry matrix", typ)
		assert.Truef(t, known[def.Retry], "connector type %q has unknown retry policy %q", typ, def.Retry)
	}
}

func TestRetryMatrix_ClassOfEachKind(t *testing.T) {
	t.Parallel()
	for kind, want := range map[string]connectors.ErrorClass{
		"transient":                connectors.ClassTransient,
		"permanent":                connectors.ClassPermanent,
		"unknown":                  connectors.ClassUnknown,
		"validation":               connectors.ClassPermanent,
		"not delivered, transient": connectors.ClassTransient,
		"not delivered, permanent": connectors.ClassPermanent,
		"delivery unknown":         connectors.ClassUnknown,
	} {
		class, _ := connectors.ClassOf(errorKinds[kind])
		assert.Equal(t, want, class, kind)
		assert.Equal(t, want == connectors.ClassTransient, connectors.IsTransient(errorKinds[kind]), kind)
		assert.Equal(t, want == connectors.ClassPermanent, connectors.IsPermanent(errorKinds[kind]), kind)
	}
}

// Every type connectors.New builds — chat-notify included, although
// registry.All() does not list it — has a retry policy and rows in the
// matrix, so DecideRetry never answers "unknown connector type" for one.
func TestRetryMatrix_CoversEveryBuiltType(t *testing.T) {
	t.Parallel()
	covered := map[string]bool{}
	for _, r := range matrix {
		covered[r.connectorType] = true
	}
	built, err := connectors.New(connectors.Config{InternalToken: "t"})
	require.NoError(t, err)
	for typ := range built {
		def, ok := registry.Lookup(typ)
		require.Truef(t, ok, "built connector type %q has no definition", typ)
		assert.Truef(t, slices.Contains([]registry.RetryPolicy{
			registry.RetryPolicySafe, registry.RetryPolicyUnsafe,
			registry.RetryPolicyConditional, registry.RetryPolicyNotDelivered,
		}, def.Retry), "built connector type %q has unknown retry policy %q", typ, def.Retry)
		assert.Truef(t, covered[typ], "built connector type %q has no rows in the retry matrix", typ)
		assert.NotEqual(t, "unknown connector type", connectors.DecideRetry(typ, errorKinds["transient"], "").Rule, typ)
	}
}

// ---- Errors produced by the library, end to end ---------------------------
//
// The rows above feed DecideRetry representative errors. The rows below make
// each error the way production does — a real Execute through connectors.New
// (or the connector's own New where New does not build it) — and check its
// class, reason and the worker's decision. Adapter errors that need a cloud
// SDK or a Valkey node are pinned the same way next to their fakes:
// pkg/connectors/storage/gocloud/retrydecision_test.go (Azure / GCS statuses,
// Azure NXDOMAIN, missing bucket on delete, S3 403) and
// test/unit/valkeystore/retry_decision_test.go (BUSY, maxclients, pool
// timeout, closed cluster client, refused ASKING).

// produced is one error a real Execute returned, and its output.
type produced struct {
	out map[string]any
	err error
}

type producedRow struct {
	name, connectorType string
	// method is passed to DecideRetry; methodFromOutput uses Execute's
	// output "method" instead, as the worker does for rest-call.
	method           string
	methodFromOutput bool
	produce          func(t *testing.T) produced
	class            connectors.ErrorClass
	reason           string // exact ClassOf reason; "" is not checked
	retry            bool
	rule             string
	is, isNot        []error
}

// unavailableRefs is a document-ref store whose reads fail as the Valkey
// store reports a capacity or topology refusal.
type unavailableRefs struct{ *docref.MemoryStore }

func (unavailableRefs) Get(context.Context, string, string) (docref.Ref, bool, error) {
	return docref.Ref{}, false, fmt.Errorf("%w: get: BUSY Valkey is busy running a script", docref.ErrUnavailable)
}

// reserveFails fails Reserve with err without reserving anything; getErr,
// when set, fails the read-back too.
type reserveFails struct {
	*sendintent.MemoryStore
	err, getErr error
	reserveLate bool // reserve in the store anyway, as a commit whose reply was lost
}

func (s *reserveFails) Reserve(ctx context.Context, tenant, key string, resendFrom int, token string) (sendintent.Intent, bool, error) {
	if s.reserveLate {
		if _, _, err := s.MemoryStore.Reserve(ctx, tenant, key, resendFrom, token); err != nil {
			return sendintent.Intent{}, false, err
		}
	}
	return sendintent.Intent{}, false, s.err
}

func (s *reserveFails) Get(ctx context.Context, tenant, key string) (sendintent.Intent, bool, error) {
	if s.getErr != nil {
		return sendintent.Intent{}, false, s.getErr
	}
	return s.MemoryStore.Get(ctx, tenant, key)
}

// emailWith runs one send-email Execute with intents and docRefs (either may
// be nil) and a provider that accepts everything.
func emailWith(t *testing.T, intents sendintent.Store, refs docref.Store, input map[string]any) produced {
	t.Helper()
	cfg := connectors.Config{
		InternalToken: "test-token",
		SendEmailProviders: map[string]sendemail.ProviderConstructor{
			"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) {
				return sendemail.NewMockSendEmailClient(), nil
			},
		},
		SendIntents: intents,
	}
	if refs != nil {
		cfg.DocRefs = docref.NewService(refs, docref.NewMemoryContent("docs"))
	}
	all, err := connectors.New(cfg)
	require.NoError(t, err)
	out, err := all[registry.TypeSendEmail].Execute(tenantCtx(), input)
	return produced{out, err}
}

// storageWith runs one storage Execute against client (as provider
// "google-drive") with refs as the document-ref store (may be nil).
func storageWith(t *testing.T, client storage.ProviderClient, refs docref.Store, input map[string]any) produced {
	t.Helper()
	cfg := connectors.Config{
		InternalToken: "test-token",
		StorageProviders: map[string]storage.ProviderConstructor{
			"google-drive": func(context.Context, map[string]any) (storage.ProviderClient, error) { return client, nil },
		},
	}
	if refs != nil {
		cfg.DocRefs = docref.NewService(refs, docref.NewMemoryContent("docs"))
	}
	all, err := connectors.New(cfg)
	require.NoError(t, err)
	out, err := all[registry.TypeStorage].Execute(tenantCtx(), input)
	return produced{out, err}
}

func storageInput(extra ...any) map[string]any {
	in := map[string]any{"operation": "fetch", "provider": "google-drive", "bucket": "folder-1", "key": "a.pdf"}
	for i := 0; i+1 < len(extra); i += 2 {
		in[extra[i].(string)] = extra[i+1]
	}
	return in
}

// inProgressDrive is a Drive client whose document another call owns.
type inProgressDrive struct{ *storage.MockStorageClient }

func (inProgressDrive) Fetch(context.Context, string, string, int64) ([]byte, string, error) {
	return nil, "", &documents.InProgressError{DocumentID: "doc-1", State: documents.StateUploading}
}

// restCallTo runs one rest-call Execute against an alias for handler.
func restCallTo(t *testing.T, handler http.HandlerFunc, ep aliasconfig.Endpoint, input map[string]any) produced {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	ep.Alias, ep.BaseURL = "svc", srv.URL
	all, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		Aliases:       aliasconfig.Config{RestCall: []aliasconfig.Endpoint{ep}},
	})
	require.NoError(t, err)
	input["endpointAlias"] = "svc"
	out, err := all[registry.TypeRestCall].Execute(connectors.WithActor(connectors.WithTenant(context.Background(), "tenant-1"), "principal-sub"), input)
	return produced{out, err}
}

func oversized(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write(make([]byte, shared.MaxResponseBytes+1))
}

func chatNotifyWith(t *testing.T, client chatnotify.ProviderClient) produced {
	t.Helper()
	all, err := connectors.New(connectors.Config{InternalToken: "test-token", ChatNotifyClient: client})
	require.NoError(t, err)
	out, err := all[registry.TypeChatNotify].Execute(tenantCtx(), map[string]any{
		"method": "post-message", "channelOrUser": "#ops", "message": "hello",
	})
	return produced{out, err}
}

// throttledChat fails every call with a 503: transient, yet never retried.
type throttledChat struct{}

func (throttledChat) CreateChannel(context.Context, string, string) (string, error) { return "", nil }
func (throttledChat) InviteToChannel(context.Context, string, []string) error       { return nil }
func (throttledChat) PostMessage(context.Context, string, string, string) (string, error) {
	return "", shared.HTTPStatusError(http.StatusServiceUnavailable, errors.New("chat provider 503"))
}

var errLostReply = errors.New("connection reset by peer")

var producedRows = []producedRow{
	// rest-call: a 2xx over 10 MiB was processed — permanent "response too
	// large", never invalid input, never retried (even for GET: the body
	// would be as large again), with the output that says what happened.
	{
		name: "rest-call 2xx over 10 MiB, GET", connectorType: registry.TypeRestCall, methodFromOutput: true,
		produce: func(t *testing.T) produced {
			return restCallTo(t, oversized, aliasconfig.Endpoint{Method: http.MethodGet, PathTemplate: "/x"}, map[string]any{})
		},
		class: connectors.ClassPermanent, reason: shared.ReasonResponseTooLarge, rule: "permanent",
		is: []error{connectors.ErrUpstream, shared.ErrTooLarge}, isNot: []error{connectors.ErrValidation},
	},
	{
		name: "rest-call 2xx over 10 MiB, POST", connectorType: registry.TypeRestCall, methodFromOutput: true,
		produce: func(t *testing.T) produced {
			return restCallTo(t, oversized, aliasconfig.Endpoint{Method: http.MethodPost, PathTemplate: "/x"}, map[string]any{})
		},
		class: connectors.ClassPermanent, reason: shared.ReasonResponseTooLarge, rule: "permanent",
		is: []error{connectors.ErrUpstream, shared.ErrTooLarge}, isNot: []error{connectors.ErrValidation},
	},
	{
		name: "rest-call list path parameter", connectorType: registry.TypeRestCall, method: http.MethodGet,
		produce: func(t *testing.T) produced {
			return restCallTo(t, func(http.ResponseWriter, *http.Request) { t.Error("request sent") },
				aliasconfig.Endpoint{Method: http.MethodGet, PathTemplate: "/x/{id}"},
				map[string]any{"pathParams": map[string]any{"id": []any{"a", "b"}}})
		},
		class: connectors.ClassPermanent, reason: "invalid input", rule: "permanent", is: []error{connectors.ErrValidation},
	},
	{
		name: "rest-call map query parameter", connectorType: registry.TypeRestCall, method: http.MethodGet,
		produce: func(t *testing.T) produced {
			return restCallTo(t, func(http.ResponseWriter, *http.Request) { t.Error("request sent") },
				aliasconfig.Endpoint{Method: http.MethodGet, PathTemplate: "/x"},
				map[string]any{"queryParams": map[string]any{"q": map[string]any{"nested": 1}}})
		},
		class: connectors.ClassPermanent, reason: "invalid input", rule: "permanent", is: []error{connectors.ErrValidation},
	},

	// sql-query (not built by New, not in the registry): the same oversized
	// 2xx is permanent "response too large", and the type is never retried.
	{
		name: "sql-query 2xx over 10 MiB", connectorType: registry.TypeSQLQuery,
		produce: func(t *testing.T) produced {
			srv := httptest.NewServer(http.HandlerFunc(oversized))
			t.Cleanup(srv.Close)
			q := sqlquery.New(aliasconfig.Config{SQLQuery: []aliasconfig.Query{{Alias: "q", BaseURL: srv.URL, Path: "/q", QueryID: "q"}}}, nil, "test-token")
			out, err := q.Execute(connectors.WithActor(connectors.WithTenant(context.Background(), "tenant-1"), "principal-sub"), map[string]any{"queryAlias": "q"})
			return produced{out, err}
		},
		class: connectors.ClassPermanent, reason: shared.ReasonResponseTooLarge, rule: "unknown connector type",
		is: []error{connectors.ErrUpstream, shared.ErrTooLarge}, isNot: []error{connectors.ErrValidation},
	},
	{
		name: "sql-query non-list params", connectorType: registry.TypeSQLQuery,
		produce: func(t *testing.T) produced {
			q := sqlquery.New(aliasconfig.Config{SQLQuery: []aliasconfig.Query{{Alias: "q", BaseURL: "http://127.0.0.1:1", Path: "/q", QueryID: "q", ParamCount: 1}}}, nil, "test-token")
			out, err := q.Execute(connectors.WithActor(connectors.WithTenant(context.Background(), "tenant-1"), "principal-sub"), map[string]any{"queryAlias": "q", "params": "1"})
			return produced{out, err}
		},
		class: connectors.ClassPermanent, reason: "invalid input", rule: "unknown connector type", is: []error{connectors.ErrValidation},
	},

	// storage
	{
		name: "storage key over 1024 bytes", connectorType: registry.TypeStorage,
		produce: func(t *testing.T) produced {
			return storageWith(t, storage.NewMockStorageClient(), nil, storageInput("key", strings.Repeat("k", 1025)))
		},
		class: connectors.ClassPermanent, reason: "invalid input", rule: "permanent", is: []error{connectors.ErrValidation},
	},
	{
		name: "storage bucket with a control character", connectorType: registry.TypeStorage,
		produce: func(t *testing.T) produced {
			return storageWith(t, storage.NewMockStorageClient(), nil, storageInput("bucket", "b\nx"))
		},
		class: connectors.ClassPermanent, reason: "invalid input", rule: "permanent", is: []error{connectors.ErrValidation},
	},
	{
		name: "storage document-ref store unavailable", connectorType: registry.TypeStorage,
		produce: func(t *testing.T) produced {
			client := storage.NewMockStorageClient()
			require.NoError(t, client.Upload(context.Background(), "folder-1", "a.pdf", []byte("%PDF"), "application/pdf"))
			refs := docref.NewMemoryStore()
			refs.FailPut(fmt.Errorf("%w: put: ERR max number of clients reached", docref.ErrUnavailable))
			return storageWith(t, client, refs, storageInput("createDocument", true))
		},
		class: connectors.ClassTransient, reason: "document-ref store unavailable", retry: true, rule: "transient",
		is: []error{docref.ErrUnavailable}, isNot: []error{connectors.ErrValidation},
	},
	{
		name: "storage google-drive upload in progress", connectorType: registry.TypeStorage,
		produce: func(t *testing.T) produced {
			return storageWith(t, inProgressDrive{storage.NewMockStorageClient()}, nil, storageInput())
		},
		class: connectors.ClassTransient, reason: "upload in progress", retry: true, rule: "transient",
		is: []error{documents.ErrUploadInProgress, connectors.ErrUpstream},
	},

	// send-email: inputs refused on this branch.
	{
		name: "send-email messageKey over 512 bytes", connectorType: registry.TypeSendEmail,
		produce: func(t *testing.T) produced {
			return emailWith(t, sendintent.NewMemoryStore(), nil, emailInput("messageKey", strings.Repeat("k", 513)))
		},
		class: connectors.ClassPermanent, reason: "invalid input", rule: "permanent", is: []error{connectors.ErrValidation},
	},
	{
		name: "send-email messageKey with a control character", connectorType: registry.TypeSendEmail,
		produce: func(t *testing.T) produced {
			return emailWith(t, sendintent.NewMemoryStore(), nil, emailInput("messageKey", "k\x00"))
		},
		class: connectors.ClassPermanent, reason: "invalid input", rule: "permanent", is: []error{connectors.ErrValidation},
	},
	{
		name: "send-email resendAttempt out of range", connectorType: registry.TypeSendEmail,
		produce: func(t *testing.T) produced {
			return emailWith(t, sendintent.NewMemoryStore(), nil, emailInput("messageKey", "k", "resend", true, "resendAttempt", int64(1)<<31))
		},
		class: connectors.ClassPermanent, reason: "invalid input", rule: "permanent", is: []error{connectors.ErrValidation},
	},
	{
		name: "send-email contentType with parameters", connectorType: registry.TypeSendEmail,
		produce: func(t *testing.T) produced {
			return emailWith(t, nil, nil, emailInput("contentType", "text/html; charset=utf-8"))
		},
		class: connectors.ClassPermanent, reason: "invalid input", rule: "permanent", is: []error{connectors.ErrValidation},
	},
	// send-email: the document-ref store refusing a read, before anything
	// was sent — not delivered and transient, so retried.
	{
		name: "send-email attachment store unavailable", connectorType: registry.TypeSendEmail,
		produce: func(t *testing.T) produced {
			return emailWith(t, nil, unavailableRefs{docref.NewMemoryStore()}, emailInput("attachments", []any{docref.NewID()}))
		},
		class: connectors.ClassTransient, retry: true, rule: "transient, not delivered",
		is: []error{docref.ErrUnavailable, connectors.ErrNotDelivered},
	},
	// send-email: Reserve failures. ErrNotReserved keeps the class.
	{
		name: "send-email Reserve failed, nothing reserved, transient", connectorType: registry.TypeSendEmail,
		produce: func(t *testing.T) produced {
			return emailWith(t, &reserveFails{MemoryStore: sendintent.NewMemoryStore(),
				err: fmt.Errorf("%w: %w", sendintent.ErrNotReserved, shared.Transient("lock timeout", errLostReply))}, nil, emailInput("messageKey", "k"))
		},
		class: connectors.ClassTransient, retry: true, rule: "transient, not delivered",
		is: []error{sendintent.ErrNotReserved, connectors.ErrNotDelivered},
	},
	{
		name: "send-email Reserve failed, nothing reserved, permanent", connectorType: registry.TypeSendEmail,
		produce: func(t *testing.T) produced {
			return emailWith(t, &reserveFails{MemoryStore: sendintent.NewMemoryStore(),
				err: fmt.Errorf("%w: %w", sendintent.ErrNotReserved, shared.Permanent("check violation", errLostReply))}, nil, emailInput("messageKey", "k"))
		},
		class: connectors.ClassPermanent, rule: "permanent",
		is: []error{sendintent.ErrNotReserved, connectors.ErrNotDelivered},
	},
	// Ambiguous: the reply was lost and no reservation of this call could be
	// found or released — it may surface pending later. Unknown, not retried.
	{
		name: "send-email Reserve ambiguous, nothing released", connectorType: registry.TypeSendEmail,
		produce: func(t *testing.T) produced {
			return emailWith(t, &reserveFails{MemoryStore: sendintent.NewMemoryStore(),
				err: shared.Transient("connection reset", errLostReply)}, nil, emailInput("messageKey", "k"))
		},
		class: connectors.ClassUnknown, reason: "send intent not recorded: verify delivery, then resend explicitly", rule: "unknown",
		is: []error{connectors.ErrNotDelivered},
	},
	// Ambiguous, but the reservation had committed and the read-back failed:
	// releasing it by its token settles it, so the error keeps its class.
	{
		name: "send-email Reserve ambiguous, own reservation released", connectorType: registry.TypeSendEmail,
		produce: func(t *testing.T) produced {
			return emailWith(t, &reserveFails{MemoryStore: sendintent.NewMemoryStore(), reserveLate: true,
				err: shared.Transient("connection reset", errLostReply), getErr: errors.New("read-back failed")}, nil, emailInput("messageKey", "k"))
		},
		class: connectors.ClassTransient, retry: true, rule: "transient, not delivered",
		is: []error{connectors.ErrNotDelivered},
	},

	// chat-notify: built by New, absent from registry.All(), but its unsafe
	// policy is read through registry.Lookup — a transient failure is never
	// retried, and no provider is invalid input.
	{
		name: "chat-notify provider 503", connectorType: registry.TypeChatNotify,
		produce: func(t *testing.T) produced { return chatNotifyWith(t, throttledChat{}) },
		class:   connectors.ClassTransient, reason: "http 503", rule: "policy unsafe",
	},
	{
		name: "chat-notify without a provider", connectorType: registry.TypeChatNotify,
		produce: func(t *testing.T) produced { return chatNotifyWith(t, nil) },
		class:   connectors.ClassPermanent, reason: "invalid input", rule: "permanent", is: []error{connectors.ErrValidation},
	},
}

func TestRetryMatrix_ProducedErrors(t *testing.T) {
	t.Parallel()
	for _, r := range producedRows {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			p := r.produce(t)
			require.Error(t, p.err)
			method := r.method
			if r.methodFromOutput {
				require.NotNil(t, p.out, "the output says which method was sent")
				method, _ = p.out["method"].(string)
				require.NotEmpty(t, method)
			}
			class, reason := connectors.ClassOf(p.err)
			assert.Equal(t, r.class, class, "reason %q: %v", reason, p.err)
			if r.reason != "" {
				assert.Equal(t, r.reason, reason)
			}
			d := connectors.DecideRetry(r.connectorType, p.err, method)
			assert.Equal(t, r.retry, d.Retry, "%+v", d)
			assert.Equal(t, r.rule, d.Rule)
			assert.Equal(t, class, d.Class)
			for _, target := range r.is {
				assert.ErrorIs(t, p.err, target)
			}
			for _, target := range r.isNot {
				assert.NotErrorIs(t, p.err, target)
			}
		})
	}
}
