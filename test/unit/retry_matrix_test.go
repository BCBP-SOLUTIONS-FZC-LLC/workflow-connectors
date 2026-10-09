// The retry decision matrix: every connector type × every kind of error ×
// the HTTP methods that matter, against the rules documented in
// docs/runbooks/retry-semantics.md. A new connector type or retry policy
// fails TestRetryMatrix_CoversEveryRegistryType until it is added here.
package unit_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
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
