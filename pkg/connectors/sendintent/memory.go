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

// Release marks the intent not_delivered when it is still pending under
// token (see Releaser).
func (m *MemoryStore) Release(_ context.Context, tenantID, messageKey, token string) (bool, error) {
	if token == "" {
		return false, ErrNoToken
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.byKey[[2]string{tenantID, messageKey}]
	if !ok || in.ReservationToken != token || in.Status != StatusPending {
		return false, nil
	}
	in.Status = StatusNotDelivered
	in.Detail = ReleasedDetail
	in.UpdatedAt = m.now()
	return true, nil
}

// PruneIntents deletes finished (not pending) intents last updated more than
// olderThan ago, at most batch, and returns how many it deleted. See sqlstore.PruneIntents:
// a pruned messageKey is accepted again as a new message.
func (m *MemoryStore) PruneIntents(_ context.Context, olderThan time.Duration, batch int) (int64, error) {
	if olderThan <= 0 || batch <= 0 {
		return 0, ErrPruneArgs
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := m.now().Add(-olderThan)
	var deleted int64
	for key, in := range m.byKey {
		if deleted == int64(batch) {
			break
		}
		if in.Status != StatusPending && in.UpdatedAt.Before(cutoff) {
			delete(m.byKey, key)
			delete(m.byID, in.ID)
			deleted++
		}
	}
	return deleted, nil
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

var (
	_ Store    = (*MemoryStore)(nil)
	_ Releaser = (*MemoryStore)(nil)
)
