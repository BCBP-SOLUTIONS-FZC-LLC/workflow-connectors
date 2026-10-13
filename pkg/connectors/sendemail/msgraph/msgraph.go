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
	// Token requests use a client of their own (tokenHTTPClient: bounded, no
	// redirects — a redirect would carry the client secret's request to
	// another host). sendMail goes through a provider client that never
	// follows a redirect either: the OAuth transport would otherwise re-send
	// the request and its bearer token to whichever host a redirect names.
	tokenClient := tokenHTTPClient()
	tokenCtx := context.WithValue(context.Background(), oauth2.HTTPClient, tokenClient)
	base := shared.ProviderHTTPClient()
	httpClient := &http.Client{
		Transport:     &oauth2.Transport{Source: cc.TokenSource(tokenCtx), Base: base.Transport},
		Timeout:       base.Timeout,
		CheckRedirect: base.CheckRedirect,
	}
	return &graphClient{api: &realGraphAPI{httpClient: httpClient, baseURL: graphAPIBaseURL, idle: base, tokenIdle: tokenClient}}, nil
}

// tokenHTTPClient is the client of the OAuth token requests: like
// shared.ProviderHTTPClient (its own transport, HTTP/1.1, no redirects),
// bounded by shared.DefaultHTTPTimeout.
func tokenHTTPClient() *http.Client {
	c := shared.ProviderHTTPClient()
	c.Timeout = shared.DefaultHTTPTimeout
	return c
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

	body := encodeRequest(req)
	if len(body) > maxRequestBytes {
		// Checked before anything is sent: Graph would refuse it.
		return "", sendemail.NotDelivered(providerName, 0, shared.Permanent("request too large",
			fmt.Errorf("sendMail request is %d bytes, over the %d-byte inline limit (body and attachments together)", len(body), maxRequestBytes)))
	}
	ctx, written := sendemail.TraceWrites(ctx)
	if err := c.api.sendMail(ctx, msg.SenderEmail, body); err != nil {
		return "", classifyError(err, written)
	}
	return "", nil
}

// encodeRequest encodes the sendMail request as JSON without HTML escaping:
// json.Marshal would write each <, > and & of an HTML body as a 6-byte
// \u003c escape, inflating the request against maxRequestBytes (Graph
// decodes both forms to the same text). The request holds only strings, a
// bool and slices of those, which always encode; the encoder's trailing
// newline is dropped.
func encodeRequest(req graphSendMailRequest) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(req)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

const providerName = "microsoft-365"

// maxRequestBytes is the largest sendMail request Graph accepts inline (4 MB;
// larger attachments need an upload session, which this client does not
// use).
const maxRequestBytes = 4_000_000

// maxAttachmentBytes is the total raw attachment size an inline sendMail
// accepts: base64 grows attachments by a third (2,900,000 raw bytes encode
// to 3,866,668), which leaves room for the JSON, subject and a modest body
// under maxRequestBytes. Send still checks the encoded request as a whole.
const maxAttachmentBytes int64 = 2_900_000

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
	tokenIdle  *http.Client // the token requests' client, likewise
}

func (r *realGraphAPI) Close() error {
	for _, client := range []*http.Client{r.idle, r.tokenIdle} {
		if client != nil {
			client.CloseIdleConnections()
		}
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
