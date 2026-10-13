// Package sendintent records send intents so a request that arrives twice is
// rejected before the second send starts.
//
// This is duplicate-request protection, NOT idempotency. It stops the same
// business message (the caller's messageKey) from entering the service twice —
// a retried workflow step, a double click, a redelivered event. It cannot
// make a provider send exactly once: if a send's outcome is unknown (the
// provider may have accepted it) and an operator explicitly resends, the email
// may be delivered twice. See package sendemail for the delivery semantics.
package sendintent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// Status is the recorded state of a send intent.
type Status string

const (
	StatusPending      Status = "pending"       // reserved; send not finished (or the worker died mid-send)
	StatusAccepted     Status = "accepted"      // the provider accepted the message
	StatusNotDelivered Status = "not_delivered" // the provider definitively did not accept it
	StatusUnknown      Status = "unknown"       // the provider may have accepted it
)

// StalePendingAfter is how long an intent may stay pending, unchanged, before
// an explicit resend may take it over. A send finishes or is recorded well
// within it (provider calls are bounded to 2 minutes, recording to 10 s), so
// an older pending intent means the worker stopped mid-send or its outcome
// could not be recorded. Its delivery is unknown, as for StatusUnknown.
const StalePendingAfter = 15 * time.Minute

type Intent struct {
	ID         string
	TenantID   string
	MessageKey string
	Status     Status
	Attempts   int
	// ReservationToken is the caller-generated token of the reservation that
	// made Attempts current. A caller whose Reserve reply was lost reads the
	// intent back and owns it only when the token is its own.
	ReservationToken  string
	ProviderMessageID string
	Detail            string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Store persists send intents, one per tenant + messageKey (unique).
type Store interface {
	// Reserve records the intent before any send, stamped with token (a
	// value unique to this call, so a caller whose reply was lost can tell
	// with Get whether the reservation is its own). reserved is false when
	// an intent for the key already exists and is not re-reserved; the
	// existing intent is returned.
	//
	// An existing intent is re-reserved (Attempts+1, status pending, the new
	// token) only when
	//   - its last outcome was not_delivered: nothing reached the provider,
	//     so trying again — an automatic retry of a transient failure —
	//     cannot duplicate the message; or
	//   - resendFrom > 0 equals its current Attempts and it is not pending:
	//     the caller's explicit resend of that specific attempt, which may
	//     deliver a duplicate if it was accepted or unknown. It is a
	//     compare-and-swap: a redelivered resend request sees Attempts =
	//     resendFrom+1 and is a duplicate, never a second resend.
	// resendFrom = 0 is no resend. Of concurrent re-reservations exactly one
	// succeeds.
	Reserve(ctx context.Context, tenantID, messageKey string, resendFrom int, token string) (intent Intent, reserved bool, err error)

	// Get returns the intent for tenant + messageKey; found is false when
	// there is none.
	Get(ctx context.Context, tenantID, messageKey string) (intent Intent, found bool, err error)

	// Record stores the outcome of attempt (the intent's Attempts when it was
	// reserved). It writes only while that attempt is still the intent's
	// current one and still pending; otherwise it changes nothing and returns
	// ErrStaleRecord — a late outcome of an earlier attempt must never
	// overwrite a newer attempt (writing not_delivered over an in-flight
	// resend would let a retry send a duplicate).
	Record(ctx context.Context, intentID string, attempt int, status Status, providerMessageID, detail string) error
}

// Releaser is implemented by a Store that can release a reservation by its
// token (MemoryStore and sqlstore.Store do). A caller whose Reserve failed
// ambiguously (the reply was lost; the reservation may have committed)
// and that cannot read its reservation back cannot tell whether it is
// left pending: if it is, it would refuse every retry with the same
// messageKey as a duplicate although nothing was sent. Release marks the
// intent not_delivered only while it is still pending under token — the
// caller has sent nothing under it — and changes nothing otherwise (no
// intent, another caller's token, already finished); that is not an error.
// released reports whether it changed the intent: only then is it certain
// that no reservation of the caller is left pending. Nothing released after
// an ambiguous Reserve is not proof that nothing was reserved: the commit
// may become visible later (for example once a synchronous standby
// confirms it). It is optional so Store stays unchanged; without it such a
// failure is reported as not retryable automatically.
type Releaser interface {
	Release(ctx context.Context, tenantID, messageKey, token string) (released bool, err error)
}

// ReleasedDetail is the detail Release records on the intent it releases.
const ReleasedDetail = "reservation released: its caller could not confirm it, and sent nothing"

// ErrPruneArgs: PruneIntents needs a positive age and batch.
var ErrPruneArgs = errors.New("sendintent: prune needs a positive age and batch")

// ErrNoToken: Release needs the reservation token to release.
var ErrNoToken = errors.New("sendintent: release needs a reservation token")

// ErrNotReserved marks a Reserve error after which the store knows that
// nothing was reserved: the statement failed before its transaction could
// commit (sqlstore wraps such errors; the error keeps its retry class). A
// Reserve error that does not match it is ambiguous — the reservation may
// have committed and only its reply been lost — and send-email recovers it
// by its token (Get, then Releaser).
var ErrNotReserved = errors.New("sendintent: nothing was reserved")

var (
	// ErrDuplicateRequest matches a *DuplicateRequestError.
	ErrDuplicateRequest = errors.New("connectors: duplicate send request")
	ErrIntentNotFound   = errors.New("sendintent: intent not found")
	// ErrStaleRecord: the outcome belongs to an attempt that is no longer the
	// intent's current pending one; nothing was written.
	ErrStaleRecord = errors.New("sendintent: outcome of a superseded attempt not recorded")
)

// DuplicateRequestError rejects a request whose messageKey already has an
// intent. Nothing was sent by this request. It matches ErrDuplicateRequest and
// shared.ErrValidation (never retried); errors.As gives the earlier intent.
type DuplicateRequestError struct {
	IntentID   string
	MessageKey string
	Status     Status
	// Attempts is the intent's current attempt: the resendAttempt an explicit
	// resend of it must name.
	Attempts int
}

func (e *DuplicateRequestError) Error() string {
	return fmt.Sprintf("%s: messageKey %q already has send intent %s (status %s, attempt %d); nothing was sent — to send again set resend: true and resendAttempt: %d, which may deliver a duplicate",
		ErrDuplicateRequest, e.MessageKey, e.IntentID, e.Status, e.Attempts, e.Attempts)
}

func (e *DuplicateRequestError) Is(target error) bool {
	return target == ErrDuplicateRequest || target == shared.ErrValidation
}
