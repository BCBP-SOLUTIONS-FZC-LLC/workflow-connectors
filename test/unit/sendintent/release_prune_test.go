package sendintent_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
)

// Release marks the intent not_delivered only while it is pending under the
// caller's token; anything else is left alone, and is not an error.
func TestMemory_Release_OnlyOwnPendingReservation(t *testing.T) {
	t.Parallel()
	ctx, store := context.Background(), sendintent.NewMemoryStore()

	_, err := store.Release(ctx, "t", "k", "")
	assert.ErrorIs(t, err, sendintent.ErrNoToken)
	release := func(key, token string) bool {
		t.Helper()
		released, err := store.Release(ctx, "t", key, token)
		require.NoError(t, err)
		return released
	}
	assert.False(t, release("missing", "tok-1"), "no intent: nothing to release")

	in, ok, err := store.Reserve(ctx, "t", "k", 0, "tok-1")
	require.NoError(t, err)
	require.True(t, ok)

	assert.False(t, release("k", "tok-other"))
	got, _, _ := store.Get(ctx, "t", "k")
	assert.Equal(t, sendintent.StatusPending, got.Status, "another caller's reservation is never released")

	assert.True(t, release("k", "tok-1"), "this caller's pending reservation is released")
	got, _, _ = store.Get(ctx, "t", "k")
	assert.Equal(t, sendintent.StatusNotDelivered, got.Status)
	assert.Equal(t, sendintent.ReleasedDetail, got.Detail)
	assert.Equal(t, in.Attempts, got.Attempts)

	// A released intent is re-reserved by the retry.
	again, ok, err := store.Reserve(ctx, "t", "k", 0, "tok-2")
	require.NoError(t, err)
	assert.True(t, ok)
	require.NoError(t, store.Record(ctx, again.ID, again.Attempts, sendintent.StatusAccepted, "m", ""))
	assert.False(t, release("k", "tok-2"))
	got, _, _ = store.Get(ctx, "t", "k")
	assert.Equal(t, sendintent.StatusAccepted, got.Status, "a recorded outcome is never overwritten")
}

// PruneIntents deletes finished intents older than the cutoff, at most batch
// per call, never a pending one; a pruned key is a new message again.
func TestMemory_PruneIntents(t *testing.T) {
	t.Parallel()
	ctx, store := context.Background(), sendintent.NewMemoryStore()
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })

	_, err := store.PruneIntents(ctx, 0, 10)
	assert.ErrorIs(t, err, sendintent.ErrPruneArgs)
	_, err = store.PruneIntents(ctx, time.Hour, 0)
	assert.ErrorIs(t, err, sendintent.ErrPruneArgs)

	for _, key := range []string{"old-1", "old-2", "old-3", "old-pending"} {
		in, _, err := store.Reserve(ctx, "t", key, 0, "tok")
		require.NoError(t, err)
		if key != "old-pending" {
			require.NoError(t, store.Record(ctx, in.ID, in.Attempts, sendintent.StatusAccepted, "m", ""))
		}
	}
	now = now.Add(48 * time.Hour)
	recent, _, err := store.Reserve(ctx, "t", "recent", 0, "tok")
	require.NoError(t, err)
	require.NoError(t, store.Record(ctx, recent.ID, recent.Attempts, sendintent.StatusAccepted, "m", ""))

	deleted, err := store.PruneIntents(ctx, 24*time.Hour, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted, "at most batch per call")
	deleted, err = store.PruneIntents(ctx, 24*time.Hour, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	for key, kept := range map[string]bool{"old-1": false, "old-2": false, "old-3": false, "old-pending": true, "recent": true} {
		_, found, err := store.Get(ctx, "t", key)
		require.NoError(t, err)
		assert.Equal(t, kept, found, key)
	}
	again, ok, err := store.Reserve(ctx, "t", "old-1", 0, "tok")
	require.NoError(t, err)
	assert.True(t, ok, "pruning reopens the duplicate window")
	assert.Equal(t, 1, again.Attempts)
}
