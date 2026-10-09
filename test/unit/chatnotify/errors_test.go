package chatnotify_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/chatnotify"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func TestChatNotify_MissingFields_AreValidationErrors(t *testing.T) {
	t.Parallel()

	for name, input := range map[string]map[string]any{
		"create-channel":    {"method": "create-channel", "channelName": "general"},
		"invite-to-channel": {"method": "invite-to-channel", "channelNameOrId": "general"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := chatnotify.NewMockChatNotifyClient()
			_, err := chatnotify.New(client).Execute(context.Background(), input)
			require.Error(t, err)
			assert.ErrorIs(t, err, shared.ErrValidation)
			assert.Empty(t, client.Calls(), "no provider call on invalid input")
		})
	}
}

func TestChatNotify_ProviderError_IsUpstream(t *testing.T) {
	t.Parallel()

	boom := errors.New("provider down")
	for name, input := range map[string]map[string]any{
		"create-channel":    {"method": "create-channel", "channelName": "general", "visibility": "public"},
		"invite-to-channel": {"method": "invite-to-channel", "channelNameOrId": "general", "users": []any{"u1"}},
		"post-message":      {"method": "post-message", "channelOrUser": "general", "message": "hi"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := chatnotify.NewMockChatNotifyClient()
			client.SetError(boom)
			_, err := chatnotify.New(client).Execute(context.Background(), input)
			require.Error(t, err)
			assert.ErrorIs(t, err, shared.ErrUpstream)
			assert.ErrorIs(t, err, boom)
		})
	}
}
