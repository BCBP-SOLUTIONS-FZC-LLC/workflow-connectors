package connectors

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
)

type sendGridAPI interface {
	send(ctx context.Context, m *mail.SGMailV3) (statusCode int, body, messageID string, err error)
}

type sendGridClient struct {
	api sendGridAPI
}

func NewSendGridProvider(_ context.Context, params map[string]any) (SendEmailProviderClient, error) {
	apiKey := stringField(params, "apiKey")
	if apiKey == "" {
		return nil, fmt.Errorf("%w: apiKey is required for sendgrid", ErrValidation)
	}
	return &sendGridClient{api: &realSendGridAPI{client: sendgrid.NewSendClient(apiKey)}}, nil
}

func (c *sendGridClient) Send(ctx context.Context, msg EmailMessage) (string, error) {
	m := mail.NewV3Mail()
	m.SetFrom(mail.NewEmail(msg.SenderName, msg.SenderEmail))
	m.Subject = msg.Subject

	p := mail.NewPersonalization()
	p.AddTos(mail.NewEmail(msg.ReceiverName, msg.ReceiverEmail))
	m.AddPersonalizations(p)

	if msg.TemplateID != "" {
		m.SetTemplateID(msg.TemplateID)
	} else {
		contentType := msg.ContentType
		if contentType == "" {
			contentType = "text/plain"
		}
		m.AddContent(mail.NewContent(contentType, msg.Body))
	}

	for _, a := range msg.Attachments {
		att := mail.NewAttachment()
		att.SetContent(base64.StdEncoding.EncodeToString(a.Content))
		att.SetType(a.ContentType)
		att.SetFilename(a.Filename)
		m.AddAttachment(att)
	}

	statusCode, body, messageID, err := c.api.send(ctx, m)
	if err != nil {
		return "", err
	}
	if statusCode >= 300 {
		return "", fmt.Errorf("sendgrid: status %d: %s", statusCode, body)
	}
	return messageID, nil
}

type realSendGridAPI struct {
	client *sendgrid.Client
}

func (r *realSendGridAPI) send(ctx context.Context, m *mail.SGMailV3) (int, string, string, error) {
	resp, err := r.client.SendWithContext(ctx, m)
	if err != nil {
		return 0, "", "", err
	}
	messageID := ""
	if ids := resp.Headers["X-Message-Id"]; len(ids) > 0 {
		messageID = ids[0]
	}
	return resp.StatusCode, resp.Body, messageID, nil
}

var (
	_ SendEmailProviderClient = (*sendGridClient)(nil)
	_ sendGridAPI             = (*realSendGridAPI)(nil)
)
