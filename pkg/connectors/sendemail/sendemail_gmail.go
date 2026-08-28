package sendemail

import (
	"context"
	"encoding/base64"
	"fmt"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
)

type gmailMessagesAPI interface {
	send(ctx context.Context, raw string) (messageID string, err error)
}

type gmailClient struct {
	api gmailMessagesAPI
}

func NewGmailProvider(ctx context.Context, params map[string]any) (ProviderClient, error) {
	serviceAccountKey := shared.StringField(params, "serviceAccountKey")
	senderEmail := shared.StringField(params, "senderEmail")
	if serviceAccountKey == "" {
		return nil, fmt.Errorf("%w: serviceAccountKey is required for google-workspace", shared.ErrValidation)
	}
	if senderEmail == "" {
		return nil, fmt.Errorf("%w: senderEmail is required for google-workspace", shared.ErrValidation)
	}

	jwtConfig, err := google.JWTConfigFromJSON([]byte(serviceAccountKey), gmail.GmailSendScope)
	if err != nil {
		return nil, fmt.Errorf("%w: google-workspace credentials: %s", shared.ErrUpstream, err)
	}
	jwtConfig.Subject = senderEmail

	svc, err := gmail.NewService(ctx, option.WithHTTPClient(jwtConfig.Client(ctx)))
	if err != nil {
		return nil, fmt.Errorf("%w: google-workspace service: %s", shared.ErrUpstream, err)
	}
	return &gmailClient{api: &realGmailAPI{messages: svc.Users.Messages}}, nil
}

func (c *gmailClient) Send(ctx context.Context, msg EmailMessage) (string, error) {
	raw, err := buildRawMIME(msg)
	if err != nil {
		return "", fmt.Errorf("gmail: %w", err)
	}
	return c.api.send(ctx, base64.URLEncoding.EncodeToString(raw))
}

type realGmailAPI struct {
	messages *gmail.UsersMessagesService
}

func (r *realGmailAPI) send(ctx context.Context, raw string) (string, error) {
	sent, err := r.messages.Send("me", &gmail.Message{Raw: raw}).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	return sent.Id, nil
}

var (
	_ ProviderClient   = (*gmailClient)(nil)
	_ gmailMessagesAPI = (*realGmailAPI)(nil)
)
