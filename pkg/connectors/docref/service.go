package docref

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Service creates and resolves document references: content in the
// ContentStore (S3), metadata in the Store (Valkey). It is the only way
// connectors reach document content.
type Service struct {
	refs      Store
	content   ContentStore
	keyPrefix string
}

// ServiceOption configures a Service.
type ServiceOption func(*Service)

// WithKeyPrefix places every object under prefix in the bucket (for
// example "docrefs/"), so a lifecycle rule can target exactly these objects.
func WithKeyPrefix(prefix string) ServiceOption {
	return func(s *Service) { s.keyPrefix = prefix }
}

// cleanupTimeout bounds the best-effort removal of an object whose reference
// could not be stored.
const cleanupTimeout = 10 * time.Second

func NewService(refs Store, content ContentStore, opts ...ServiceOption) *Service {
	s := &Service{refs: refs, content: content}
	for _, o := range opts {
		o(s)
	}
	return s
}

// objectKey is the one object key a ref may point at: <prefix><tenant>/<uuid>,
// derived from the tenant and the ref ID.
func (s *Service) objectKey(tenantID, id string) string {
	return s.keyPrefix + tenantID + "/" + strings.TrimPrefix(id, Prefix)
}

// maxTenantIDLength bounds a tenant ID so object keys stay well inside S3's
// 1024-byte key limit.
const maxTenantIDLength = 255

// validTenant accepts a tenant ID that can scope an object key: non-empty,
// valid UTF-8, no path separators, control characters or dot segments.
func validTenant(tenantID string) error {
	if tenantID == "" || len(tenantID) > maxTenantIDLength || !utf8.ValidString(tenantID) ||
		strings.ContainsAny(tenantID, "/\\") || tenantID == "." || tenantID == ".." ||
		strings.IndexFunc(tenantID, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: %q", ErrInvalidTenant, tenantID)
	}
	return nil
}

// Create writes content to S3, then persists the reference metadata, and
// returns the ref. The object is written first, so a returned ref always
// points at an existing object. If the metadata write fails the object is
// removed (best effort; the bucket lifecycle rule removes any leftover).
func (s *Service) Create(ctx context.Context, tenantID, contentType string, content []byte) (Ref, error) {
	if err := validTenant(tenantID); err != nil {
		return Ref{}, err
	}
	id := NewID()
	key := s.objectKey(tenantID, id)
	if err := s.content.Put(ctx, key, content, contentType); err != nil {
		return Ref{}, fmt.Errorf("docref: store content: %w", err)
	}
	ref, err := s.refs.Put(ctx, Ref{
		ID:          id,
		TenantID:    tenantID,
		Bucket:      s.content.Bucket(),
		ObjectKey:   key,
		ContentType: contentType,
		Size:        int64(len(content)),
		SHA256:      Checksum(content),
	})
	if err != nil {
		// Detached from the caller's cancellation but still bounded, so a
		// stalled S3 connection cannot hold the task after its deadline.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		_ = s.content.Delete(cleanupCtx, s.content.Bucket(), key)
		cancel()
		return Ref{}, fmt.Errorf("docref: store reference: %w", err)
	}
	return ref, nil
}

// Lookup returns the ref's metadata for tenantID without touching S3.
func (s *Service) Lookup(ctx context.Context, tenantID, id string) (Ref, error) {
	if err := validTenant(tenantID); err != nil {
		return Ref{}, err
	}
	// Only an ID this package could have issued is looked up: anything else
	// can never resolve (and must not reach a key that is not a ref).
	if !validID(id) {
		return Ref{}, fmt.Errorf("%w: %q is not a document reference", ErrNotFound, id)
	}
	ref, found, err := s.refs.Get(ctx, tenantID, id)
	if err != nil {
		return Ref{}, fmt.Errorf("docref: read reference: %w", err)
	}
	if !found {
		return Ref{}, fmt.Errorf("%w: %q (expired, another tenant's, or never created)", ErrNotFound, id)
	}
	// Defence in depth: the store already scopes by tenant; the record must
	// also point at exactly the object Create wrote for this tenant and ID in
	// this service's bucket — not merely somewhere under the tenant's prefix —
	// so a forged or corrupted record can never reach another object (another
	// tenant's, or another of this tenant's documents).
	if ref.TenantID != tenantID || ref.ID != id || ref.Bucket != s.content.Bucket() || ref.ObjectKey != s.objectKey(tenantID, id) {
		return Ref{}, fmt.Errorf("%w: reference %q points outside its tenant's documents", ErrIntegrityViolation, id)
	}
	if ref.Size < 0 || !validSHA256(ref.SHA256) {
		return Ref{}, fmt.Errorf("%w: reference %q has malformed size or checksum", ErrIntegrityViolation, id)
	}
	return ref, nil
}

func validID(id string) bool {
	raw, ok := strings.CutPrefix(id, Prefix)
	if !ok {
		return false
	}
	_, err := uuid.Parse(raw)
	return err == nil
}

func validSHA256(h string) bool {
	if len(h) != 64 {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil
}

// Open resolves the ref and streams its content from S3. The stream verifies
// as it goes: reading past the recorded size, or reaching the end with a
// different size or SHA-256, returns ErrIntegrityViolation instead of
// io.EOF — so a caller must treat the content as valid only once it has read
// to io.EOF without error. Large documents are never held in memory.
func (s *Service) Open(ctx context.Context, tenantID, id string) (Ref, io.ReadCloser, error) {
	ref, err := s.Lookup(ctx, tenantID, id)
	if err != nil {
		return Ref{}, nil, err
	}
	body, err := s.open(ctx, ref)
	if err != nil {
		return Ref{}, nil, err
	}
	return ref, body, nil
}

// open streams the content of an already looked-up ref, verifying it.
func (s *Service) open(ctx context.Context, ref Ref) (io.ReadCloser, error) {
	id := ref.ID
	body, size, err := s.content.Open(ctx, ref.Bucket, ref.ObjectKey)
	if errors.Is(err, ErrObjectNotFound) {
		return nil, fmt.Errorf("%w: reference %q: s3://%s/%s", ErrSourceMissing, id, ref.Bucket, ref.ObjectKey)
	}
	if err != nil {
		return nil, fmt.Errorf("docref: read content: %w", err)
	}
	if size >= 0 && size != ref.Size {
		_ = body.Close()
		return nil, fmt.Errorf("%w: reference %q: object is %d bytes, reference records %d", ErrIntegrityViolation, id, size, ref.Size)
	}
	return &verifyingReader{body: body, id: id, size: ref.Size, want: ref.SHA256, h: sha256.New()}, nil
}

// Read resolves the ref and returns its verified content, refusing a
// document larger than maxBytes before downloading it. Use it where the
// whole payload is needed at once (an email attachment); use Open to stream.
func (s *Service) Read(ctx context.Context, tenantID, id string, maxBytes int64) (Ref, []byte, error) {
	ref, err := s.Lookup(ctx, tenantID, id)
	if err != nil {
		return Ref{}, nil, err
	}
	if ref.Size > maxBytes {
		return Ref{}, nil, fmt.Errorf("%w: reference %q is %d bytes (limit %d)", ErrTooLarge, id, ref.Size, maxBytes)
	}
	body, err := s.open(ctx, ref)
	if err != nil {
		return Ref{}, nil, err
	}
	defer func() { _ = body.Close() }()
	var buf bytes.Buffer
	buf.Grow(int(ref.Size) + bytes.MinRead)
	if _, err := buf.ReadFrom(body); err != nil {
		return Ref{}, nil, err
	}
	return ref, buf.Bytes(), nil
}

// Delete removes the reference, then its object. Once the reference is gone
// nothing resolves it; if the object delete fails the bucket lifecycle rule
// removes it later.
func (s *Service) Delete(ctx context.Context, tenantID, id string) error {
	ref, err := s.Lookup(ctx, tenantID, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.refs.Delete(ctx, tenantID, id); err != nil {
		return fmt.Errorf("docref: delete reference: %w", err)
	}
	if err := s.content.Delete(ctx, ref.Bucket, ref.ObjectKey); err != nil {
		return fmt.Errorf("docref: reference deleted, object left for the lifecycle rule: %w", err)
	}
	return nil
}

// verifyingReader passes the object through while counting and hashing it.
type verifyingReader struct {
	body io.ReadCloser
	id   string
	size int64
	want string
	h    hash.Hash
	n    int64
	err  error // sticky verification failure
}

func (v *verifyingReader) Read(p []byte) (int, error) {
	if v.err != nil {
		return 0, v.err
	}
	n, err := v.body.Read(p)
	v.n += int64(n)
	v.h.Write(p[:n])
	if v.n > v.size {
		v.err = fmt.Errorf("%w: reference %q: object is longer than the recorded %d bytes", ErrIntegrityViolation, v.id, v.size)
		return 0, v.err
	}
	if err == io.EOF {
		switch {
		case v.n != v.size:
			v.err = fmt.Errorf("%w: reference %q: object is %d bytes, reference records %d", ErrIntegrityViolation, v.id, v.n, v.size)
		case hex.EncodeToString(v.h.Sum(nil)) != v.want:
			v.err = fmt.Errorf("%w: reference %q: SHA-256 does not match the reference", ErrIntegrityViolation, v.id)
		}
		if v.err != nil {
			return n, v.err
		}
	}
	return n, err
}

func (v *verifyingReader) Close() error { return v.body.Close() }
