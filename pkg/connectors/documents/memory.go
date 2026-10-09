package documents

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryStore is an in-process Store with the same semantics as the SQL
// store: one mutex stands in for the database's row locking and unique
// constraint. It suits tests and a single worker process only — uniqueness
// across worker replicas needs a shared database (package sqlstore).
type MemoryStore struct {
	mu       sync.Mutex
	byID     map[string]*Document
	identity map[Identity]string
	attempts []AttemptRecord
	now      func() time.Time
}

// AttemptRecord is the audit trail of one finished attempt.
type AttemptRecord struct {
	DocumentID string
	Attempt    string
	Outcome    string // "available", "failed", "deleted"
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byID:     make(map[string]*Document),
		identity: make(map[Identity]string),
		now:      time.Now,
	}
}

// SetClock replaces the store's clock, for lease-expiry tests.
func (m *MemoryStore) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

// Attempts returns the audit trail recorded so far.
func (m *MemoryStore) Attempts() []AttemptRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]AttemptRecord(nil), m.attempts...)
}

// Len returns the number of document rows.
func (m *MemoryStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byID)
}

func (m *MemoryStore) Claim(_ context.Context, identity Identity, attempt string, lease time.Duration) (Document, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()

	id, exists := m.identity[identity]
	if !exists {
		doc := &Document{
			ID:             uuid.NewString(),
			Identity:       identity,
			State:          StatePendingUpload,
			Version:        1,
			Owner:          attempt,
			LeaseExpiresAt: now.Add(lease),
			ClaimedAt:      now,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		m.byID[doc.ID] = doc
		m.identity[identity] = doc.ID
		return *doc, true, nil
	}
	return m.takeoverLocked(m.byID[id], attempt, lease, now)
}

func (m *MemoryStore) ClaimExisting(_ context.Context, identity Identity, attempt string, lease time.Duration) (Document, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, exists := m.identity[identity]
	if !exists {
		return Document{}, false, ErrNotFound
	}
	return m.takeoverLocked(m.byID[id], attempt, lease, m.now())
}

func (m *MemoryStore) takeoverLocked(doc *Document, attempt string, lease time.Duration, now time.Time) (Document, bool, error) {
	claimable := doc.State == StateAvailable || doc.State == StateFailed || !now.Before(doc.LeaseExpiresAt)
	if !claimable {
		return *doc, false, nil
	}
	doc.State = StatePendingUpload
	doc.Version++
	doc.Owner = attempt
	doc.LeaseExpiresAt = now.Add(lease)
	doc.ClaimedAt = now
	doc.UpdatedAt = now
	return *doc, true, nil
}

func (m *MemoryStore) Advance(_ context.Context, docID, attempt string, to State) (Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, err := m.ownedLocked(docID, attempt)
	if err != nil {
		return Document{}, err
	}
	doc.State = to
	doc.Version++
	doc.UpdatedAt = m.now()
	return *doc, nil
}

func (m *MemoryStore) Complete(_ context.Context, docID, attempt string, result Result) (Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, err := m.ownedLocked(docID, attempt)
	if err != nil {
		return Document{}, err
	}
	now := m.now()
	m.attempts = append(m.attempts, AttemptRecord{DocumentID: doc.ID, Attempt: attempt, Outcome: "available", StartedAt: doc.ClaimedAt, FinishedAt: now})
	doc.State = StateAvailable
	doc.Version++
	doc.ObjectID = result.ObjectID
	doc.ContentType = result.ContentType
	doc.SizeBytes = result.SizeBytes
	doc.LastError = ""
	doc.Owner = ""
	doc.LeaseExpiresAt = time.Time{}
	doc.UpdatedAt = now
	return *doc, nil
}

func (m *MemoryStore) Fail(_ context.Context, docID, attempt, reason string) (Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, err := m.ownedLocked(docID, attempt)
	if err != nil {
		return Document{}, err
	}
	now := m.now()
	m.attempts = append(m.attempts, AttemptRecord{DocumentID: doc.ID, Attempt: attempt, Outcome: "failed", Error: reason, StartedAt: doc.ClaimedAt, FinishedAt: now})
	doc.State = StateFailed
	doc.Version++
	doc.LastError = reason
	doc.FailedAttempts++
	doc.Owner = ""
	doc.LeaseExpiresAt = time.Time{}
	doc.UpdatedAt = now
	return *doc, nil
}

func (m *MemoryStore) Remove(_ context.Context, docID, attempt string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, err := m.ownedLocked(docID, attempt)
	if err != nil {
		return err
	}
	m.attempts = append(m.attempts, AttemptRecord{DocumentID: doc.ID, Attempt: attempt, Outcome: "deleted", StartedAt: doc.ClaimedAt, FinishedAt: m.now()})
	delete(m.identity, doc.Identity)
	delete(m.byID, doc.ID)
	return nil
}

func (m *MemoryStore) Get(_ context.Context, identity Identity) (Document, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, exists := m.identity[identity]
	if !exists {
		return Document{}, false, nil
	}
	return *m.byID[id], true, nil
}

// ownedLocked returns the row when attempt still owns it with a live lease.
func (m *MemoryStore) ownedLocked(docID, attempt string) (*Document, error) {
	doc, ok := m.byID[docID]
	if !ok || doc.Owner != attempt || !m.now().Before(doc.LeaseExpiresAt) {
		return nil, ErrOwnershipLost
	}
	return doc, nil
}

var _ Store = (*MemoryStore)(nil)
