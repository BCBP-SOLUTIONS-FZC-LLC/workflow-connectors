package sendintent_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
)

// The in-memory store follows the same contract as the PostgreSQL store
// (test/postgres/sendintent runs both with the integration tag); these run
// without Docker.

func TestMemory_RecordOnlyForTheCurrentPendingAttempt(t *testing.T) {
	t.Parallel()
	ctx, store := context.Background(), sendintent.NewMemoryStore()

	first, ok, err := store.Reserve(ctx, "t", "k", 0, "")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, store.Record(ctx, first.ID, first.Attempts, sendintent.StatusUnknown, "", ""))
	second, ok, err := store.Reserve(ctx, "t", "k", first.Attempts, "")
	require.NoError(t, err)
	require.True(t, ok)

	assert.ErrorIs(t, store.Record(ctx, first.ID, first.Attempts, sendintent.StatusNotDelivered, "", ""), sendintent.ErrStaleRecord)
	require.NoError(t, store.Record(ctx, second.ID, second.Attempts, sendintent.StatusAccepted, "m", ""))
	assert.ErrorIs(t, store.Record(ctx, second.ID, second.Attempts, sendintent.StatusUnknown, "", ""), sendintent.ErrStaleRecord, "recorded once")
	assert.ErrorIs(t, store.Record(ctx, "no-such-id", 1, sendintent.StatusAccepted, "", ""), sendintent.ErrIntentNotFound)

	got, found, _ := store.Get(context.Background(), "t", "k")
	require.True(t, found)
	assert.Equal(t, sendintent.StatusAccepted, got.Status)
}

// A pending key (in flight, or its worker stopped mid-send) refuses every
// later request, an explicit resend included: a resend names a finished
// attempt.
func TestMemory_PendingKeyRefusesRetries(t *testing.T) {
	t.Parallel()
	ctx, store := context.Background(), sendintent.NewMemoryStore()
	_, ok, err := store.Reserve(ctx, "t", "k", 0, "")
	require.NoError(t, err)
	require.True(t, ok)

	_, ok, err = store.Reserve(ctx, "t", "k", 0, "")
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = store.Reserve(ctx, "t", "k", 1, "")
	require.NoError(t, err)
	assert.False(t, ok, "a pending attempt is never resent")
}
