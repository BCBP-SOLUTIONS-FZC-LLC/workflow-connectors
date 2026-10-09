package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
)

func TestStorage_Delete_RemovesObject(t *testing.T) {
	t.Parallel()

	client := storage.NewMockStorageClient()
	require.NoError(t, client.Upload(context.Background(), "b", "k", []byte("x"), "text/plain"))
	conn := storage.New(mockStorageProviders(client), nil)

	out, err := conn.Execute(tctx, base("delete"))
	require.NoError(t, err)
	assert.Empty(t, out)

	_, _, err = client.Fetch(context.Background(), "b", "k", 10)
	assert.Error(t, err, "deleted object is gone")
}

func TestStorage_UploadValidation(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		ctx   context.Context
		refs  *docref.Service
		input map[string]any
		want  error
	}{
		"empty content": {
			ctx: tctx, refs: docref.NewMemoryService(),
			input: base("upload"), want: shared.ErrValidation,
		},
		"ref content without a ref service": {
			ctx: tctx, refs: nil,
			input: with(base("upload"), "content", "docref:abc"), want: shared.ErrValidation,
		},
		"ref content without a tenant": {
			ctx: context.Background(), refs: docref.NewMemoryService(),
			input: with(base("upload"), "content", "docref:abc"), want: shared.ErrMissingTenant,
		},
		"over the object limit": {
			ctx: tctx, refs: nil,
			input: with(base("upload"), "content", strings.Repeat("a", int(shared.MaxObjectBytes)+1)), want: shared.ErrValidation,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := storage.NewMockStorageClient()
			_, err := storage.New(mockStorageProviders(client), tc.refs).Execute(tc.ctx, tc.input)
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.want)
			_, _, fetchErr := client.Fetch(context.Background(), "b", "k", 10)
			assert.Error(t, fetchErr, "nothing uploaded")
		})
	}
}

func TestStorage_UploadProviderError_IsUpstream(t *testing.T) {
	t.Parallel()

	client := storage.NewMockStorageClient()
	boom := errors.New("upload failed")
	client.SetError(boom)
	_, err := storage.New(mockStorageProviders(client), nil).Execute(tctx, with(base("upload"), "content", "hi"))
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrUpstream)
	assert.ErrorIs(t, err, boom)
}

func TestStorage_UploadCreateDocument_WithoutRefService_IsValidation(t *testing.T) {
	t.Parallel()

	client := storage.NewMockStorageClient()
	_, err := storage.New(mockStorageProviders(client), nil).Execute(tctx, with(base("upload"), "content", "hi", "createDocument", true))
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrValidation)
}

func TestStorage_UploadCreateDocument_RefStoreFailure_IsUpstream(t *testing.T) {
	t.Parallel()

	refs := docref.NewMemoryStore()
	boom := errors.New("valkey down")
	refs.FailPut(boom)
	svc := docref.NewService(refs, docref.NewMemoryContent("test-documents"))

	client := storage.NewMockStorageClient()
	_, err := storage.New(mockStorageProviders(client), svc).Execute(tctx, with(base("upload"), "content", "hi", "createDocument", true))
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrUpstream)
	assert.ErrorIs(t, err, boom)
}
