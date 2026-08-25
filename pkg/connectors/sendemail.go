package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

// SendEmailProviderClient is the minimal surface an email-sending provider
// needs to expose — SDK-agnostic so a real provider client can implement it
// later without touching Execute() itself.
type SendEmailProviderClient interface {
	Send(ctx context.Context, msg EmailMessage) (messageID string, err error)
}

type SendEmailProviderConstructor func(ctx context.Context, params map[string]any) (SendEmailProviderClient, error)

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

type sendEmailConnector struct {
	providers map[string]SendEmailProviderConstructor
	docRefs   *docRefStore

	cacheMu sync.Mutex
	cache   map[string]SendEmailProviderClient
}

func newSendEmail(cfg Config, docRefs *docRefStore) Connector {
	return &sendEmailConnector{
		providers: cfg.SendEmailProviders,
		docRefs:   docRefs,
		cache:     make(map[string]SendEmailProviderClient),
	}
}

func (*sendEmailConnector) Type() string { return registry.TypeSendEmail }

func (s *sendEmailConnector) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	senderEmail := stringField(input, "senderEmail")
	receiverEmail := stringField(input, "receiverEmail")
	if senderEmail == "" || receiverEmail == "" {
		return nil, fmt.Errorf("%w: senderEmail and receiverEmail are required", ErrValidation)
	}

	provider := stringField(input, "provider")
	templateID := stringField(input, "templateId")
	body := stringField(input, "body")
	if templateID == "" && body == "" {
		return nil, fmt.Errorf("%w: either templateId or body is required", ErrValidation)
	}
	if templatelessEmailProviders[provider] && body == "" {
		return nil, fmt.Errorf("%w: body is required for provider %q (no server-side template mechanism)", ErrValidation, provider)
	}

	client, err := s.clientFor(ctx, provider, input)
	if err != nil {
		return nil, err
	}

	attachments, err := s.resolveAttachments(stringSliceField(input, "attachments"))
	if err != nil {
		return nil, err
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
		Attachments:   attachments,
	}

	messageID, err := client.Send(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("%w: send-email: %s", ErrUpstream, err)
	}

	return map[string]any{
		"sent":      true,
		"messageId": messageID,
		"sentAt":    time.Now().UTC(),
	}, nil
}

func (s *sendEmailConnector) clientFor(ctx context.Context, provider string, input map[string]any) (SendEmailProviderClient, error) {
	if provider == "" {
		return nil, fmt.Errorf("%w: provider is required", ErrValidation)
	}

	ctor, ok := s.providers[provider]
	if !ok {
		return nil, fmt.Errorf("%w: provider %q is not configured", ErrValidation, provider)
	}

	key := emailCacheKey(provider, input)
	s.cacheMu.Lock()
	if cached, ok := s.cache[key]; ok {
		s.cacheMu.Unlock()
		return cached, nil
	}
	s.cacheMu.Unlock()

	client, err := ctor(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("%w: build %s client: %s", ErrUpstream, provider, err)
	}

	s.cacheMu.Lock()
	if len(s.cache) >= clientCacheLimit {
		s.cache = make(map[string]SendEmailProviderClient)
	}
	s.cache[key] = client
	s.cacheMu.Unlock()
	return client, nil
}

func emailCacheKey(provider string, input map[string]any) string {
	h := sha256.New()
	h.Write([]byte(provider))
	names := append([]string(nil), emailCredentialFieldNames...)
	sort.Strings(names)
	for _, name := range names {
		h.Write([]byte{0})
		h.Write([]byte(stringField(input, name)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// resolveAttachments turns each attachments entry — a doc ref minted by a
// prior storage fetch — into its actual bytes. A filename is synthesized
// from the resolved content type since the registry field carries no
// per-attachment name.
func (s *sendEmailConnector) resolveAttachments(refs []string) ([]EmailAttachment, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]EmailAttachment, 0, len(refs))
	for i, ref := range refs {
		content, contentType, found := s.docRefs.resolve(ref)
		if !found {
			return nil, fmt.Errorf("%w: attachments[%d]: unresolvable document ref %q", ErrValidation, i, ref)
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
