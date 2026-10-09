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
