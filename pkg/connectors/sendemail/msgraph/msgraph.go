package msgraph

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

const graphAPIBaseURL = "https://graph.microsoft.com/v1.0"

type graphAPI interface {
	sendMail(ctx context.Context, senderEmail string, body []byte) error
}

type graphClient struct {
	api graphAPI
}

func NewProvider(_ context.Context, params map[string]any) (sendemail.ProviderClient, error) {
	tenantID := shared.StringField(params, "tenantId")
	clientID := shared.StringField(params, "clientId")
	clientSecret := shared.StringField(params, "clientSecret")
	if tenantID == "" || clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("%w: tenantId, clientId, and clientSecret are required for microsoft-365", shared.ErrValidation)
	}
	// tenantId becomes part of the token URL: allow only a tenant GUID or a
	// domain name, never a path, query or another host.
	if !tenantIDPattern.MatchString(tenantID) {
		return nil, fmt.Errorf("%w: tenantId must be an Entra tenant ID (GUID) or domain name", shared.ErrValidation)
	}

	cc := &clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID),
		Scopes:       []string{"https://graph.microsoft.com/.default"},
	}
	// Token requests use a bounded client of their own. sendMail goes through
	// a provider client that never follows a redirect: the OAuth transport
	// would otherwise re-send the request and its bearer token to whichever
	// host a redirect names.
	tokenCtx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: shared.DefaultHTTPTimeout})
	base := shared.ProviderHTTPClient()
	httpClient := &http.Client{
		Transport:     &oauth2.Transport{Source: cc.TokenSource(tokenCtx), Base: base.Transport},
		Timeout:       base.Timeout,
		CheckRedirect: base.CheckRedirect,
	}
	return &graphClient{api: &realGraphAPI{httpClient: httpClient, baseURL: graphAPIBaseURL, idle: base}}, nil
}

var tenantIDPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// Close drops the client's idle connections when the cache retires it.
func (c *graphClient) Close() error {
	if closer, ok := c.api.(io.Closer); ok {
		return closer.Close()
	}
	return nil
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

func (c *graphClient) Send(ctx context.Context, msg sendemail.EmailMessage) (string, error) {
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

	// The request holds only strings, a bool and slices of those, which
	// json.Marshal always encodes.
	body, _ := json.Marshal(req)
	ctx, written := sendemail.TraceWrites(ctx)
	if err := c.api.sendMail(ctx, msg.SenderEmail, body); err != nil {
		return "", classifyError(err, written)
	}
	return "", nil
}

const providerName = "microsoft-365"

// maxAttachmentBytes is the total raw attachment size an inline sendMail
// accepts: Graph refuses a request over 4 MB, and base64 grows attachments by
// a third (larger files need an upload session, which this client does not
// use).
const maxAttachmentBytes int64 = 3 << 20

// MaxAttachmentBytes is the total raw attachment size an inline sendMail
// accepts.
func (*graphClient) MaxAttachmentBytes() int64 { return maxAttachmentBytes }

// statusError is a non-2xx sendMail response.
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string { return fmt.Sprintf("graph: status %d: %s", e.code, e.body) }

// classifyError gives a sendMail error its delivery outcome. A token request
// the identity provider refused means sendMail was never called; otherwise
// whether the request was written decides (sendemail.ClassifyAfterSend).
func classifyError(err error, written *sendemail.WriteTracker) error {
	var status *statusError
	if errors.As(err, &status) {
		return sendemail.ClassifyStatus(providerName, status.code, err)
	}
	if tokenFailed, ok := classifyTokenError(err); ok {
		return tokenFailed
	}
	return sendemail.ClassifyAfterSend(providerName, err, written)
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
	idle       *http.Client // owns the transport whose idle connections Close drops
}

func (r *realGraphAPI) Close() error {
	if r.idle != nil {
		r.idle.CloseIdleConnections()
	}
	return nil
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return &statusError{code: resp.StatusCode, body: string(respBody)}
	}
	return nil
}

var (
	_ sendemail.ProviderClient    = (*graphClient)(nil)
	_ sendemail.AttachmentLimiter = (*graphClient)(nil)
	_ graphAPI                    = (*realGraphAPI)(nil)
)

// classifyTokenError recognises a failed OAuth token request: the send was
// never made, so the failure is not delivered, classed by the token
// endpoint's status (401/400 permanent, 429/5xx transient). x/oauth2 flattens
// a transport failure into a string ("oauth2: cannot fetch token: …"),
// losing the cause; nothing was sent, so it is transient.
func classifyTokenError(err error) (error, bool) {
	var tokenErr *oauth2.RetrieveError
	if errors.As(err, &tokenErr) {
		if tokenErr.Response != nil && tokenErr.Response.StatusCode != 0 {
			return sendemail.NotDelivered(providerName, tokenErr.Response.StatusCode, err), true
		}
		return sendemail.NotDelivered(providerName, 0, shared.Transient("token request failed", err)), true
	}
	if strings.Contains(err.Error(), "oauth2: cannot fetch token") {
		return sendemail.NotDelivered(providerName, 0, shared.Transient("token request failed", err)), true
	}
	return nil, false
}
