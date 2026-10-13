package documents_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
)

func TestMemory_SetClock_DrivesLeaseExpiry(t *testing.T) {
	t.Parallel()

	ctx, store := context.Background(), documents.NewMemoryStore()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })

	id := documents.Identity{TenantID: "t", Provider: "google-drive", Container: "folder", Filename: "a.pdf"}
	doc, claimed, err := store.Claim(ctx, id, "attempt-1", time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, now.Add(time.Minute), doc.LeaseExpiresAt)

	_, claimed, err = store.Claim(ctx, id, "attempt-2", time.Minute)
	require.NoError(t, err)
	assert.False(t, claimed, "lease still held")

	now = now.Add(time.Minute)
	store.SetClock(func() time.Time { return now })
	doc, claimed, err = store.Claim(ctx, id, "attempt-2", time.Minute)
	require.NoError(t, err)
	assert.True(t, claimed, "expired lease is taken over")
	assert.Equal(t, "attempt-2", doc.Owner)
}

func TestMemory_ClaimExisting_TakesOverAnIdleRow(t *testing.T) {
	t.Parallel()
	ctx, store := context.Background(), documents.NewMemoryStore()
	id := documents.Identity{TenantID: "t", Provider: "google-drive", Container: "folder", Filename: "b.pdf"}

	_, _, err := store.ClaimExisting(ctx, id, "a", time.Minute)
	require.ErrorIs(t, err, documents.ErrNotFound)

	doc, _, err := store.Claim(ctx, id, "a", time.Minute)
	require.NoError(t, err)
	_, err = store.Complete(ctx, doc.ID, "a", documents.Result{ObjectID: "f"})
	require.NoError(t, err)

	taken, claimed, err := store.ClaimExisting(ctx, id, "b", time.Minute)
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.Equal(t, doc.ID, taken.ID)
	assert.Equal(t, "b", taken.Owner)
}

// Every owner transition is conditional on the owner and a live lease, as in
// sqlstore: another attempt, an unknown row and an expired lease all get
// ErrOwnershipLost and change nothing.
func TestMemory_TransitionsNeedTheOwnerAndALiveLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	transitions := map[string]func(s *documents.MemoryStore, docID, attempt string) error{
		"advance": func(s *documents.MemoryStore, docID, attempt string) error {
			_, err := s.Advance(ctx, docID, attempt, documents.StateUploading)
			return err
		},
		"complete": func(s *documents.MemoryStore, docID, attempt string) error {
			_, err := s.Complete(ctx, docID, attempt, documents.Result{ObjectID: "f"})
			return err
		},
		"fail": func(s *documents.MemoryStore, docID, attempt string) error {
			_, err := s.Fail(ctx, docID, attempt, "boom")
			return err
		},
		"remove": func(s *documents.MemoryStore, docID, attempt string) error {
			return s.Remove(ctx, docID, attempt)
		},
	}
	for name, transition := range transitions {
		store := documents.NewMemoryStore()
		store.SetClock(func() time.Time { return now })
		id := documents.Identity{TenantID: "t", Provider: "google-drive", Container: "folder", Filename: name}
		doc, claimed, err := store.Claim(ctx, id, "owner", time.Minute)
		require.NoError(t, err)
		require.True(t, claimed)

		assert.ErrorIs(t, transition(store, doc.ID, "intruder"), documents.ErrOwnershipLost, "%s: another attempt", name)
		assert.ErrorIs(t, transition(store, "no-such-doc", "owner"), documents.ErrOwnershipLost, "%s: unknown row", name)

		store.SetClock(func() time.Time { return now.Add(time.Minute) })
		assert.ErrorIs(t, transition(store, doc.ID, "owner"), documents.ErrOwnershipLost, "%s: lease expired", name)

		got, found, err := store.Get(ctx, id)
		require.NoError(t, err)
		require.True(t, found, name)
		assert.Equal(t, documents.StatePendingUpload, got.State, "%s: unchanged", name)
		assert.Equal(t, "owner", got.Owner, name)
	}
}
