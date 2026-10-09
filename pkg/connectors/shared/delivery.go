package shared

import "errors"

// Delivery outcome classes for connectors whose side effect cannot be made
// idempotent (send-email). Neither wraps ErrUpstream. Their retry class comes
// from the send (see sendemail.SendError.ErrorClass): a transient
// ErrNotDelivered may be retried automatically, since nothing reached the
// provider; ErrDeliveryUnknown is always class unknown and never is.
var (
	// ErrNotDelivered: the provider definitively did not accept the request —
	// it was never sent (the request was not fully written: DNS, dial or TLS
	// failure, a deadline while connecting, cancelled before sending, client
	// could not be built) or the provider rejected it (4xx). A resend cannot
	// duplicate this message; a transient one (throttling, a connection
	// failure) is retried automatically.
	ErrNotDelivered = errors.New("connectors: not delivered")

	// ErrDeliveryUnknown: the request may have reached the provider and been
	// accepted, but no success response arrived (timeout, connection reset,
	// 5xx, crash). Exactly-once delivery is impossible here: resending may
	// deliver a duplicate. Verify delivery outside the system before any
	// manual resend.
	ErrDeliveryUnknown = errors.New("connectors: delivery outcome unknown (the provider may have accepted the message)")
)

// IsRetryable reports whether err's class allows an automatic retry: only a
// transient error. Callers must still respect the connector type's
// registry.RetryPolicy (see connectors.DecideRetry, which combines both —
// for send-email, only a transient failure that was provably not delivered
// qualifies).
func IsRetryable(err error) bool { return IsTransient(err) }
