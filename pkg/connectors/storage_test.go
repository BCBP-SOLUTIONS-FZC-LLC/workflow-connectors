package connectors_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors"
)

func storageConfig(client *connectors.MockStorageClient) connectors.Config {
	cfg := validConfig()
	cfg.StorageClient = client
	return cfg
}

func TestStorage_UploadThenFetch_RoundTrips(t *testing.T) {
	t.Parallel()

	client := connectors.NewMockStorageClient()
	all, err := connectors.New(storageConfig(client))
	require.NoError(t, err)
	storage := all["storage"]

	uploadOut, err := storage.Execute(context.Background(), map[string]any{
		"operation":   "upload",
		"bucket":      "b1",
		"key":         "k1",
		"content":     "hello world",
		"contentType": "text/plain",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, uploadOut["contentRef"])

	fetchOut, err := storage.Execute(context.Background(), map[string]any{
		"operation": "fetch",
		"bucket":    "b1",
		"key":       "k1",
	})
	require.NoError(t, err)
	assert.Equal(t, "hello world", fetchOut["content"])
	assert.Equal(t, "text/plain", fetchOut["contentType"])
}

func TestStorage_FetchWithCreateDocument_MintsResolvableRef(t *testing.T) {
	t.Parallel()

	client := connectors.NewMockStorageClient()
	require.NoError(t, client.Upload(context.Background(), "b1", "k1", []byte("payload"), "text/plain"))
	all, err := connectors.New(storageConfig(client))
	require.NoError(t, err)
	storage := all["storage"]

	fetchOut, err := storage.Execute(context.Background(), map[string]any{
		"operation":      "fetch",
		"bucket":         "b1",
		"key":            "k1",
		"createDocument": true,
	})
	require.NoError(t, err)
	ref, ok := fetchOut["contentRef"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, ref)
	_, hasInlineContent := fetchOut["content"]
	assert.False(t, hasInlineContent)

	// Uploading that same ref elsewhere resolves back to the original bytes.
	uploadOut, err := storage.Execute(context.Background(), map[string]any{
		"operation": "upload",
		"bucket":    "b2",
		"key":       "k2",
		"content":   ref,
	})
	require.NoError(t, err)
	assert.Equal(t, len("payload"), uploadOut["sizeBytes"])
}

func TestStorage_MissingRequiredFields(t *testing.T) {
	t.Parallel()

	all, err := connectors.New(storageConfig(connectors.NewMockStorageClient()))
	require.NoError(t, err)
	storage := all["storage"]

	_, err = storage.Execute(context.Background(), map[string]any{"operation": "fetch"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrValidation))
}

func TestStorage_UnknownOperation(t *testing.T) {
	t.Parallel()

	all, err := connectors.New(storageConfig(connectors.NewMockStorageClient()))
	require.NoError(t, err)
	storage := all["storage"]

	_, err = storage.Execute(context.Background(), map[string]any{"operation": "frobnicate", "bucket": "b", "key": "k"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrValidation))
}

func TestMockStorageClient_SetErrorAndReset(t *testing.T) {
	t.Parallel()

	client := connectors.NewMockStorageClient()
	client.SetError(errors.New("boom"))
	_, _, err := client.Fetch(context.Background(), "b", "k")
	require.Error(t, err)

	client.Reset()
	err = client.Upload(context.Background(), "b", "k", []byte("x"), "text/plain")
	require.NoError(t, err)
	content, _, err := client.Fetch(context.Background(), "b", "k")
	require.NoError(t, err)
	assert.Equal(t, "x", string(content))
}

func TestStorage_ProviderError_Propagates(t *testing.T) {
	t.Parallel()

	client := connectors.NewMockStorageClient()
	client.SetError(errors.New("boom"))
	all, err := connectors.New(storageConfig(client))
	require.NoError(t, err)
	storage := all["storage"]

	_, err = storage.Execute(context.Background(), map[string]any{"operation": "delete", "bucket": "b", "key": "k"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrUpstream))
}
