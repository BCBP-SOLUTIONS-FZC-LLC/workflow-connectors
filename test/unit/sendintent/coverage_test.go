package sendintent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
)

func TestMemory_Get(t *testing.T) {
	t.Parallel()
	ctx, store := context.Background(), sendintent.NewMemoryStore()

	_, ok, _ := store.Get(context.Background(), "t", "missing")
	assert.False(t, ok)

	reserved, ok, err := store.Reserve(ctx, "t", "k", 0, "")
	require.NoError(t, err)
	require.True(t, ok)
	got, ok, _ := store.Get(context.Background(), "t", "k")
	require.True(t, ok)
	assert.Equal(t, reserved.ID, got.ID)
}

func TestDuplicateRequestError_Message(t *testing.T) {
	t.Parallel()
	err := &sendintent.DuplicateRequestError{IntentID: "intent-1", MessageKey: "k", Status: sendintent.StatusAccepted}
	assert.Contains(t, err.Error(), `messageKey "k" already has send intent intent-1`)
	assert.Contains(t, err.Error(), string(sendintent.StatusAccepted))
	assert.True(t, errors.Is(err, sendintent.ErrDuplicateRequest))
}
