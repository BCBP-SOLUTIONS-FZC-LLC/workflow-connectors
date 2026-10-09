package sendintent

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryStore is an in-process Store for tests and a single process; it does
// not protect across worker replicas (use package sqlstore).
type MemoryStore struct {
	mu    sync.Mutex
	byKey map[[2]string]*Intent
	byID  map[string]*Intent
	now   func() time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byKey: make(map[[2]string]*Intent), byID: make(map[string]*Intent), now: time.Now}
}

// SetClock replaces the clock, for staleness tests.
func (m *MemoryStore) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

func (m *MemoryStore) Reserve(_ context.Context, tenantID, messageKey string, resendFrom int, token string) (Intent, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	key := [2]string{tenantID, messageKey}
	if existing, ok := m.byKey[key]; ok {
		if !reservable(*existing, resendFrom, now) {
			return *existing, false, nil
		}
		existing.Status = StatusPending
		existing.Attempts++
		existing.ReservationToken = token
		existing.UpdatedAt = now
		return *existing, true, nil
	}
	in := &Intent{ID: uuid.NewString(), TenantID: tenantID, MessageKey: messageKey, Status: StatusPending, Attempts: 1,
		ReservationToken: token, CreatedAt: now, UpdatedAt: now}
	m.byKey[key] = in
	m.byID[in.ID] = in
	return *in, true, nil
}

// reservable reports whether Reserve re-reserves existing for resendFrom:
// after a not_delivered outcome, or for an explicit resend naming its current
// attempt when that attempt is finished or stale (pending, unchanged for
// StalePendingAfter).
func reservable(existing Intent, resendFrom int, now time.Time) bool {
	if existing.Status == StatusNotDelivered {
		return true
	}
	if resendFrom <= 0 || existing.Attempts != resendFrom {
		return false
	}
	return existing.Status != StatusPending || now.Sub(existing.UpdatedAt) >= StalePendingAfter
}

func (m *MemoryStore) Record(_ context.Context, intentID string, attempt int, status Status, providerMessageID, detail string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.byID[intentID]
	if !ok {
		return ErrIntentNotFound
	}
	if in.Attempts != attempt || in.Status != StatusPending {
		return ErrStaleRecord
	}
	in.Status = status
	in.ProviderMessageID = providerMessageID
	in.Detail = detail
	in.UpdatedAt = m.now()
	return nil
}

// Get returns the intent for tenant + messageKey.
func (m *MemoryStore) Get(_ context.Context, tenantID, messageKey string) (Intent, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.byKey[[2]string{tenantID, messageKey}]
	if !ok {
		return Intent{}, false, nil
	}
	return *in, true, nil
}

var _ Store = (*MemoryStore)(nil)
