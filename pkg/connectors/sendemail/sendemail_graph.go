package sendemail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"golang.org/x/oauth2/clientcredentials"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
)

const graphAPIBaseURL = "https://graph.microsoft.com/v1.0"

type graphAPI interface {
	sendMail(ctx context.Context, senderEmail string, body []byte) error
}

type graphClient struct {
	api graphAPI
}

func NewGraphSendMailProvider(_ context.Context, params map[string]any) (ProviderClient, error) {
	tenantID := shared.StringField(params, "tenantId")
	clientID := shared.StringField(params, "clientId")
	clientSecret := shared.StringField(params, "clientSecret")
	if tenantID == "" || clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("%w: tenantId, clientId, and clientSecret are required for microsoft-365", shared.ErrValidation)
	}

	cc := &clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID),
		Scopes:       []string{"https://graph.microsoft.com/.default"},
	}
	return &graphClient{api: &realGraphAPI{httpClient: cc.Client(context.Background()), baseURL: graphAPIBaseURL}}, nil
}

type graphAttachment struct {
	ODataType    string `json:"@odata.type"`
	Name         string `json:"name"`
	ContentType  string `json:"contentType,omitempty"`
	ContentBytes string `json:"contentBytes"`
}

type graphRecipient struct {
	EmailAddress graphEmailAddress `json:"emailAddress"`
}

type graphEmailAddress struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

type graphSendMailRequest struct {
	Message struct {
		Subject      string            `json:"subject"`
		Body         graphItemBody     `json:"body"`
		ToRecipients []graphRecipient  `json:"toRecipients"`
		Attachments  []graphAttachment `json:"attachments,omitempty"`
	} `json:"message"`
	SaveToSentItems bool `json:"saveToSentItems"`
}

type graphItemBody struct {
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

func (c *graphClient) Send(ctx context.Context, msg EmailMessage) (string, error) {
	req := graphSendMailRequest{}
	req.Message.Subject = msg.Subject
	req.Message.Body = graphItemBody{ContentType: graphContentType(msg.ContentType), Content: msg.Body}
	req.Message.ToRecipients = []graphRecipient{{EmailAddress: graphEmailAddress{Name: msg.ReceiverName, Address: msg.ReceiverEmail}}}
	for _, a := range msg.Attachments {
		req.Message.Attachments = append(req.Message.Attachments, graphAttachment{
			ODataType:    "#microsoft.graph.fileAttachment",
			Name:         a.Filename,
			ContentType:  a.ContentType,
			ContentBytes: base64.StdEncoding.EncodeToString(a.Content),
		})
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("graph: encode request: %w", err)
	}
	if err := c.api.sendMail(ctx, msg.SenderEmail, body); err != nil {
		return "", err
	}
	return "", nil
}

func graphContentType(ct string) string {
	if ct == "text/html" {
		return "HTML"
	}
	return "Text"
}

type realGraphAPI struct {
	httpClient *http.Client
	baseURL    string
}

func (r *realGraphAPI) sendMail(ctx context.Context, senderEmail string, body []byte) error {
	endpoint := fmt.Sprintf("%s/users/%s/sendMail", r.baseURL, url.PathEscape(senderEmail))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("graph: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

var (
	_ ProviderClient = (*graphClient)(nil)
	_ graphAPI       = (*realGraphAPI)(nil)
)
