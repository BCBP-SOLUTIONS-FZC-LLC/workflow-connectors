package connectors

import (
	"context"
	"fmt"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

type ChatNotifyProviderClient interface {
	CreateChannel(ctx context.Context, name, visibility string) (channelID string, err error)
	InviteToChannel(ctx context.Context, channelNameOrID string, users []string) error
	PostMessage(ctx context.Context, channelOrUser, thread, message string) (messageID string, err error)
}

type chatNotifyConnector struct {
	client ChatNotifyProviderClient
}

func newChatNotify(cfg Config) Connector {
	client := cfg.ChatNotifyClient
	if client == nil {
		client = NewMockChatNotifyClient()
	}
	return chatNotifyConnector{client: client}
}

func (chatNotifyConnector) Type() string { return registry.TypeChatNotify }

func (c chatNotifyConnector) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	switch stringField(input, "method") {
	case "create-channel":
		return c.createChannel(ctx, input)
	case "invite-to-channel":
		return c.inviteToChannel(ctx, input)
	case "post-message":
		return c.postMessage(ctx, input)
	default:
		return nil, fmt.Errorf("%w: method must be one of create-channel/invite-to-channel/post-message", ErrValidation)
	}
}

func (c chatNotifyConnector) createChannel(ctx context.Context, input map[string]any) (map[string]any, error) {
	name := stringField(input, "channelName")
	visibility := stringField(input, "visibility")
	if name == "" || visibility == "" {
		return nil, fmt.Errorf("%w: channelName and visibility are required for create-channel", ErrValidation)
	}
	channelID, err := c.client.CreateChannel(ctx, name, visibility)
	if err != nil {
		return nil, fmt.Errorf("%w: chat-notify create-channel: %s", ErrUpstream, err)
	}
	return map[string]any{"sent": true, "messageId": channelID}, nil
}

func (c chatNotifyConnector) inviteToChannel(ctx context.Context, input map[string]any) (map[string]any, error) {
	target := stringField(input, "channelNameOrId")
	users := stringSliceField(input, "users")
	if target == "" || len(users) == 0 {
		return nil, fmt.Errorf("%w: channelNameOrId and users are required for invite-to-channel", ErrValidation)
	}
	if err := c.client.InviteToChannel(ctx, target, users); err != nil {
		return nil, fmt.Errorf("%w: chat-notify invite-to-channel: %s", ErrUpstream, err)
	}
	return map[string]any{"sent": true, "messageId": ""}, nil
}

func (c chatNotifyConnector) postMessage(ctx context.Context, input map[string]any) (map[string]any, error) {
	target := stringField(input, "channelOrUser")
	message := stringField(input, "message")
	if target == "" || message == "" {
		return nil, fmt.Errorf("%w: channelOrUser and message are required for post-message", ErrValidation)
	}
	messageID, err := c.client.PostMessage(ctx, target, stringField(input, "thread"), message)
	if err != nil {
		return nil, fmt.Errorf("%w: chat-notify post-message: %s", ErrUpstream, err)
	}
	return map[string]any{"sent": true, "messageId": messageID}, nil
}
