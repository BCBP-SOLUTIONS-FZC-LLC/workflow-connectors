package connectors_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
)

// This exercises the one thing only root's New() can: storage and send-email
// resolving document refs through the same configured document-ref store.
func TestSendEmail_AttachmentFromStorage_ResolvesViaSharedDocRefs(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	storageClient := storage.NewMockStorageClient()
	emailClient := sendemail.NewMockSendEmailClient()
	cfg.StorageProviders = map[string]storage.ProviderConstructor{
		"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) {
			return storageClient, nil
		},
	}
	cfg.SendEmailProviders = map[string]sendemail.ProviderConstructor{
		"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) {
			return emailClient, nil
		},
	}
	cfg.DocRefs = docref.NewMemoryService()
	all, err := connectors.New(cfg)
	require.NoError(t, err)
	ctx := connectors.WithTenant(context.Background(), "tenant-1")

	_, err = all["storage"].Execute(ctx, map[string]any{
		"operation":   "upload",
		"provider":    "aws-s3",
		"bucket":      "b1",
		"key":         "k1",
		"content":     "invoice contents",
		"contentType": "application/pdf",
	})
	require.NoError(t, err)

	fetchOut, err := all["storage"].Execute(ctx, map[string]any{
		"operation":      "fetch",
		"provider":       "aws-s3",
		"bucket":         "b1",
		"key":            "k1",
		"createDocument": true,
	})
	require.NoError(t, err)
	ref, ok := fetchOut["contentRef"].(string)
	require.True(t, ok)

	_, err = all["send-email"].Execute(ctx, map[string]any{
		"provider":      "sendgrid",
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"body":          "invoice attached",
		"attachments":   []any{ref},
	})
	require.NoError(t, err)

	sent := emailClient.Sent()
	require.Len(t, sent, 1)
	require.Len(t, sent[0].Attachments, 1)
	assert.Equal(t, "invoice contents", string(sent[0].Attachments[0].Content))
	assert.Equal(t, "application/pdf", sent[0].Attachments[0].ContentType)
	assert.Equal(t, "attachment-1.pdf", sent[0].Attachments[0].Filename)
}

func TestNew_WithoutDocRefStore_RefsAreDisabledNotLocal(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.StorageProviders = map[string]storage.ProviderConstructor{
		"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) {
			return storage.NewMockStorageClient(), nil
		},
	}
	all, err := connectors.New(cfg)
	require.NoError(t, err)

	_, err = all["storage"].Execute(connectors.WithTenant(context.Background(), "t"), map[string]any{
		"operation": "fetch", "provider": "aws-s3", "bucket": "b", "key": "k", "createDocument": true,
	})
	assert.ErrorIs(t, err, connectors.ErrValidation, "no silent fallback to worker-local refs")
}

// A ref whose S3 object is missing or altered fails before anything is sent
// or uploaded, with its docref error kept for errors.Is.
func TestDocRefs_UnresolvableSource_FailsBeforeAnySideEffect(t *testing.T) {
	t.Parallel()

	refs, content := docref.NewMemoryStore(), docref.NewMemoryContent("docs")
	cfg := validConfig()
	tenantBucket := storage.NewMockStorageClient()
	emailClient := sendemail.NewMockSendEmailClient()
	cfg.StorageProviders = map[string]storage.ProviderConstructor{
		"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) { return tenantBucket, nil },
	}
	cfg.SendEmailProviders = map[string]sendemail.ProviderConstructor{
		"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) { return emailClient, nil },
	}
	cfg.DocRefs = docref.NewService(refs, content)
	all, err := connectors.New(cfg)
	require.NoError(t, err)
	ctx := connectors.WithTenant(context.Background(), "tenant-1")

	missing, err := cfg.DocRefs.Create(ctx, "tenant-1", "text/plain", []byte("deleted later"))
	require.NoError(t, err)
	require.NoError(t, content.Delete(ctx, missing.Bucket, missing.ObjectKey))
	altered, err := cfg.DocRefs.Create(ctx, "tenant-1", "text/plain", []byte("original"))
	require.NoError(t, err)
	require.NoError(t, content.Put(ctx, altered.ObjectKey, []byte("tampered"), "text/plain"))

	for ref, want := range map[string]error{missing.ID: docref.ErrSourceMissing, altered.ID: docref.ErrIntegrityViolation} {
		_, err = all["send-email"].Execute(ctx, map[string]any{
			"provider": "sendgrid", "senderEmail": "a@example.com", "receiverEmail": "b@example.com",
			"body": "x", "attachments": []any{ref},
		})
		assert.ErrorIs(t, err, connectors.ErrValidation)
		assert.ErrorIs(t, err, want)

		_, err = all["storage"].Execute(ctx, map[string]any{
			"operation": "upload", "provider": "aws-s3", "bucket": "b", "key": "out", "content": ref,
		})
		assert.ErrorIs(t, err, connectors.ErrValidation)
		assert.ErrorIs(t, err, want)
	}
	assert.Empty(t, emailClient.Sent(), "nothing was sent")
	_, _, err = tenantBucket.Fetch(ctx, "b", "out", 50<<20)
	assert.Error(t, err, "nothing was uploaded")
}
