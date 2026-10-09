// Package docref holds document references: opaque IDs that let one connector
// task hand a document to another (storage fetch → send-email attachment,
// storage fetch → storage upload) without the bytes passing through workflow
// variables.
//
// # Where things live
//
//   - Content: S3 is the system of record for document content
//     (ContentStore; package s3content). Each document is one object at
//     <prefix><tenant>/<uuid> in the platform's document bucket.
//   - Metadata: Valkey holds the reference — a small record with the S3
//     location, size and SHA-256, never the bytes (Store; package
//     valkeystore). Valkey memory grows with the number of refs, not with
//     document size.
//
// A reference is a pointer, not a snapshot. Consumers resolve it through
// Service, which reads the metadata, checks the tenant, fetches the object
// from S3 and verifies its size and SHA-256 on every resolution: an object
// that is missing is ErrSourceMissing, and one that was overwritten or
// truncated is ErrIntegrityViolation. Nothing reads document content from
// Valkey.
//
// # Consistency
//
//   - Store.Put is create-only and atomic, and returns only after the write
//     is durable; any replica resolves a ref as soon as it is returned.
//   - A ref's metadata never changes after creation; Delete removes it.
//   - A ref is scoped to its tenant: the stored tenant must match the caller,
//     and the object key must be exactly <prefix><tenant>/<uuid> for the
//     ref's own ID, before any S3 request is made.
//   - A ref expires after its TTL (default 24 h) through Valkey's key expiry;
//     the bucket's lifecycle rule removes the object (see the runbook).
package docref

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Prefix marks a document reference, so a literal value is never mistaken
// for one. It is also the Valkey key prefix: the key of a ref is its ID.
const Prefix = "docref:"

// DefaultTTL is how long a ref stays resolvable.
const DefaultTTL = 24 * time.Hour

// Ref is a document reference's metadata. It never carries content.
type Ref struct {
	ID          string // reference identifier, docref:<uuid>
	TenantID    string
	Bucket      string // S3 bucket holding the content
	ObjectKey   string // S3 object key: <prefix><tenant>/<uuid>
	ContentType string
	Size        int64  // bytes, verified on every resolution
	SHA256      string // hex digest of the object, verified on every resolution
	CreatedAt   time.Time
	UpdatedAt   time.Time // equals CreatedAt: refs are never updated
}

// Store keeps reference metadata (Valkey in production).
type Store interface {
	// Put creates ref and returns it with its timestamps set. It never
	// overwrites: an existing ID holding a different ref is ErrExists. It is
	// idempotent: an existing ID holding exactly this ref (a retry after a
	// lost reply) returns the stored ref. It returns only after the write is
	// durable per the store's configuration.
	Put(ctx context.Context, ref Ref) (Ref, error)

	// Get returns the unexpired ref with id for tenantID. A missing, expired
	// or other-tenant ref is (Ref{}, false, nil), never an error.
	Get(ctx context.Context, tenantID, id string) (ref Ref, found bool, err error)

	// Delete removes the ref; deleting a missing ref is not an error.
	Delete(ctx context.Context, tenantID, id string) error
}

// ContentStore keeps document content (S3 in production). Access comes from
// the platform's own credentials; a ref never carries credentials.
type ContentStore interface {
	// Bucket is the bucket new documents are written to.
	Bucket() string
	// Put writes content at key in Bucket().
	Put(ctx context.Context, key string, content []byte, contentType string) error
	// Open streams the object. A missing object is ErrObjectNotFound. size is
	// the object's stored length.
	Open(ctx context.Context, bucket, key string) (body io.ReadCloser, size int64, err error)
	// Delete removes the object; deleting a missing object is not an error.
	Delete(ctx context.Context, bucket, key string) error
}

var (
	// ErrNotFound (DocumentReferenceNotFound): no unexpired reference with
	// that ID exists for the tenant.
	ErrNotFound = errors.New("docref: document reference not found")
	// ErrSourceMissing (DocumentSourceMissing): the reference exists but its
	// S3 object does not.
	ErrSourceMissing = errors.New("docref: document source object missing")
	// ErrIntegrityViolation (DocumentIntegrityViolation): the object's size
	// or SHA-256 does not match the reference, or the reference does not
	// point at exactly its own object (<prefix><tenant>/<uuid>).
	ErrIntegrityViolation = errors.New("docref: document integrity violation")
	// ErrTooLarge: the document exceeds the caller's limit.
	ErrTooLarge = errors.New("docref: document too large")
	// ErrInvalidTenant: the tenant ID cannot scope an object key.
	ErrInvalidTenant = errors.New("docref: invalid tenant id")

	// ErrUnavailable: a store is temporarily unable to serve, or to accept
	// or confirm a write (Valkey full under noeviction, a write not yet in
	// the AOF, a node refusing during failover or resharding). The call may
	// succeed later; a retry creates a new ref.
	ErrUnavailable = errors.New("docref: store temporarily unavailable")

	// ErrExists means a ref with that ID already exists: refs are create-only.
	ErrExists = errors.New("docref: reference already exists")
	// ErrObjectNotFound is returned by ContentStore.Open for a missing object.
	ErrObjectNotFound = errors.New("docref: object not found")
)

// IsResolutionFailure reports whether err means the ref can never resolve as
// it stands (not found, source missing, integrity violation, too large), as
// opposed to a transient store failure.
func IsResolutionFailure(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrSourceMissing) ||
		errors.Is(err, ErrIntegrityViolation) || errors.Is(err, ErrTooLarge) ||
		errors.Is(err, ErrInvalidTenant)
}

// IsRef reports whether s has the shape of a document reference.
func IsRef(s string) bool { return strings.HasPrefix(s, Prefix) && len(s) > len(Prefix) }

// NewID returns a fresh ref ID.
func NewID() string { return Prefix + uuid.NewString() }

// Checksum returns the hex SHA-256 of content.
func Checksum(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
