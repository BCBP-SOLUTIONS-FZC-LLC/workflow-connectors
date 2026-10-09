package sendemail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

type ProviderClient interface {
	Send(ctx context.Context, msg EmailMessage) (messageID string, err error)
}

// AttachmentLimiter is implemented by a provider client whose API accepts
// less attachment data than shared.MaxAttachmentBytes. The core enforces the
// smaller limit on the total raw (decoded) attachment bytes before sending;
// a larger message is a permanent validation failure, not delivered.
type AttachmentLimiter interface {
	MaxAttachmentBytes() int64
}

type ProviderConstructor func(ctx context.Context, params map[string]any) (ProviderClient, error)

type EmailMessage struct {
	SenderName    string
	SenderEmail   string
	ReceiverName  string
	ReceiverEmail string
	Subject       string
	ContentType   string
	Body          string
	TemplateID    string
	Attachments   []EmailAttachment
}

type EmailAttachment struct {
	Filename    string
	ContentType string
	Content     []byte
}

var emailCredentialFieldNames = []string{
	"apiKey",
	"accessKey", "secretKey", "region",
	"tenantId", "clientId", "clientSecret",
	"serviceAccountKey",
}

var templatelessEmailProviders = map[string]bool{
	"microsoft-365":    true,
	"google-workspace": true,
}

const clientCacheLimit = 256

type Connector struct {
	providers map[string]ProviderConstructor
	docRefs   *docref.Service // document-reference resolution (nil: attachments disabled)
	intents   sendintent.Store

	clients *shared.ClientCache[ProviderClient]
}

// Option configures a Connector.
type Option func(*Connector)

// WithSendIntents enables duplicate-request protection for calls that carry a
// messageKey (package sendintent). It is not idempotency: see the delivery
// semantics in outcome.go.
func WithSendIntents(store sendintent.Store) Option {
	return func(c *Connector) { c.intents = store }
}

func New(providers map[string]ProviderConstructor, docRefs *docref.Service, opts ...Option) *Connector {
	c := &Connector{
		providers: providers,
		docRefs:   docRefs,
		clients:   shared.NewClientCache[ProviderClient](clientCacheLimit),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (*Connector) Type() string { return registry.TypeSendEmail }

func (s *Connector) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	senderEmail := shared.StringField(input, "senderEmail")
	receiverEmail := shared.StringField(input, "receiverEmail")
	if senderEmail == "" || receiverEmail == "" {
		return s.finish(ctx, nil, "", fmt.Errorf("%w: senderEmail and receiverEmail are required", shared.ErrValidation))
	}
	if err := validateAddress("senderEmail", senderEmail); err != nil {
		return s.finish(ctx, nil, "", err)
	}
	if err := validateAddress("receiverEmail", receiverEmail); err != nil {
		return s.finish(ctx, nil, "", err)
	}

	provider := shared.StringField(input, "provider")
	templateID := shared.StringField(input, "templateId")
	body := shared.StringField(input, "body")
	if templateID == "" && body == "" {
		return s.finish(ctx, nil, "", fmt.Errorf("%w: either templateId or body is required", shared.ErrValidation))
	}
	if templatelessEmailProviders[provider] && body == "" {
		return s.finish(ctx, nil, "", fmt.Errorf("%w: body is required for provider %q (no server-side template mechanism)", shared.ErrValidation, provider))
	}

	attachments, err := s.resolveAttachments(ctx, shared.StringSliceField(input, "attachments"))
	if err != nil {
		return s.finish(ctx, nil, "", err)
	}

	// Optional duplicate-request protection: reserve the caller's messageKey
	// before anything is sent.
	intent, duplicate, err := s.reserveIntent(ctx, input)
	if duplicate != nil {
		return duplicateResult(*duplicate, shared.StringField(input, "messageKey"))
	}
	if err != nil {
		return s.finish(ctx, nil, "", err)
	}

	handle, err := s.clientFor(ctx, provider, input)
	if err != nil {
		if !errors.Is(err, shared.ErrValidation) {
			// The client could not be built: nothing was sent.
			err = NotDelivered(provider, 0, err)
		}
		return s.finish(ctx, intent, "", err)
	}
	defer handle.Release()
	client := handle.Client()

	if err := checkProviderAttachmentLimit(provider, client, attachments); err != nil {
		return s.finish(ctx, intent, "", err)
	}

	msg := EmailMessage{
		SenderName:    shared.StringField(input, "senderName"),
		SenderEmail:   senderEmail,
		ReceiverName:  shared.StringField(input, "receiverName"),
		ReceiverEmail: receiverEmail,
		Subject:       shared.StringField(input, "subject"),
		ContentType:   shared.StringField(input, "contentType"),
		Body:          body,
		TemplateID:    templateID,
		Attachments:   attachments,
	}

	if err := ctx.Err(); err != nil {
		// Cancelled before the send started: nothing reached the provider.
		return s.finish(ctx, intent, "", NotDelivered(provider, 0, err))
	}
	messageID, err := client.Send(ctx, msg)
	if err != nil {
		return s.finish(ctx, intent, "", classifySend(provider, err))
	}
	return s.finish(ctx, intent, messageID, nil)
}

// checkProviderAttachmentLimit enforces the provider's own attachment limit
// when it is below shared.MaxAttachmentBytes (already enforced by
// resolveAttachments).
func checkProviderAttachmentLimit(provider string, client ProviderClient, attachments []EmailAttachment) error {
	limiter, ok := client.(AttachmentLimiter)
	if !ok || limiter.MaxAttachmentBytes() <= 0 || limiter.MaxAttachmentBytes() >= shared.MaxAttachmentBytes {
		return nil
	}
	var total int64
	for _, a := range attachments {
		total += int64(len(a.Content))
	}
	if limit := limiter.MaxAttachmentBytes(); total > limit {
		return fmt.Errorf("%w: attachments total %d bytes, over provider %q's %d-byte limit", shared.ErrValidation, total, provider, limit)
	}
	return nil
}

// reserveIntent records a send intent when the call carries a messageKey and
// the connector has a store. A key already reserved is returned as duplicate
// (Execute reports it), unless the call is an explicit resend of the intent's
// current attempt (resend: true, resendAttempt: n).
func (s *Connector) reserveIntent(ctx context.Context, input map[string]any) (intent, duplicate *sendintent.Intent, err error) {
	messageKey := shared.StringField(input, "messageKey")
	if messageKey == "" {
		return nil, nil, nil
	}
	if s.intents == nil {
		return nil, nil, fmt.Errorf("%w: messageKey needs a send-intent store (sendemail.WithSendIntents / connectors.Config.SendIntents)", shared.ErrValidation)
	}
	resendFrom, err := resendAttempt(input)
	if err != nil {
		return nil, nil, err
	}
	tenant, ok := shared.TenantFromContext(ctx)
	if !ok {
		return nil, nil, shared.ErrMissingTenant
	}
	token := uuid.NewString()
	in, reserved, err := s.intents.Reserve(ctx, tenant, messageKey, resendFrom, token)
	if err != nil {
		if owned, ok := s.recoverReservation(ctx, tenant, messageKey, token); ok {
			return &owned, nil, nil
		}
		// No intent of ours could be confirmed, so nothing is sent.
		return nil, nil, NotDelivered("send-intent store", 0, err)
	}
	if !reserved {
		return nil, &in, nil
	}
	return &in, nil, nil
}

// recoverReservation handles a Reserve whose reply was lost: the statement
// may have committed. The intent is read back (detached from the call's
// cancellation, bounded); it is this call's reservation only when it carries
// this call's token and is still pending.
func (s *Connector) recoverReservation(ctx context.Context, tenant, messageKey, token string) (sendintent.Intent, bool) {
	getCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	in, found, err := s.intents.Get(getCtx, tenant, messageKey)
	if err != nil || !found || in.ReservationToken != token || in.Status != sendintent.StatusPending {
		return sendintent.Intent{}, false
	}
	return in, true
}

// resendAttempt reads the explicit-resend inputs: resend: true must name the
// attempt it resends (resendAttempt, the intent's current attempts as shown
// with the duplicate), so a redelivered resend request cannot send a third
// time. 0 is no resend.
func resendAttempt(input map[string]any) (int, error) {
	attempt, present, err := intField(input, "resendAttempt")
	if err != nil {
		return 0, err
	}
	switch resend := shared.BoolField(input, "resend"); {
	case resend && attempt < 1:
		return 0, fmt.Errorf("%w: resend needs resendAttempt: the send intent's current attempts (shown with the duplicate)", shared.ErrValidation)
	case !resend && present:
		return 0, fmt.Errorf("%w: resendAttempt is only valid with resend: true", shared.ErrValidation)
	case resend:
		return attempt, nil
	default:
		return 0, nil
	}
}

// intField reads an optional integer input, as a Go integer or a JSON number
// with no fraction.
func intField(input map[string]any, key string) (value int, present bool, err error) {
	raw, ok := input[key]
	if !ok || raw == nil {
		return 0, false, nil
	}
	switch v := raw.(type) {
	case int:
		return v, true, nil
	case int64:
		return int(v), true, nil
	case float64:
		if v == float64(int(v)) {
			return int(v), true, nil
		}
	case json.Number:
		if n, convErr := v.Int64(); convErr == nil {
			return int(n), true, nil
		}
	}
	return 0, true, fmt.Errorf("%w: %s must be an integer", shared.ErrValidation, key)
}

// duplicateResult reports a request whose messageKey already has an intent
// that was not re-reserved. Nothing was sent by this request. A duplicate of
// an accepted intent succeeds (the email was sent: a redelivered job after a
// crash before its acknowledgement is then idempotent); any other is a
// *sendintent.DuplicateRequestError. Both carry the intent's state.
func duplicateResult(in sendintent.Intent, messageKey string) (map[string]any, error) {
	out := map[string]any{
		"sent":            false,
		"duplicate":       true,
		"sendIntentId":    in.ID,
		"status":          string(in.Status),
		"attempts":        in.Attempts,
		"deliveryOutcome": string(intentOutcome(in.Status)),
	}
	if in.Status == sendintent.StatusAccepted {
		out["providerMessageId"] = in.ProviderMessageID
		return out, nil
	}
	return out, &sendintent.DuplicateRequestError{IntentID: in.ID, MessageKey: messageKey, Status: in.Status, Attempts: in.Attempts}
}

// intentOutcome is the delivery outcome an intent's status reports; a
// pending intent (in flight, or its worker stopped mid-send) is unknown.
func intentOutcome(status sendintent.Status) Outcome {
	switch status {
	case sendintent.StatusAccepted:
		return OutcomeAccepted
	case sendintent.StatusNotDelivered:
		return OutcomeNotDelivered
	default:
		return OutcomeUnknown
	}
}

// recordTimeout bounds storing a send's outcome on its intent, and reading
// back an intent whose reservation reply was lost.
const recordTimeout = 10 * time.Second

// finish builds the output, records the outcome on the intent, and returns
// sendErr. Every result carries deliveryOutcome; an error that is not a
// delivery outcome (invalid input, missing call context) means nothing was
// sent: not_delivered. A failed send still returns the output map, so the
// caller can record the delivery outcome with the failure. Recording the
// intent never turns a successful send into an error: a false failure invites
// a resend, which would duplicate the email.
func (s *Connector) finish(ctx context.Context, intent *sendintent.Intent, messageID string, sendErr error) (map[string]any, error) {
	out := intentOutput(intent, map[string]any{"sent": sendErr == nil})
	outcome := OutcomeOf(sendErr)
	if outcome == "" {
		outcome = OutcomeNotDelivered
	}
	out["deliveryOutcome"] = string(outcome)
	if sendErr == nil {
		out["messageId"] = messageID
		out["sentAt"] = time.Now().UTC()
	}

	if intent != nil && s.intents != nil {
		status := sendintent.Status(outcome)
		detail := ""
		if sendErr != nil {
			detail = sendErr.Error()
		}
		// Detached from the call's cancellation (the outcome must be kept even
		// if the caller gave up) but bounded: a stuck database must not hold
		// the call after the provider already accepted the message.
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
		err := s.intents.Record(recordCtx, intent.ID, intent.Attempts, status, messageID, detail)
		cancel()
		if err != nil {
			out["sendIntentWarning"] = fmt.Sprintf("outcome not recorded on send intent: %v", err)
			// The intent stays pending: a retry with this messageKey would be
			// refused as a duplicate, so the failure must not invite one.
			var sendError *SendError
			if errors.As(sendErr, &sendError) {
				unrecorded := *sendError
				unrecorded.IntentUnrecorded = true
				sendErr = &unrecorded
			}
		}
	}
	return out, sendErr
}

func intentOutput(intent *sendintent.Intent, out map[string]any) map[string]any {
	if intent != nil {
		out["sendIntentId"] = intent.ID
	}
	return out
}

// clientFor returns a handle on the provider client for this call's
// credentials and sender. The caller must Release it when the call is
// finished; a reset of the cache never closes a client while a handle is out.
func (s *Connector) clientFor(ctx context.Context, provider string, input map[string]any) (*shared.ClientHandle[ProviderClient], error) {
	if provider == "" {
		return nil, fmt.Errorf("%w: provider is required", shared.ErrValidation)
	}

	ctor, ok := s.providers[provider]
	if !ok {
		return nil, fmt.Errorf("%w: provider %q is not configured", shared.ErrValidation, provider)
	}

	return s.clients.Acquire(emailCacheKey(provider, input), func() (ProviderClient, error) {
		client, err := ctor(ctx, input)
		if err != nil {
			// Not shared.Classify: a send-email failure must never carry the
			// retryable ErrUpstream class. Execute reports it as ErrNotDelivered.
			return nil, fmt.Errorf("build %s client: %w", provider, err)
		}
		return client, nil
	})
}

// ResetClients retires every cached provider client, for example after
// credentials are rotated. Clients in use by an in-flight call are closed
// when that call finishes; new calls build fresh clients.
func (s *Connector) ResetClients() { s.clients.Reset() }

func emailCacheKey(provider string, input map[string]any) string {
	h := sha256.New()
	h.Write([]byte(provider))
	h.Write([]byte{0})
	h.Write([]byte(shared.StringField(input, "senderEmail")))
	names := append([]string(nil), emailCredentialFieldNames...)
	sort.Strings(names)
	for _, name := range names {
		h.Write([]byte{0})
		h.Write([]byte(shared.StringField(input, name)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Connector) resolveAttachments(ctx context.Context, refs []string) ([]EmailAttachment, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if s.docRefs == nil {
		return nil, fmt.Errorf("%w: attachments need a document-ref service (connectors.Config.DocRefs)", shared.ErrValidation)
	}
	tenant, ok := shared.TenantFromContext(ctx)
	if !ok {
		return nil, shared.ErrMissingTenant
	}
	out := make([]EmailAttachment, 0, len(refs))
	var total int64
	for i, ref := range refs {
		// Fetched from S3 and verified (size, SHA-256) in full before
		// anything is sent; never larger than the remaining attachment budget.
		stored, content, err := s.docRefs.Read(ctx, tenant, ref, shared.MaxAttachmentBytes-total)
		if errors.Is(err, docref.ErrTooLarge) {
			return nil, fmt.Errorf("%w: attachments exceed the %d-byte total limit: %w", shared.ErrValidation, shared.MaxAttachmentBytes, err)
		}
		if docref.IsResolutionFailure(err) {
			return nil, fmt.Errorf("%w: attachments[%d]: %w", shared.ErrValidation, i, err)
		}
		if err != nil {
			// Valkey or S3 unavailable: resolved before anything is sent, so a
			// retry cannot duplicate the email. A store that reports itself
			// temporarily unavailable (full, resharding, AOF not confirmed)
			// is transient; network failures are classified by their cause.
			if errors.Is(err, docref.ErrUnavailable) {
				err = shared.Transient("document-ref store unavailable", err)
			}
			return nil, NotDelivered("document-ref store", 0, err)
		}
		contentType := stored.ContentType
		total += int64(len(content))
		// Read is given the remaining budget, so this cannot trigger today; it
		// stays as the limit's own guard rather than relying on docref's.
		if total > shared.MaxAttachmentBytes {
			return nil, fmt.Errorf("%w: attachments exceed the %d-byte total limit", shared.ErrValidation, shared.MaxAttachmentBytes)
		}
		out = append(out, EmailAttachment{
			Filename:    attachmentFilename(i, contentType),
			ContentType: contentType,
			Content:     content,
		})
	}
	return out, nil
}

var attachmentExtensions = map[string]string{
	"application/pdf": ".pdf",
	"image/png":       ".png",
	"image/jpeg":      ".jpg",
	"text/plain":      ".txt",
	"text/csv":        ".csv",
}

func attachmentFilename(index int, contentType string) string {
	return fmt.Sprintf("attachment-%d%s", index+1, attachmentExtensions[contentType])
}

// validateAddress requires exactly one bare address. A value such as
// "a@x.com, b@y.com" would otherwise add recipients on a provider that takes a
// raw To header (Gmail), and "Name <a@x.com>" belongs in the name fields.
func validateAddress(field, value string) error {
	addr, err := mail.ParseAddress(value)
	if err != nil || addr.Name != "" || addr.Address != value {
		return fmt.Errorf("%w: %s must be a single email address", shared.ErrValidation, field)
	}
	return nil
}
