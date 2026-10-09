package sendemail

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http/httptrace"
	"reflect"
	"strings"
	"sync/atomic"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// Email delivery semantics
//
// No supported provider (SendGrid, SES, Microsoft Graph, Gmail) accepts a
// caller-supplied idempotency key on send, and a lost success response cannot
// be told apart from a send that never arrived. So:
//
//   - A send that is not retried is at-most-once.
//   - A send retried after an uncertain outcome is effectively at-least-once:
//     the retry may deliver a duplicate.
//   - Exactly-once delivery is not provided, and send is not idempotent.
//
// Every failed send is therefore classified as exactly one of
// shared.ErrNotDelivered (definitive: the provider did not accept it) or
// shared.ErrDeliveryUnknown (ambiguous: it may have been accepted). The
// registry's RetryPolicy for send-email is not-delivered: only a transient
// ErrNotDelivered — which cannot duplicate the message — is retried
// automatically; ErrDeliveryUnknown never is.
//
// Adapters decide "not delivered" from evidence, not error types: they send
// with TraceWrites and pass the tracker to ClassifyAfterSend, so any failure
// before the transport began writing the request (DNS, dial, TLS handshake, a
// deadline that expired while connecting) is not delivered, and any failure
// once it began — even part-way through the request — is unknown.

// Outcome is the delivery outcome a send reports.
type Outcome string

const (
	// OutcomeAccepted: the provider accepted the message (2xx).
	OutcomeAccepted Outcome = "accepted"
	// OutcomeNotDelivered: the provider definitively did not accept it.
	OutcomeNotDelivered Outcome = "not_delivered"
	// OutcomeUnknown: the provider may have accepted it; a resend may duplicate.
	OutcomeUnknown Outcome = "unknown"
)

// SendError is a failed send with its delivery outcome. It matches
// shared.ErrNotDelivered or shared.ErrDeliveryUnknown (never ErrUpstream),
// and errors.As gives the provider and HTTP status, when one was received.
type SendError struct {
	Outcome    Outcome
	Provider   string
	StatusCode int // 0 when no response was received
	Err        error
	// IntentUnrecorded: the outcome could not be stored on the call's send
	// intent, which is left pending; an automatic retry with the same
	// messageKey would be refused as a duplicate, so none is allowed.
	IntentUnrecorded bool
}

func (e *SendError) Error() string {
	status := ""
	if e.StatusCode != 0 {
		status = fmt.Sprintf(" (status %d)", e.StatusCode)
	}
	switch e.Outcome {
	case OutcomeNotDelivered:
		return fmt.Sprintf("%s: %s%s: %v", shared.ErrNotDelivered, e.Provider, status, e.Err)
	default:
		return fmt.Sprintf("%s: %s%s: %v — do not resend without verifying delivery; a resend may duplicate the email",
			shared.ErrDeliveryUnknown, e.Provider, status, e.Err)
	}
}

// Unwrap exposes the cause for errors.Is and errors.As — but never a class
// the cause carries. SendError's own ErrorClass is the failure's only class:
// a classified error inside it (a transient token failure under an unknown
// outcome, a validation cause under an unrecorded intent, shared.ErrUpstream
// which would make it look retryable) must not let errors.As or errors.Is,
// nor shared.ClassOf, find a class that disagrees. The view returned hides
// every shared.Classifier in the chain and the class-bearing sentinels;
// everything else (provider error types, context errors) stays reachable.
// Err always holds the cause.
func (e *SendError) Unwrap() error {
	if e.Err == nil {
		return nil
	}
	return declassified{e.Err}
}

func (e *SendError) Is(target error) bool {
	switch e.Outcome {
	case OutcomeNotDelivered:
		return target == shared.ErrNotDelivered
	default:
		return target == shared.ErrDeliveryUnknown
	}
}

// ErrorClass classifies the failed send for retry decisions:
//
//   - unknown outcome (timeout after sending, 5xx, reset): ClassUnknown —
//     the provider may have accepted it, so it is never retried
//     automatically;
//   - not delivered with a provider status: that status's class — 408/429
//     transient, other 4xx (invalid recipient, rejected message, bad
//     credentials) permanent;
//   - not delivered without a response (DNS failure, connection refused or
//     connect timeout, a document-ref store outage before sending): the
//     cause's class.
//
// Only a transient, not-delivered failure may be retried automatically, and
// it cannot duplicate the email.
func (e *SendError) ErrorClass() (shared.Class, string) {
	if e.IntentUnrecorded {
		return shared.ClassUnknown, "send intent not recorded: verify delivery, then resend explicitly"
	}
	if e.Outcome != OutcomeNotDelivered {
		return shared.ClassUnknown, "delivery outcome unknown"
	}
	// A provider error code is more specific than its status (a throttling
	// code can come with a 400 or 403): one an adapter classified (Gmail's
	// rateLimitExceeded) or one the cause exposes (an AWS ErrorCode).
	if class, reason := shared.ClassOf(e.Err); strings.HasPrefix(reason, "api ") {
		return class, "not delivered: " + reason
	}
	if class, reason := shared.ClassifyCause(e.Err); strings.HasPrefix(reason, "api ") {
		return class, "not delivered: " + reason
	}
	if e.StatusCode != 0 {
		return shared.ClassifyHTTPStatus(e.StatusCode), fmt.Sprintf("not delivered: http %d", e.StatusCode)
	}
	class, reason := shared.ClassOf(e.Err)
	if class == shared.ClassUnknown {
		class, reason = shared.ClassifyCause(e.Err)
	}
	return class, "not delivered: " + reason
}

// NotDelivered reports a send the provider definitively did not accept.
func NotDelivered(provider string, statusCode int, err error) error {
	return &SendError{Outcome: OutcomeNotDelivered, Provider: provider, StatusCode: statusCode, Err: err}
}

// Unknown reports a send whose outcome is uncertain.
func Unknown(provider string, statusCode int, err error) error {
	return &SendError{Outcome: OutcomeUnknown, Provider: provider, StatusCode: statusCode, Err: err}
}

// WriteTracker records whether any part of an HTTP request to the provider
// may have been written. It is safe for concurrent use.
type WriteTracker struct{ wrote atomic.Bool }

// TraceWrites returns ctx carrying an httptrace hook that marks the tracker as
// soon as the transport starts writing a request made with that context —
// its first header field — not only once it is written in full. A request
// written partly or completely may have reached the provider, so a failure
// after that point is never classed not delivered; a failure before it (DNS,
// dial, TLS handshake) still is.
func TraceWrites(ctx context.Context) (context.Context, *WriteTracker) {
	t := &WriteTracker{}
	mark := func() { t.wrote.Store(true) }
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteHeaderField: func(string, []string) { mark() },
		WroteHeaders:     mark,
		WroteRequest:     func(httptrace.WroteRequestInfo) { mark() },
	}), t
}

// Wrote reports whether a request may have been written, in part or in full.
func (t *WriteTracker) Wrote() bool { return t != nil && t.wrote.Load() }

// ClassifyAfterSend classifies an error that came back without a provider
// response, from whether writing the request had started: a request no byte
// of which was written cannot have been accepted (not delivered); any other
// may have been (unknown).
func ClassifyAfterSend(provider string, err error, t *WriteTracker) error {
	if t.Wrote() {
		return Unknown(provider, 0, err)
	}
	return NotDelivered(provider, 0, err)
}

// ClassifyStatus classifies a non-2xx provider response. A 4xx is a definitive
// rejection (including 429: throttled, not accepted). Anything else — 5xx in
// particular — is unknown, because a gateway or provider error can follow an
// acceptance.
func ClassifyStatus(provider string, statusCode int, err error) error {
	if statusCode >= 400 && statusCode < 500 {
		return NotDelivered(provider, statusCode, err)
	}
	return Unknown(provider, statusCode, err)
}

// ClassifyTransport classifies an error that came back without a provider
// response, from the error alone. It is the fallback for a client that cannot
// trace writes; adapters use ClassifyAfterSend. Only failures that provably happened before the request could
// reach the provider — connection never established (dial, DNS) or TLS
// handshake failed — are definitive; every other failure (timeout, reset,
// EOF, cancellation mid-request) is unknown.
func ClassifyTransport(provider string, err error) error {
	if sentNothing(err) {
		return NotDelivered(provider, 0, err)
	}
	return Unknown(provider, 0, err)
}

func sentNothing(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var certErr *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	// tls.RecordHeaderError is deliberately absent: it can also come from a
	// connection the request was already written to.
	return errors.As(err, &certErr) || errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostnameErr)
}

// classifySend gives an error from ProviderClient.Send its delivery outcome.
// An adapter's own classification is kept; anything unclassified (a custom or
// mock client) is treated as unknown, the conservative choice.
func classifySend(provider string, err error) error {
	if errors.Is(err, shared.ErrNotDelivered) || errors.Is(err, shared.ErrDeliveryUnknown) {
		return err
	}
	// Includes a context cancelled or timed out mid-send: the request may
	// already have been accepted.
	return Unknown(provider, 0, err)
}

// OutcomeOf returns the delivery outcome err reports, or "" when err is not a
// delivery outcome.
func OutcomeOf(err error) Outcome {
	switch {
	case err == nil:
		return OutcomeAccepted
	case errors.Is(err, shared.ErrNotDelivered):
		return OutcomeNotDelivered
	case errors.Is(err, shared.ErrDeliveryUnknown):
		return OutcomeUnknown
	default:
		return ""
	}
}

// declassified is the view of a SendError's cause that its Unwrap exposes:
// errors.Is and errors.As see the cause's chain except every
// shared.Classifier in it (whose own Is would also match class sentinels) and
// the sentinels that carry a class.
type declassified struct{ err error }

func (d declassified) Error() string { return d.err.Error() }

// classSentinels carry a class of their own (shared.ClassOf, the retry
// policies); a SendError's class comes only from its ErrorClass.
var classSentinels = []error{
	shared.ErrValidation, shared.ErrMissingTenant, shared.ErrMissingInternalAuth,
	shared.ErrUpstream, shared.ErrNotDelivered, shared.ErrDeliveryUnknown,
}

func (d declassified) Is(target error) bool {
	for _, sentinel := range classSentinels {
		if target == sentinel {
			return false
		}
	}
	return walkChain(d.err, func(err error) bool {
		if err == target {
			return true
		}
		if matcher, ok := err.(interface{ Is(error) bool }); ok {
			return matcher.Is(target)
		}
		return false
	})
}

func (d declassified) As(target any) bool {
	val := reflect.ValueOf(target)
	if val.Kind() != reflect.Pointer || val.IsNil() {
		return false
	}
	targetType := val.Type().Elem()
	return walkChain(d.err, func(err error) bool {
		if reflect.TypeOf(err).AssignableTo(targetType) {
			val.Elem().Set(reflect.ValueOf(err))
			return true
		}
		if asser, ok := err.(interface{ As(any) bool }); ok {
			return asser.As(target)
		}
		return false
	})
}

// walkChain calls match on err and every error it wraps, depth first,
// skipping (but descending through) each shared.Classifier, until match
// reports true.
func walkChain(err error, match func(error) bool) bool {
	for err != nil {
		if _, classified := err.(shared.Classifier); !classified && match(err) {
			return true
		}
		switch wrapped := err.(type) {
		case interface{ Unwrap() error }:
			err = wrapped.Unwrap()
		case interface{ Unwrap() []error }:
			for _, inner := range wrapped.Unwrap() {
				if walkChain(inner, match) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}
