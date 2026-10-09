package storage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

type ProviderClient interface {
	// Fetch reads the object, holding at most maxBytes: a larger object fails
	// with shared.ErrTooLarge, ideally before any of it is read.
	Fetch(ctx context.Context, bucket, key string, maxBytes int64) (content []byte, contentType string, err error)
	Upload(ctx context.Context, bucket, key string, content []byte, contentType string) error
	Delete(ctx context.Context, bucket, key string) error
}

type ProviderConstructor func(ctx context.Context, params map[string]any) (ProviderClient, error)

var credentialFieldNames = []string{
	"accessKey", "secretKey", "region",
	"azureAccountName", "azureAccountKey",
	"gcpServiceAccountKey", "projectId",
	"driveServiceAccountKey",
}

const clientCacheLimit = 256

type Connector struct {
	providers map[string]ProviderConstructor
	// docRefs creates and resolves document references: content in S3,
	// metadata in Valkey (nil: refs disabled — createDocument and ref content
	// are validation errors and upload returns no contentRef).
	docRefs *docref.Service

	clients *shared.ClientCache[ProviderClient]
}

func New(providers map[string]ProviderConstructor, docRefs *docref.Service) *Connector {
	return &Connector{
		providers: providers,
		docRefs:   docRefs,
		clients:   shared.NewClientCache[ProviderClient](clientCacheLimit),
	}
}

func (*Connector) Type() string { return registry.TypeStorage }

func (s *Connector) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	bucket := shared.StringField(input, "bucket")
	key := shared.StringField(input, "key")
	if bucket == "" || key == "" {
		return nil, fmt.Errorf("%w: bucket and key are required", shared.ErrValidation)
	}

	handle, err := s.clientFor(ctx, input)
	if err != nil {
		return nil, err
	}
	defer handle.Release()
	client := handle.Client()

	switch shared.StringField(input, "operation") {
	case "fetch":
		return s.fetch(ctx, client, bucket, key, input)
	case "upload":
		return s.upload(ctx, client, bucket, key, input)
	case "delete":
		if err := client.Delete(ctx, bucket, key); err != nil {
			return nil, shared.Classify("storage delete", err)
		}
		return map[string]any{}, nil
	default:
		return nil, fmt.Errorf("%w: operation must be one of fetch/upload/delete", shared.ErrValidation)
	}
}

// clientFor returns a handle on the provider client for this call's
// credentials. The caller must Release it when the call is finished; a reset
// of the cache never closes a client while a handle is out.
func (s *Connector) clientFor(ctx context.Context, input map[string]any) (*shared.ClientHandle[ProviderClient], error) {
	provider := shared.StringField(input, "provider")
	if provider == "" {
		return nil, fmt.Errorf("%w: provider is required", shared.ErrValidation)
	}

	ctor, ok := s.providers[provider]
	if !ok {
		return nil, fmt.Errorf("%w: provider %q is not configured", shared.ErrValidation, provider)
	}

	return s.clients.Acquire(cacheKey(provider, input), func() (ProviderClient, error) {
		client, err := ctor(ctx, input)
		if err != nil {
			return nil, shared.Classify(fmt.Sprintf("build %s client", provider), err)
		}
		return client, nil
	})
}

// ResetClients retires every cached provider client, for example after
// credentials are rotated. Clients in use by an in-flight call are closed
// when that call finishes; new calls build fresh clients.
func (s *Connector) ResetClients() { s.clients.Reset() }

func cacheKey(provider string, input map[string]any) string {
	h := sha256.New()
	h.Write([]byte(provider))
	h.Write([]byte{0})
	h.Write([]byte(shared.StringField(input, "bucket")))
	names := append([]string(nil), credentialFieldNames...)
	sort.Strings(names)
	for _, name := range names {
		h.Write([]byte{0})
		h.Write([]byte(shared.StringField(input, name)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Connector) fetch(ctx context.Context, client ProviderClient, bucket, key string, input map[string]any) (map[string]any, error) {
	createDocument := shared.BoolField(input, "createDocument")
	tenant, err := s.refTenant(ctx, createDocument)
	if err != nil {
		return nil, err
	}

	// An inline result is capped at 1 MiB, so never read more than that for
	// one; a document may be up to the object limit.
	limit := shared.MaxObjectBytes
	if !createDocument {
		limit = shared.MaxInlineBytes
	}
	content, contentType, err := client.Fetch(ctx, bucket, key, limit)
	if err != nil {
		if !createDocument && errors.Is(err, shared.ErrTooLarge) {
			return nil, fmt.Errorf("%w: object %q is too large to return inline (limit %d bytes); set createDocument", shared.ErrValidation, key, shared.MaxInlineBytes)
		}
		return nil, shared.Classify("storage fetch", err)
	}

	out := map[string]any{
		"contentType": contentType,
		"sizeBytes":   len(content),
		"fetchedAt":   time.Now().UTC(),
	}
	if createDocument {
		// Content to S3, then metadata to Valkey, both before the ref is
		// returned, so any replica can resolve it.
		ref, err := s.docRefs.Create(ctx, tenant, contentType, content)
		if err != nil {
			return nil, classifyRef("storage fetch: create document ref", err)
		}
		out["contentRef"] = ref.ID
		return out, nil
	}

	// Inline content travels as a JSON string. Bytes that are not valid UTF-8
	// would be replaced in transit, so they are base64-encoded instead.
	if utf8.Valid(content) {
		out["content"] = string(content)
		out["contentEncoding"] = encodingUTF8
	} else {
		out["content"] = base64.StdEncoding.EncodeToString(content)
		out["contentEncoding"] = encodingBase64
	}
	return out, nil
}

func (s *Connector) upload(ctx context.Context, client ProviderClient, bucket, key string, input map[string]any) (map[string]any, error) {
	content := shared.StringField(input, "content")
	if content == "" {
		return nil, fmt.Errorf("%w: content is required for upload", shared.ErrValidation)
	}
	raw := []byte(content)
	contentType := shared.StringField(input, "contentType")
	fromRef := docref.IsRef(content)

	if fromRef {
		tenant, err := s.refTenant(ctx, true)
		if err != nil {
			return nil, err
		}
		// Resolved from S3 and verified (size, SHA-256) before anything is
		// uploaded.
		ref, resolved, err := s.docRefs.Read(ctx, tenant, content, shared.MaxObjectBytes)
		if err != nil {
			return nil, classifyRef("storage upload: resolve document ref", err)
		}
		raw = resolved
		if contentType == "" {
			contentType = ref.ContentType
		}
	} else {
		switch shared.StringField(input, "contentEncoding") {
		case "", encodingUTF8:
		case encodingBase64:
			decoded, err := base64.StdEncoding.DecodeString(content)
			if err != nil {
				return nil, fmt.Errorf("%w: content is not valid base64: %s", shared.ErrValidation, err)
			}
			raw = decoded
		default:
			return nil, fmt.Errorf("%w: contentEncoding must be %q or %q", shared.ErrValidation, encodingUTF8, encodingBase64)
		}
	}
	if int64(len(raw)) > shared.MaxObjectBytes {
		return nil, fmt.Errorf("%w: upload of %d bytes exceeds the %d-byte limit", shared.ErrValidation, len(raw), shared.MaxObjectBytes)
	}

	if err := client.Upload(ctx, bucket, key, raw, contentType); err != nil {
		return nil, shared.Classify("storage upload", err)
	}

	out := map[string]any{"sizeBytes": len(raw)}
	switch {
	case fromRef:
		// The uploaded bytes are the ref's verified bytes, so the same ref
		// already resolves to them.
		out["contentRef"] = content
	case shared.BoolField(input, "createDocument"):
		// Opt-in, as for fetch: a plain upload stores nothing beyond the
		// tenant's bucket, so it cannot fail after the upload succeeded.
		tenant, err := s.refTenant(ctx, true)
		if err != nil {
			return nil, err
		}
		ref, err := s.docRefs.Create(ctx, tenant, contentType, raw)
		if err != nil {
			return nil, classifyRef("storage upload: create document ref", err)
		}
		out["contentRef"] = ref.ID
	}
	return out, nil
}

// refTenant returns the tenant a document ref is scoped to, when needed.
func (s *Connector) refTenant(ctx context.Context, needed bool) (string, error) {
	if !needed {
		return "", nil
	}
	if s.docRefs == nil {
		return "", fmt.Errorf("%w: document refs need a document-ref service (connectors.Config.DocRefs)", shared.ErrValidation)
	}
	tenant, ok := shared.TenantFromContext(ctx)
	if !ok {
		return "", shared.ErrMissingTenant
	}
	return tenant, nil
}

// classifyRef maps a document-ref failure to the connector error classes: a
// ref that can never resolve as it stands (not found, source missing,
// integrity violation, too large) is ErrValidation and keeps its docref
// sentinel for errors.Is; a store failure (Valkey or S3 unavailable) is
// ErrUpstream: transient when the store says so (docref.ErrUnavailable) or
// the cause is a network failure, else as classified by its cause.
func classifyRef(op string, err error) error {
	if docref.IsResolutionFailure(err) {
		return fmt.Errorf("%w: %s: %w", shared.ErrValidation, op, err)
	}
	if errors.Is(err, docref.ErrUnavailable) {
		err = shared.Transient("document-ref store unavailable", err)
	}
	return shared.Classify(op, err)
}

const (
	encodingUTF8   = "utf-8"
	encodingBase64 = "base64"
)
