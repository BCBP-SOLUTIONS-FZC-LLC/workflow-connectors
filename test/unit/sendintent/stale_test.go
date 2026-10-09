package sendintent_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
)

// An intent left pending by a worker that stopped mid-send may be resent once
// it is stale, naming its attempt; a fresh pending intent may not, and a
// redelivered resend of the stale one is refused.
func TestMemory_ResendOfStalePending(t *testing.T) {
	t.Parallel()
	ctx, store := context.Background(), sendintent.NewMemoryStore()
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })

	first, ok, err := store.Reserve(ctx, "t", "k", 0, "tok-1")
	require.NoError(t, err)
	require.True(t, ok)

	now = now.Add(sendintent.StalePendingAfter - time.Second)
	_, ok, err = store.Reserve(ctx, "t", "k", first.Attempts, "tok-2")
	require.NoError(t, err)
	assert.False(t, ok, "a pending attempt may still be sending")

	now = now.Add(time.Second)
	again, ok, err := store.Reserve(ctx, "t", "k", first.Attempts, "tok-3")
	require.NoError(t, err)
	require.True(t, ok, "a stale pending attempt may be resent")
	assert.Equal(t, 2, again.Attempts)
	assert.Equal(t, "tok-3", again.ReservationToken)

	_, ok, err = store.Reserve(ctx, "t", "k", first.Attempts, "tok-4")
	require.NoError(t, err)
	assert.False(t, ok, "the redelivered resend names a superseded attempt")

	_, ok, err = store.Reserve(ctx, "t", "k", 0, "tok-5")
	require.NoError(t, err)
	assert.False(t, ok, "without resend a stale pending intent is never re-reserved")
}
