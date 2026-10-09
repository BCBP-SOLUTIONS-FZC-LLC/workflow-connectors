package sendgrid

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"

	"github.com/sendgrid/sendgrid-go/helpers/mail"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

const (
	providerName = "sendgrid"
	sendGridHost = "https://api.sendgrid.com"
	// maxErrorBody caps how much of a response body is read for an error
	// message; a successful send needs only the X-Message-Id header.
	maxErrorBody = 64 << 10
)

// maxAttachmentBytes is SendGrid's documented limit for the attachments of
// one message, as raw bytes (the request itself is limited to 30 MB).
const maxAttachmentBytes int64 = 20 << 20

type sendGridAPI interface {
	send(ctx context.Context, m *mail.SGMailV3) (statusCode int, body, messageID string, err error)
}

type sendGridClient struct {
	api sendGridAPI
}

func NewProvider(_ context.Context, params map[string]any) (sendemail.ProviderClient, error) {
	apiKey := shared.StringField(params, "apiKey")
	if apiKey == "" {
		return nil, fmt.Errorf("%w: apiKey is required for sendgrid", shared.ErrValidation)
	}
	return &sendGridClient{api: newRealSendGridAPI(apiKey, sendGridHost)}, nil
}

func (c *sendGridClient) Send(ctx context.Context, msg sendemail.EmailMessage) (string, error) {
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

	ctx, written := sendemail.TraceWrites(ctx)
	statusCode, body, messageID, err := c.api.send(ctx, m)
	if err != nil {
		return "", sendemail.ClassifyAfterSend(providerName, err, written)
	}
	if statusCode < 200 || statusCode >= 300 {
		return "", sendemail.ClassifyStatus(providerName, statusCode, fmt.Errorf("sendgrid: status %d: %s", statusCode, body))
	}
	return messageID, nil
}

// MaxAttachmentBytes is the total raw attachment size SendGrid accepts.
func (*sendGridClient) MaxAttachmentBytes() int64 { return maxAttachmentBytes }

// Close drops the client's idle connections when the cache retires it.
func (c *sendGridClient) Close() error {
	if closer, ok := c.api.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// realSendGridAPI calls POST /v3/mail/send directly. It does not use
// sendgrid.Client: that client's SendWithContext writes each message body
// onto the shared client (cl.Body = …) before sending, so two concurrent
// sends through one cached client could swap bodies — one recipient getting
// the other's email twice, the other's never sent but reported accepted.
// Every request here is built per call and the client holds no mutable
// state.
type realSendGridAPI struct {
	apiKey     string
	host       string
	httpClient *http.Client
}

func newRealSendGridAPI(apiKey, host string) *realSendGridAPI {
	return &realSendGridAPI{apiKey: apiKey, host: host, httpClient: shared.ProviderHTTPClient()}
}

func (r *realSendGridAPI) send(ctx context.Context, m *mail.SGMailV3) (int, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.host+"/v3/mail/send", bytes.NewReader(mail.GetRequestBody(m)))
	if err != nil {
		return 0, "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return resp.StatusCode, string(body), resp.Header.Get("X-Message-Id"), nil
}

func (r *realSendGridAPI) Close() error {
	r.httpClient.CloseIdleConnections()
	return nil
}

var (
	_ sendemail.ProviderClient    = (*sendGridClient)(nil)
	_ sendemail.AttachmentLimiter = (*sendGridClient)(nil)
	_ sendGridAPI                 = (*realSendGridAPI)(nil)
)
