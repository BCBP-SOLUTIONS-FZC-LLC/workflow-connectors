package chatnotify

import (
	"context"
	"fmt"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

type ProviderClient interface {
	CreateChannel(ctx context.Context, name, visibility string) (channelID string, err error)
	InviteToChannel(ctx context.Context, channelNameOrID string, users []string) error
	PostMessage(ctx context.Context, channelOrUser, thread, message string) (messageID string, err error)
}

type Connector struct {
	client ProviderClient
}

func New(client ProviderClient) Connector {
	if client == nil {
		client = NewMockChatNotifyClient()
	}
	return Connector{client: client}
}

func (Connector) Type() string { return registry.TypeChatNotify }

func (c Connector) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	switch shared.StringField(input, "method") {
	case "create-channel":
		return c.createChannel(ctx, input)
	case "invite-to-channel":
		return c.inviteToChannel(ctx, input)
	case "post-message":
		return c.postMessage(ctx, input)
	default:
		return nil, fmt.Errorf("%w: method must be one of create-channel/invite-to-channel/post-message", shared.ErrValidation)
	}
}

func (c Connector) createChannel(ctx context.Context, input map[string]any) (map[string]any, error) {
	name := shared.StringField(input, "channelName")
	visibility := shared.StringField(input, "visibility")
	if name == "" || visibility == "" {
		return nil, fmt.Errorf("%w: channelName and visibility are required for create-channel", shared.ErrValidation)
	}
	channelID, err := c.client.CreateChannel(ctx, name, visibility)
	if err != nil {
		return nil, fmt.Errorf("%w: chat-notify create-channel: %s", shared.ErrUpstream, err)
	}
	return map[string]any{"sent": true, "messageId": channelID}, nil
}

func (c Connector) inviteToChannel(ctx context.Context, input map[string]any) (map[string]any, error) {
	target := shared.StringField(input, "channelNameOrId")
	users := shared.StringSliceField(input, "users")
	if target == "" || len(users) == 0 {
		return nil, fmt.Errorf("%w: channelNameOrId and users are required for invite-to-channel", shared.ErrValidation)
	}
	if err := c.client.InviteToChannel(ctx, target, users); err != nil {
		return nil, fmt.Errorf("%w: chat-notify invite-to-channel: %s", shared.ErrUpstream, err)
	}
	return map[string]any{"sent": true, "messageId": ""}, nil
}

func (c Connector) postMessage(ctx context.Context, input map[string]any) (map[string]any, error) {
	target := shared.StringField(input, "channelOrUser")
	message := shared.StringField(input, "message")
	if target == "" || message == "" {
		return nil, fmt.Errorf("%w: channelOrUser and message are required for post-message", shared.ErrValidation)
	}
	messageID, err := c.client.PostMessage(ctx, target, shared.StringField(input, "thread"), message)
	if err != nil {
		return nil, fmt.Errorf("%w: chat-notify post-message: %s", shared.ErrUpstream, err)
	}
	return map[string]any{"sent": true, "messageId": messageID}, nil
}
