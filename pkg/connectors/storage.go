package connectors

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

// StorageProviderClient is the minimal surface storage's Execute needs from
// an S3-compatible object store — deliberately SDK-agnostic so a real
// AWS SDK/MinIO client can implement it later without touching Execute()
// itself.
type StorageProviderClient interface {
	Fetch(ctx context.Context, bucket, key string) (content []byte, contentType string, err error)
	Upload(ctx context.Context, bucket, key string, content []byte, contentType string) error
	Delete(ctx context.Context, bucket, key string) error
}

// DocRefResolver is an optional capability a StorageProviderClient may
// implement to resolve one of its own previously-minted document refs back
// into bytes — only meaningful for the in-memory mock (MockStorageClient),
// since a real SDK's document refs are self-describing (e.g. a bucket/key
// pair) and never need this indirection.
type DocRefResolver interface {
	ResolveDocRef(ref string) (content []byte, contentType string, found bool)
}

type docRefRegistrar interface {
	RegisterDocRef(ref string, content []byte, contentType string)
}

type storageConnector struct {
	client StorageProviderClient
}

func newStorage(cfg Config) Connector {
	client := cfg.StorageClient
	if client == nil {
		client = NewMockStorageClient()
	}
	return storageConnector{client: client}
}

func (storageConnector) Type() string { return registry.TypeStorage }

func (s storageConnector) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	bucket := stringField(input, "bucket")
	key := stringField(input, "key")
	if bucket == "" || key == "" {
		return nil, fmt.Errorf("%w: bucket and key are required", ErrValidation)
	}

	switch stringField(input, "operation") {
	case "fetch":
		return s.fetch(ctx, bucket, key, input)
	case "upload":
		return s.upload(ctx, bucket, key, input)
	case "delete":
		if err := s.client.Delete(ctx, bucket, key); err != nil {
			return nil, fmt.Errorf("%w: storage delete: %s", ErrUpstream, err)
		}
		return map[string]any{}, nil
	default:
		return nil, fmt.Errorf("%w: operation must be one of fetch/upload/delete", ErrValidation)
	}
}

func (s storageConnector) fetch(ctx context.Context, bucket, key string, input map[string]any) (map[string]any, error) {
	content, contentType, err := s.client.Fetch(ctx, bucket, key)
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
		if registrar, ok := s.client.(docRefRegistrar); ok {
			registrar.RegisterDocRef(ref, content, contentType)
		}
		out["contentRef"] = ref
	} else {
		out["content"] = string(content)
	}
	return out, nil
}

func (s storageConnector) upload(ctx context.Context, bucket, key string, input map[string]any) (map[string]any, error) {
	content := stringField(input, "content")
	if content == "" {
		return nil, fmt.Errorf("%w: content is required for upload", ErrValidation)
	}
	raw := []byte(content)
	contentType := stringField(input, "contentType")

	// content is a document_ref field — it may be a ref this same client
	// minted on an earlier fetch, in which case resolve it back to real
	// bytes rather than uploading the literal reference string.
	if resolver, ok := s.client.(DocRefResolver); ok {
		if resolved, ct, found := resolver.ResolveDocRef(content); found {
			raw = resolved
			if contentType == "" {
				contentType = ct
			}
		}
	}

	if err := s.client.Upload(ctx, bucket, key, raw, contentType); err != nil {
		return nil, fmt.Errorf("%w: storage upload: %s", ErrUpstream, err)
	}
	return map[string]any{"contentRef": mintDocRef(), "sizeBytes": len(raw)}, nil
}

func mintDocRef() string {
	return "mock-doc:" + uuid.New().String()
}
