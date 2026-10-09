package gmail

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

type gmailMessagesAPI interface {
	send(ctx context.Context, raw string) (messageID string, err error)
}

type gmailClient struct {
	api gmailMessagesAPI
	// httpClient owns the transport under the OAuth layer, so Close can drop
	// its idle connections; nil in tests.
	httpClient *http.Client
}

func NewProvider(ctx context.Context, params map[string]any) (sendemail.ProviderClient, error) {
	serviceAccountKey := shared.StringField(params, "serviceAccountKey")
	senderEmail := shared.StringField(params, "senderEmail")
	if serviceAccountKey == "" {
		return nil, fmt.Errorf("%w: serviceAccountKey is required for google-workspace", shared.ErrValidation)
	}
	if senderEmail == "" {
		return nil, fmt.Errorf("%w: senderEmail is required for google-workspace", shared.ErrValidation)
	}

	if err := shared.ValidateGoogleServiceAccountKey([]byte(serviceAccountKey), "serviceAccountKey"); err != nil {
		return nil, err
	}
	jwtConfig, err := google.JWTConfigFromJSON([]byte(serviceAccountKey), gmail.GmailSendScope)
	if err != nil {
		return nil, fmt.Errorf("%w: google-workspace credentials: %w", shared.ErrValidation, err)
	}
	jwtConfig.Subject = senderEmail

	// The token source outlives this call (clients are cached), so it gets a
	// background context whose token requests use a bounded client. Sends go
	// through a provider client that never follows a redirect: the OAuth
	// transport would otherwise re-send the message and its bearer token to
	// whichever host a redirect names.
	tokenCtx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: shared.DefaultHTTPTimeout})
	base := shared.ProviderHTTPClient()
	httpClient := &http.Client{
		Transport:     &oauth2.Transport{Source: jwtConfig.TokenSource(tokenCtx), Base: base.Transport},
		Timeout:       base.Timeout,
		CheckRedirect: base.CheckRedirect,
	}

	svc, err := gmail.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, shared.Classify("google-workspace service", err)
	}
	return &gmailClient{api: &realGmailAPI{messages: svc.Users.Messages}, httpClient: base}, nil
}

func (c *gmailClient) Send(ctx context.Context, msg sendemail.EmailMessage) (string, error) {
	raw := buildRawMIME(msg)
	ctx, written := sendemail.TraceWrites(ctx)
	id, err := c.api.send(ctx, base64.URLEncoding.EncodeToString(raw))
	if err != nil {
		return "", classifyError(err, written)
	}
	return id, nil
}

const providerName = "google-workspace"

// classifyError gives a Gmail send error its delivery outcome: the API's
// status when it answered; a refused token request means send was never
// called; otherwise whether the request was written decides
// (sendemail.ClassifyAfterSend).
func classifyError(err error, written *sendemail.WriteTracker) error {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		// Gmail throttles with 403 and a rate-limit reason, not 429: the
		// message was not accepted and the same send may succeed later.
		if reason, ok := rateLimitReason(apiErr); ok && apiErr.Code == http.StatusForbidden {
			return sendemail.NotDelivered(providerName, apiErr.Code, shared.Transient("api "+reason, err))
		}
		return sendemail.ClassifyStatus(providerName, apiErr.Code, err)
	}
	if tokenFailed, ok := classifyTokenError(err); ok {
		return tokenFailed
	}
	return sendemail.ClassifyAfterSend(providerName, err, written)
}

// rateLimitReason returns the Gmail error reason that marks a throttled
// request (https://developers.google.com/gmail/api/guides/handle-errors).
func rateLimitReason(apiErr *googleapi.Error) (string, bool) {
	for _, item := range apiErr.Errors {
		switch item.Reason {
		case "rateLimitExceeded", "userRateLimitExceeded":
			return item.Reason, true
		}
	}
	return "", false
}

// maxAttachmentBytes is Gmail's 25 MB message limit, applied to the raw
// attachment bytes like shared.MaxAttachmentBytes.
const maxAttachmentBytes int64 = 25 << 20

// MaxAttachmentBytes is the total raw attachment size Gmail accepts.
func (*gmailClient) MaxAttachmentBytes() int64 { return maxAttachmentBytes }

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
	_ sendemail.ProviderClient    = (*gmailClient)(nil)
	_ sendemail.AttachmentLimiter = (*gmailClient)(nil)
	_ gmailMessagesAPI            = (*realGmailAPI)(nil)
)

// Close drops this client's idle connections. The client cache calls it once
// the client is retired and no call is using it.
func (c *gmailClient) Close() error {
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	return nil
}

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
