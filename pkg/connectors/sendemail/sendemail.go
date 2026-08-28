package sendemail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

type ProviderClient interface {
	Send(ctx context.Context, msg EmailMessage) (messageID string, err error)
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
	docRefs   *shared.DocRefStore

	cacheMu sync.Mutex
	cache   map[string]ProviderClient
}

func New(providers map[string]ProviderConstructor, docRefs *shared.DocRefStore) *Connector {
	return &Connector{
		providers: providers,
		docRefs:   docRefs,
		cache:     make(map[string]ProviderClient),
	}
}

func (*Connector) Type() string { return registry.TypeSendEmail }

func (s *Connector) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	senderEmail := shared.StringField(input, "senderEmail")
	receiverEmail := shared.StringField(input, "receiverEmail")
	if senderEmail == "" || receiverEmail == "" {
		return nil, fmt.Errorf("%w: senderEmail and receiverEmail are required", shared.ErrValidation)
	}

	provider := shared.StringField(input, "provider")
	templateID := shared.StringField(input, "templateId")
	body := shared.StringField(input, "body")
	if templateID == "" && body == "" {
		return nil, fmt.Errorf("%w: either templateId or body is required", shared.ErrValidation)
	}
	if templatelessEmailProviders[provider] && body == "" {
		return nil, fmt.Errorf("%w: body is required for provider %q (no server-side template mechanism)", shared.ErrValidation, provider)
	}

	attachments, err := s.resolveAttachments(shared.StringSliceField(input, "attachments"))
	if err != nil {
		return nil, err
	}

	client, err := s.clientFor(ctx, provider, input)
	if err != nil {
		return nil, err
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

	messageID, err := client.Send(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("%w: send-email: %s", shared.ErrUpstream, err)
	}

	return map[string]any{
		"sent":      true,
		"messageId": messageID,
		"sentAt":    time.Now().UTC(),
	}, nil
}

func (s *Connector) clientFor(ctx context.Context, provider string, input map[string]any) (ProviderClient, error) {
	if provider == "" {
		return nil, fmt.Errorf("%w: provider is required", shared.ErrValidation)
	}

	ctor, ok := s.providers[provider]
	if !ok {
		return nil, fmt.Errorf("%w: provider %q is not configured", shared.ErrValidation, provider)
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
		return nil, fmt.Errorf("%w: build %s client: %s", shared.ErrUpstream, provider, err)
	}

	s.cacheMu.Lock()
	if len(s.cache) >= clientCacheLimit {
		s.cache = make(map[string]ProviderClient)
	}
	s.cache[key] = client
	s.cacheMu.Unlock()
	return client, nil
}

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

func (s *Connector) resolveAttachments(refs []string) ([]EmailAttachment, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]EmailAttachment, 0, len(refs))
	for i, ref := range refs {
		content, contentType, found := s.docRefs.Resolve(ref)
		if !found {
			return nil, fmt.Errorf("%w: attachments[%d]: unresolvable document ref %q", shared.ErrValidation, i, ref)
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
