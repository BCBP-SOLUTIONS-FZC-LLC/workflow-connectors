package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

type ProviderClient interface {
	Fetch(ctx context.Context, bucket, key string) (content []byte, contentType string, err error)
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

func (*Connector) Type() string { return registry.TypeStorage }

func (s *Connector) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	bucket := shared.StringField(input, "bucket")
	key := shared.StringField(input, "key")
	if bucket == "" || key == "" {
		return nil, fmt.Errorf("%w: bucket and key are required", shared.ErrValidation)
	}

	client, err := s.clientFor(ctx, input)
	if err != nil {
		return nil, err
	}

	switch shared.StringField(input, "operation") {
	case "fetch":
		return s.fetch(ctx, client, bucket, key, input)
	case "upload":
		return s.upload(ctx, client, bucket, key, input)
	case "delete":
		if err := client.Delete(ctx, bucket, key); err != nil {
			return nil, fmt.Errorf("%w: storage delete: %s", shared.ErrUpstream, err)
		}
		return map[string]any{}, nil
	default:
		return nil, fmt.Errorf("%w: operation must be one of fetch/upload/delete", shared.ErrValidation)
	}
}

func (s *Connector) clientFor(ctx context.Context, input map[string]any) (ProviderClient, error) {
	provider := shared.StringField(input, "provider")
	if provider == "" {
		return nil, fmt.Errorf("%w: provider is required", shared.ErrValidation)
	}

	ctor, ok := s.providers[provider]
	if !ok {
		return nil, fmt.Errorf("%w: provider %q is not configured", shared.ErrValidation, provider)
	}

	key := cacheKey(provider, input)
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
	content, contentType, err := client.Fetch(ctx, bucket, key)
	if err != nil {
		return nil, fmt.Errorf("%w: storage fetch: %s", shared.ErrUpstream, err)
	}

	out := map[string]any{
		"contentType": contentType,
		"sizeBytes":   len(content),
		"fetchedAt":   time.Now().UTC(),
	}
	if shared.BoolField(input, "createDocument") {
		ref := mintDocRef()
		s.docRefs.Register(ref, content, contentType)
		out["contentRef"] = ref
	} else {
		out["content"] = string(content)
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

	if resolved, ct, found := s.docRefs.Resolve(content); found {
		raw = resolved
		if contentType == "" {
			contentType = ct
		}
	}

	if err := client.Upload(ctx, bucket, key, raw, contentType); err != nil {
		return nil, fmt.Errorf("%w: storage upload: %s", shared.ErrUpstream, err)
	}
	return map[string]any{"contentRef": mintDocRef(), "sizeBytes": len(raw)}, nil
}

func mintDocRef() string {
	return "mock-doc:" + uuid.New().String()
}
