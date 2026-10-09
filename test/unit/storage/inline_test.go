package storage_test

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
)

func base(op string) map[string]any {
	return map[string]any{"provider": "aws-s3", "operation": op, "bucket": "b", "key": "k"}
}

func with(m map[string]any, kv ...any) map[string]any {
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func TestStorage_FetchBinaryInline_IsBase64(t *testing.T) {
	t.Parallel()

	client := storage.NewMockStorageClient()
	binary := []byte{0x25, 0x50, 0x44, 0x46, 0xff, 0xfe, 0x00, 0x80}
	require.NoError(t, client.Upload(context.Background(), "b", "k", binary, "application/pdf"))
	conn := storage.New(mockStorageProviders(client), docref.NewMemoryService())

	out, err := conn.Execute(tctx, base("fetch"))
	require.NoError(t, err)
	assert.Equal(t, "base64", out["contentEncoding"])
	decoded, err := base64.StdEncoding.DecodeString(out["content"].(string))
	require.NoError(t, err)
	assert.Equal(t, binary, decoded, "binary bytes must survive the inline round trip")
}

func TestStorage_FetchTextInline_IsUTF8(t *testing.T) {
	t.Parallel()

	client := storage.NewMockStorageClient()
	require.NoError(t, client.Upload(context.Background(), "b", "k", []byte("héllo"), "text/plain"))
	conn := storage.New(mockStorageProviders(client), docref.NewMemoryService())

	out, err := conn.Execute(tctx, base("fetch"))
	require.NoError(t, err)
	assert.Equal(t, "utf-8", out["contentEncoding"])
	assert.Equal(t, "héllo", out["content"])
}

func TestStorage_FetchInlineOverLimit_RequiresCreateDocument(t *testing.T) {
	t.Parallel()

	client := storage.NewMockStorageClient()
	require.NoError(t, client.Upload(context.Background(), "b", "k", make([]byte, shared.MaxInlineBytes+1), ""))
	conn := storage.New(mockStorageProviders(client), docref.NewMemoryService())

	_, err := conn.Execute(tctx, base("fetch"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))

	out, err := conn.Execute(tctx, with(base("fetch"), "createDocument", true))
	require.NoError(t, err)
	assert.NotEmpty(t, out["contentRef"])
}

func TestStorage_UploadBase64Content_StoresDecodedBytes(t *testing.T) {
	t.Parallel()

	client := storage.NewMockStorageClient()
	conn := storage.New(mockStorageProviders(client), docref.NewMemoryService())
	binary := []byte{0x00, 0xff, 0x10}

	_, err := conn.Execute(tctx, with(base("upload"),
		"content", base64.StdEncoding.EncodeToString(binary), "contentEncoding", "base64"))
	require.NoError(t, err)
	got, _, err := client.Fetch(context.Background(), "b", "k", 50<<20)
	require.NoError(t, err)
	assert.Equal(t, binary, got)
}

func TestStorage_UploadInvalidEncoding_IsValidationError(t *testing.T) {
	t.Parallel()

	conn := storage.New(mockStorageProviders(storage.NewMockStorageClient()), docref.NewMemoryService())

	_, err := conn.Execute(tctx, with(base("upload"), "content", "x", "contentEncoding", "rot13"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))

	_, err = conn.Execute(tctx, with(base("upload"), "content", "%%%", "contentEncoding", "base64"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestStorage_UploadRef_ResolvesToUploadedBytes(t *testing.T) {
	t.Parallel()

	docRefs := docref.NewMemoryService()
	conn := storage.New(mockStorageProviders(storage.NewMockStorageClient()), docRefs)

	ctx := shared.WithTenant(context.Background(), "tenant-1")
	out, err := conn.Execute(ctx, with(base("upload"), "content", "report body", "contentType", "text/plain", "createDocument", true))
	require.NoError(t, err)
	ref, content, err := docRefs.Read(ctx, "tenant-1", out["contentRef"].(string), 1<<20)
	require.NoError(t, err, "an upload's contentRef must be usable by a later task")
	assert.Equal(t, "report body", string(content))
	assert.Equal(t, "text/plain", ref.ContentType)
	assert.Equal(t, "test-documents", ref.Bucket, "content lives in the document bucket")
	assert.Equal(t, "tenant-1/"+ref.ID[len(docref.Prefix):], ref.ObjectKey)
}

func TestStorage_ConstructorValidationError_StaysValidation(t *testing.T) {
	t.Parallel()

	conn := storage.New(map[string]storage.ProviderConstructor{
		"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) {
			return nil, errors.Join(shared.ErrValidation, errors.New("accessKey is required"))
		},
	}, docref.NewMemoryService())

	_, err := conn.Execute(tctx, base("fetch"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation), "a missing credential is permanent")
	assert.False(t, errors.Is(err, shared.ErrUpstream))
}
