package connectors

import (
	"context"
	"fmt"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

// SendEmailProviderClient is the minimal surface an email-sending provider
// needs to expose — SDK-agnostic so a real provider client can implement it
// later without touching Execute() itself.
type SendEmailProviderClient interface {
	Send(ctx context.Context, msg EmailMessage) (messageID string, err error)
}

type EmailMessage struct {
	SenderName    string
	SenderEmail   string
	ReceiverName  string
	ReceiverEmail string
	Subject       string
	ContentType   string
	Body          string
	TemplateID    string
	Attachments   []string
}

type sendEmailConnector struct {
	client SendEmailProviderClient
}

func newSendEmail(cfg Config) Connector {
	client := cfg.SendEmailClient
	if client == nil {
		client = NewMockSendEmailClient()
	}
	return sendEmailConnector{client: client}
}

func (sendEmailConnector) Type() string { return registry.TypeSendEmail }

func (s sendEmailConnector) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	senderEmail := stringField(input, "senderEmail")
	receiverEmail := stringField(input, "receiverEmail")
	if senderEmail == "" || receiverEmail == "" {
		return nil, fmt.Errorf("%w: senderEmail and receiverEmail are required", ErrValidation)
	}
	templateID := stringField(input, "templateId")
	body := stringField(input, "body")
	if templateID == "" && body == "" {
		return nil, fmt.Errorf("%w: either templateId or body is required", ErrValidation)
	}

	msg := EmailMessage{
		SenderName:    stringField(input, "senderName"),
		SenderEmail:   senderEmail,
		ReceiverName:  stringField(input, "receiverName"),
		ReceiverEmail: receiverEmail,
		Subject:       stringField(input, "subject"),
		ContentType:   stringField(input, "contentType"),
		Body:          body,
		TemplateID:    templateID,
		Attachments:   stringSliceField(input, "attachments"),
	}

	messageID, err := s.client.Send(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("%w: send-email: %s", ErrUpstream, err)
	}

	return map[string]any{
		"sent":      true,
		"messageId": messageID,
		"sentAt":    time.Now().UTC(),
	}, nil
}
