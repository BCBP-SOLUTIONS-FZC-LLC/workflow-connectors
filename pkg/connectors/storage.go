package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

type StorageProviderClient interface {
	Fetch(ctx context.Context, bucket, key string) (content []byte, contentType string, err error)
	Upload(ctx context.Context, bucket, key string, content []byte, contentType string) error
	Delete(ctx context.Context, bucket, key string) error
}

type StorageProviderConstructor func(ctx context.Context, params map[string]any) (StorageProviderClient, error)

var credentialFieldNames = []string{
	"accessKey", "secretKey", "region",
	"azureAccountName", "azureAccountKey",
	"gcpServiceAccountKey", "projectId",
	"driveServiceAccountKey",
}

const clientCacheLimit = 256

type storageConnector struct {
	providers map[string]StorageProviderConstructor
	docRefs   *docRefStore

	cacheMu sync.Mutex
	cache   map[string]StorageProviderClient
}

func newStorage(cfg Config) Connector {
	return &storageConnector{
		providers: cfg.StorageProviders,
		docRefs:   newDocRefStore(),
		cache:     make(map[string]StorageProviderClient),
	}
}

func (*storageConnector) Type() string { return registry.TypeStorage }

func (s *storageConnector) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	bucket := stringField(input, "bucket")
	key := stringField(input, "key")
	if bucket == "" || key == "" {
		return nil, fmt.Errorf("%w: bucket and key are required", ErrValidation)
	}

	client, err := s.clientFor(ctx, input)
	if err != nil {
		return nil, err
	}

	switch stringField(input, "operation") {
	case "fetch":
		return s.fetch(ctx, client, bucket, key, input)
	case "upload":
		return s.upload(ctx, client, bucket, key, input)
	case "delete":
		if err := client.Delete(ctx, bucket, key); err != nil {
			return nil, fmt.Errorf("%w: storage delete: %s", ErrUpstream, err)
		}
		return map[string]any{}, nil
	default:
		return nil, fmt.Errorf("%w: operation must be one of fetch/upload/delete", ErrValidation)
	}
}

func (s *storageConnector) clientFor(ctx context.Context, input map[string]any) (StorageProviderClient, error) {
	provider := stringField(input, "provider")
	if provider == "" {
		return nil, fmt.Errorf("%w: provider is required", ErrValidation)
	}

	ctor, ok := s.providers[provider]
	if !ok {
		return nil, fmt.Errorf("%w: provider %q is not configured", ErrValidation, provider)
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
		return nil, fmt.Errorf("%w: build %s client: %s", ErrUpstream, provider, err)
	}

	s.cacheMu.Lock()
	if len(s.cache) >= clientCacheLimit {
		s.cache = make(map[string]StorageProviderClient)
	}
	s.cache[key] = client
	s.cacheMu.Unlock()
	return client, nil
}

func cacheKey(provider string, input map[string]any) string {
	h := sha256.New()
	h.Write([]byte(provider))
	h.Write([]byte{0})
	h.Write([]byte(stringField(input, "bucket")))
	names := append([]string(nil), credentialFieldNames...)
	sort.Strings(names)
	for _, name := range names {
		h.Write([]byte{0})
		h.Write([]byte(stringField(input, name)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *storageConnector) fetch(ctx context.Context, client StorageProviderClient, bucket, key string, input map[string]any) (map[string]any, error) {
	content, contentType, err := client.Fetch(ctx, bucket, key)
	if err != nil {
		return nil, fmt.Errorf("%w: storage fetch: %s", ErrUpstream, err)
	}

	out := map[string]any{
		"contentType": contentType,
		"sizeBytes":   len(content),
		"fetchedAt":   time.Now().UTC(),
	}
	if boolField(input, "createDocument") {
		ref := mintDocRef()
		s.docRefs.register(ref, content, contentType)
		out["contentRef"] = ref
	} else {
		out["content"] = string(content)
	}
	return out, nil
}

func (s *storageConnector) upload(ctx context.Context, client StorageProviderClient, bucket, key string, input map[string]any) (map[string]any, error) {
	content := stringField(input, "content")
	if content == "" {
		return nil, fmt.Errorf("%w: content is required for upload", ErrValidation)
	}
	raw := []byte(content)
	contentType := stringField(input, "contentType")

	if resolved, ct, found := s.docRefs.resolve(content); found {
		raw = resolved
		if contentType == "" {
			contentType = ct
		}
	}

	if err := client.Upload(ctx, bucket, key, raw, contentType); err != nil {
		return nil, fmt.Errorf("%w: storage upload: %s", ErrUpstream, err)
	}
	return map[string]any{"contentRef": mintDocRef(), "sizeBytes": len(raw)}, nil
}

func mintDocRef() string {
	return "mock-doc:" + uuid.New().String()
}
