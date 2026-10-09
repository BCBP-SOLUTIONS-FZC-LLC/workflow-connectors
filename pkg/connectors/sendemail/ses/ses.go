package ses

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

type sesAPI interface {
	sendEmail(ctx context.Context, in *sesv2.SendEmailInput) (messageID string, err error)
}

type sesClient struct {
	api sesAPI
	// httpClient is this client's own transport, so Close can drop its idle
	// connections; nil in tests.
	httpClient *http.Client
}

func NewProvider(_ context.Context, params map[string]any) (sendemail.ProviderClient, error) {
	accessKey := shared.StringField(params, "accessKey")
	secretKey := shared.StringField(params, "secretKey")
	region := shared.StringField(params, "region")
	if accessKey == "" || secretKey == "" || region == "" {
		return nil, fmt.Errorf("%w: accessKey, secretKey, and region are required for aws-ses", shared.ErrValidation)
	}

	return newSESClient(accessKey, secretKey, region), nil
}

// newSESClient builds the client; optFns let tests point it at a test server.
func newSESClient(accessKey, secretKey, region string, optFns ...func(*sesv2.Options)) *sesClient {
	// Built from the tenant's values only: config.LoadDefaultConfig would also
	// read the worker's own environment and shared config into this client.
	httpClient := shared.ProviderHTTPClient()
	cfg := aws.Config{
		Region:      region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		HTTPClient:  httpClient,
		// No SDK retries: the standard retryer would resend SendEmail after a
		// throttle, 5xx or timeout — an outcome the provider may already have
		// accepted — and deliver duplicates. A send happens at most once here;
		// any retry is the caller's explicit decision.
		Retryer: func() aws.Retryer { return aws.NopRetryer{} },
	}
	return &sesClient{api: &realSESAPI{client: sesv2.NewFromConfig(cfg, optFns...)}, httpClient: httpClient}
}

func (c *sesClient) Send(ctx context.Context, msg sendemail.EmailMessage) (string, error) {
	content := &types.EmailContent{}
	if msg.TemplateID != "" {
		// The template renders subject and body; attachments still go with
		// it. EmailMessage carries no template data, so none is sent (SES
		// then renders the template's variables empty).
		content.Template = &types.Template{
			TemplateName: &msg.TemplateID,
			Attachments:  sesAttachments(msg.Attachments),
		}
	} else {
		contentType := msg.ContentType
		body := &types.Body{}
		if contentType == "text/html" {
			body.Html = &types.Content{Data: &msg.Body}
		} else {
			body.Text = &types.Content{Data: &msg.Body}
		}
		content.Simple = &types.Message{
			Subject:     &types.Content{Data: &msg.Subject},
			Body:        body,
			Attachments: sesAttachments(msg.Attachments),
		}
	}

	// mail.Address quotes and RFC 2047-encodes the display name, so a comma or
	// a non-ASCII character cannot split or corrupt the From address.
	fromAddress := msg.SenderEmail
	if msg.SenderName != "" {
		fromAddress = (&mail.Address{Name: msg.SenderName, Address: msg.SenderEmail}).String()
	}

	ctx, written := sendemail.TraceWrites(ctx)
	id, err := c.api.sendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: &fromAddress,
		Destination:      &types.Destination{ToAddresses: []string{msg.ReceiverEmail}},
		Content:          content,
	})
	if err != nil {
		return "", classifyError(err, written)
	}
	return id, nil
}

// classifyError gives an SES error its delivery outcome: a response status
// when SES answered, otherwise whether the request had been written
// (sendemail.ClassifyAfterSend).
func classifyError(err error, written *sendemail.WriteTracker) error {
	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) {
		return sendemail.ClassifyStatus(providerName, respErr.HTTPStatusCode(), err)
	}
	return sendemail.ClassifyAfterSend(providerName, err, written)
}

const providerName = "aws-ses"

// maxAttachmentBytes keeps SES at the shared 25 MiB raw attachment limit
// (SES v2 accepts messages up to 40 MB after encoding).
const maxAttachmentBytes int64 = 25 << 20

// MaxAttachmentBytes is the total raw attachment size sent through SES.
func (*sesClient) MaxAttachmentBytes() int64 { return maxAttachmentBytes }

func sesAttachments(attachments []sendemail.EmailAttachment) []types.Attachment {
	if len(attachments) == 0 {
		return nil
	}
	out := make([]types.Attachment, 0, len(attachments))
	for _, a := range attachments {
		out = append(out, types.Attachment{
			FileName:    aws0(a.Filename),
			RawContent:  a.Content,
			ContentType: aws0(a.ContentType),
		})
	}
	return out
}

func aws0(s string) *string { return &s }

type realSESAPI struct {
	client *sesv2.Client
}

func (r *realSESAPI) sendEmail(ctx context.Context, in *sesv2.SendEmailInput) (string, error) {
	out, err := r.client.SendEmail(ctx, in)
	if err != nil {
		return "", err
	}
	if out.MessageId != nil {
		return *out.MessageId, nil
	}
	return "", nil
}

var (
	_ sendemail.ProviderClient    = (*sesClient)(nil)
	_ sendemail.AttachmentLimiter = (*sesClient)(nil)
	_ sesAPI                      = (*realSESAPI)(nil)
)

// Close drops this client's idle connections. The client cache calls it once
// the client is retired and no call is using it.
func (c *sesClient) Close() error {
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	return nil
}
