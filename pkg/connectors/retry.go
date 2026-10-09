package connectors

import (
	"errors"
	"log/slog"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

// RetryDecision is the outcome of DecideRetry: whether a failed Execute may
// be re-run automatically, and why. Log it (LogAttrs) and count it
// (Class.String() and Retry as metric labels) for every failed call.
type RetryDecision struct {
	Retry bool
	// Class is the error's classification: transient, permanent or unknown.
	Class ErrorClass
	// Reason is the classifier's short reason, e.g. "http 503", "api SlowDown",
	// "timeout", "not delivered: dns", "invalid input".
	Reason string
	// Policy is the connector type's registry retry policy.
	Policy registry.RetryPolicy
	// Rule names the rule that decided, e.g. "transient", "permanent",
	// "unknown", "policy unsafe", "non-idempotent method", "may have been
	// delivered", "unknown connector type".
	Rule string
}

// LogAttrs returns the decision as structured log attributes:
// retry, error_class, error_reason, retry_policy, retry_rule.
func (d RetryDecision) LogAttrs() []slog.Attr {
	return []slog.Attr{
		slog.Bool("retry", d.Retry),
		slog.String("error_class", d.Class.String()),
		slog.String("error_reason", d.Reason),
		slog.String("retry_policy", string(d.Policy)),
		slog.String("retry_rule", d.Rule),
	}
}

// DecideRetry is the single, deterministic decision generic retry logic
// (worker retry loops, workflow activity retry policies, queue redelivery)
// must make before re-running a failed Execute without a person deciding to.
// The same error, type and method always give the same decision:
//
//  1. Only a transient error may be retried. A permanent error (invalid
//     input, not found, access denied, integrity violation, 4xx) never is,
//     and neither is an unknown one (unrecognised errors, cancellation, an
//     email whose delivery outcome is unknown).
//  2. The connector type's registry.RetryPolicy must also allow it:
//     safe (storage) → yes; conditional (rest-call) → only when method, the
//     alias's HTTP method, is idempotent; not-delivered (send-email) → only
//     when the provider provably never accepted the message (ErrNotDelivered),
//     so a retry cannot duplicate it; unsafe (chat-notify) → never.
//
// A retried call still needs a bounded number of attempts with backoff —
// the worker's retry policy — since a transient failure can persist.
func DecideRetry(connectorType string, err error, method string) RetryDecision {
	class, reason := shared.ClassOf(err)
	d := RetryDecision{Class: class, Reason: reason}
	def, known := registry.All()[connectorType]
	if !known {
		d.Rule = "unknown connector type"
		return d
	}
	d.Policy = def.Retry
	switch class {
	case ClassPermanent:
		d.Rule = "permanent"
		return d
	case ClassUnknown:
		d.Rule = "unknown"
		return d
	}
	switch def.Retry {
	case registry.RetryPolicySafe:
		d.Retry, d.Rule = true, "transient"
	case registry.RetryPolicyConditional:
		if registry.IsIdempotentMethod(method) {
			d.Retry, d.Rule = true, "transient"
		} else {
			d.Rule = "non-idempotent method"
		}
	case registry.RetryPolicyNotDelivered:
		if errors.Is(err, shared.ErrNotDelivered) {
			d.Retry, d.Rule = true, "transient, not delivered"
		} else {
			d.Rule = "may have been delivered"
		}
	default:
		d.Rule = "policy " + string(def.Retry)
	}
	return d
}

// AutoRetryAllowed reports DecideRetry(connectorType, err, method).Retry.
func AutoRetryAllowed(connectorType string, err error, method string) bool {
	return DecideRetry(connectorType, err, method).Retry
}
