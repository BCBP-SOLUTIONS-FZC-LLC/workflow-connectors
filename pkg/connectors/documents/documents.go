// Package documents is the uniqueness authority for uploads to object stores
// that cannot create a file atomically "if absent" (Google Drive). A database
// row per logical document — tenant + provider + container + filename, unique
// by constraint — decides which upload owns the document; the object store is
// only written by the owner, and files are addressed by the ID recorded on the
// row, never found again by name.
//
// # State machine
//
//	(none) ──Claim──▶ PENDING_UPLOAD ──Advance──▶ UPLOADING ──Complete──▶ AVAILABLE
//	                       ▲                          │
//	                       │                          └────Fail────▶ FAILED
//	AVAILABLE, FAILED, or a PENDING_UPLOAD/UPLOADING/DELETING row whose lease
//	expired ──Claim (CAS takeover)──▶ PENDING_UPLOAD
//	AVAILABLE, FAILED, or an expired lease ──ClaimExisting──▶ PENDING_UPLOAD
//	  ──Advance──▶ DELETING ──Remove──▶ (row deleted)
//
// A claim makes the caller the row's owner (a random attempt ID) until its
// lease expires. Every later transition is a compare-and-set on the owner, so
// a caller whose lease was taken over gets ErrOwnershipLost and must not
// record anything. A second upload of the same document while it is
// PENDING_UPLOAD, UPLOADING or DELETING with a live lease is refused with an
// *InProgressError: deterministic, retryable, and it never touches the object
// store. A re-upload of an AVAILABLE document, or a retry of a FAILED one,
// claims the same row and replaces the same object in place.
//
// # Transaction boundaries
//
// Each Store method is atomic on its own: Claim is one INSERT … ON CONFLICT
// DO UPDATE … WHERE (insert, or take over an idle or expired row); Advance is one
// conditional UPDATE; Complete, Fail and Remove each update or delete the row
// and append the audit record in one transaction. No transaction is held open
// across an object-store call.
package documents

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

type State string

const (
	StatePendingUpload State = "PENDING_UPLOAD"
	StateUploading     State = "UPLOADING"
	StateAvailable     State = "AVAILABLE"
	StateFailed        State = "FAILED"
	// StateDeleting extends the upload lifecycle so a delete holds the row
	// exactly as an upload does.
	StateDeleting State = "DELETING"
)

// Busy reports whether a document in state s is owned by an in-flight call.
func (s State) Busy() bool {
	return s == StatePendingUpload || s == StateUploading || s == StateDeleting
}

// Identity is a logical document. At most one row exists per Identity.
type Identity struct {
	TenantID  string
	Provider  string
	Container string // bucket, or Drive folder ID
	Filename  string
}

type Document struct {
	ID string
	Identity
	State   State
	Version int64
	// ObjectID is the object store's own ID for the document's file (a Drive
	// file ID), once one has been written. A replace keeps it.
	ObjectID       string
	ContentType    string
	SizeBytes      int64
	Owner          string
	LeaseExpiresAt time.Time
	ClaimedAt      time.Time
	LastError      string
	FailedAttempts int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Result is what a completed upload records.
type Result struct {
	ObjectID    string
	ContentType string
	SizeBytes   int64
}

// Store is the uniqueness authority. Implementations must make each method
// atomic and enforce one row per Identity with a unique constraint, never by
// reading before writing.
type Store interface {
	// Claim takes ownership of identity for attempt: it inserts a new
	// PENDING_UPLOAD row, or takes over an AVAILABLE or FAILED row, or one
	// whose lease expired, by compare-and-set. claimed is false when another
	// owner holds a live lease; doc is then that row.
	Claim(ctx context.Context, identity Identity, attempt string, lease time.Duration) (doc Document, claimed bool, err error)

	// ClaimExisting is Claim without the insert: it only takes over an
	// existing row, and returns ErrNotFound when none exists. (Drive's delete
	// uses Claim, so a delete serialises with an upload that would adopt the
	// same file.)
	ClaimExisting(ctx context.Context, identity Identity, attempt string, lease time.Duration) (doc Document, claimed bool, err error)

	// Advance moves an owned row to state (UPLOADING or DELETING).
	Advance(ctx context.Context, docID, attempt string, to State) (Document, error)

	// Complete marks an owned row AVAILABLE with result, releases ownership
	// and records a successful attempt.
	Complete(ctx context.Context, docID, attempt string, result Result) (Document, error)

	// Fail marks an owned row FAILED with reason, releases ownership and
	// records a failed attempt. A recorded ObjectID is kept.
	Fail(ctx context.Context, docID, attempt, reason string) (Document, error)

	// Remove deletes an owned row and records the deletion.
	Remove(ctx context.Context, docID, attempt string) error

	// Get returns the row for identity, if any.
	Get(ctx context.Context, identity Identity) (doc Document, found bool, err error)
}

var (
	// ErrOwnershipLost means the caller no longer owns the row: its lease
	// expired and another call claimed it. The caller must stop.
	ErrOwnershipLost = errors.New("documents: ownership lost")
	ErrNotFound      = errors.New("documents: not found")
	// ErrUploadInProgress matches an *InProgressError.
	ErrUploadInProgress = errors.New("documents: upload in progress")
)

// InProgressError is returned when another call owns the document. It matches
// ErrUploadInProgress and shared.ErrUpstream, so the worker retries it under
// the connector's retry policy, and errors.As exposes the document ID.
type InProgressError struct {
	DocumentID string
	State      State
}

func (e *InProgressError) Error() string {
	return fmt.Sprintf("%s: %s: document %s is %s by another call", shared.ErrUpstream, ErrUploadInProgress, e.DocumentID, e.State)
}

// ErrorClass: transient — the owning call finishes or its lease expires.
func (e *InProgressError) ErrorClass() (shared.Class, string) {
	return shared.ClassTransient, "upload in progress"
}

func (e *InProgressError) Is(target error) bool {
	return target == ErrUploadInProgress || target == shared.ErrUpstream
}

// DefaultLease bounds how long an owner may hold a document. It must exceed
// the worker's per-connector execution timeout, so a live call never loses
// its lease; a crashed call's document becomes claimable once it expires.
const DefaultLease = 15 * time.Minute
