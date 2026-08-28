package chatnotify_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/chatnotify"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
)

func TestChatNotify_CreateChannel(t *testing.T) {
	t.Parallel()

	client := chatnotify.NewMockChatNotifyClient()
	conn := chatnotify.New(client)

	out, err := conn.Execute(context.Background(), map[string]any{
		"method":      "create-channel",
		"channelName": "general",
		"visibility":  "public",
	})
	require.NoError(t, err)
	assert.Equal(t, true, out["sent"])
	assert.NotEmpty(t, out["messageId"])

	calls := client.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "create-channel", calls[0].Method)
}

func TestChatNotify_PostMessage_MissingFields(t *testing.T) {
	t.Parallel()

	conn := chatnotify.New(nil)

	_, err := conn.Execute(context.Background(), map[string]any{"method": "post-message"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestChatNotify_InviteToChannel(t *testing.T) {
	t.Parallel()

	client := chatnotify.NewMockChatNotifyClient()
	conn := chatnotify.New(client)

	out, err := conn.Execute(context.Background(), map[string]any{
		"method":          "invite-to-channel",
		"channelNameOrId": "general",
		"users":           []any{"user-1", "user-2"},
	})
	require.NoError(t, err)
	assert.Equal(t, true, out["sent"])

	calls := client.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "invite-to-channel", calls[0].Method)
	assert.Equal(t, []string{"general", "user-1", "user-2"}, calls[0].Args)
}

func TestChatNotify_PostMessage(t *testing.T) {
	t.Parallel()

	client := chatnotify.NewMockChatNotifyClient()
	conn := chatnotify.New(client)

	out, err := conn.Execute(context.Background(), map[string]any{
		"method":        "post-message",
		"channelOrUser": "general",
		"message":       "hello",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, out["messageId"])
}

func TestMockChatNotifyClient_SetErrorAndReset(t *testing.T) {
	t.Parallel()

	client := chatnotify.NewMockChatNotifyClient()
	client.SetError(errors.New("boom"))
	_, err := client.CreateChannel(context.Background(), "n", "public")
	require.Error(t, err)

	client.Reset()
	assert.Empty(t, client.Calls())
	_, err = client.CreateChannel(context.Background(), "n", "public")
	require.NoError(t, err)
}

func TestChatNotify_UnknownMethod(t *testing.T) {
	t.Parallel()

	conn := chatnotify.New(nil)

	_, err := conn.Execute(context.Background(), map[string]any{"method": "delete-everything"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}
