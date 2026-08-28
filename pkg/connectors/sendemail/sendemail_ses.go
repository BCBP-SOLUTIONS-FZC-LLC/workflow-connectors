package sendemail

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
)

type sesAPI interface {
	sendEmail(ctx context.Context, in *sesv2.SendEmailInput) (messageID string, err error)
}

type sesClient struct {
	api sesAPI
}

func NewSESProvider(ctx context.Context, params map[string]any) (ProviderClient, error) {
	accessKey := shared.StringField(params, "accessKey")
	secretKey := shared.StringField(params, "secretKey")
	region := shared.StringField(params, "region")
	if accessKey == "" || secretKey == "" || region == "" {
		return nil, fmt.Errorf("%w: accessKey, secretKey, and region are required for aws-ses", shared.ErrValidation)
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: aws-ses config: %s", shared.ErrUpstream, err)
	}
	return &sesClient{api: &realSESAPI{client: sesv2.NewFromConfig(cfg)}}, nil
}

func (c *sesClient) Send(ctx context.Context, msg EmailMessage) (string, error) {
	content := &types.EmailContent{}
	if msg.TemplateID != "" {
		content.Template = &types.Template{TemplateName: &msg.TemplateID}
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

	fromAddress := msg.SenderEmail
	if msg.SenderName != "" {
		fromAddress = fmt.Sprintf("%s <%s>", msg.SenderName, msg.SenderEmail)
	}

	return c.api.sendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: &fromAddress,
		Destination:      &types.Destination{ToAddresses: []string{msg.ReceiverEmail}},
		Content:          content,
	})
}

func sesAttachments(attachments []EmailAttachment) []types.Attachment {
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
	_ ProviderClient = (*sesClient)(nil)
	_ sesAPI         = (*realSESAPI)(nil)
)
