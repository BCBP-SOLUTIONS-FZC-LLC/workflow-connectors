package googledrive

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

// Drive scenarios through the public facade (connectors.New + Execute) and
// the worker's retry decision, over the fake Drive API. They belong to
// test/unit's regression and worker-scenario suites (REG-13, WS-12) but live
// here because the Drive fake is unexported.

// driveConnector is connectors.New's storage connector with the Drive
// adapter over api and store as its google-drive provider.
func driveConnector(t *testing.T, api driveFilesAPI, store documents.Store) connectors.Connector {
	t.Helper()
	client := newTestClient(api, store)
	all, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		StorageProviders: map[string]storage.ProviderConstructor{
			providerName: func(context.Context, map[string]any) (storage.ProviderClient, error) { return client, nil },
		},
	})
	require.NoError(t, err)
	return all[registry.TypeStorage]
}

func driveInput(operation, name string) map[string]any {
	return map[string]any{"operation": operation, "provider": providerName, "bucket": folder, "key": name}
}

// REG-13: a delete with no registry row fell back to the oldest untagged
// same-name item, which could be a folder; files.Delete skips the trash, so
// the folder and everything in it was permanently deleted.
func TestREG13_DriveDelete_NeverRemovesASameNameFolder(t *testing.T) {
	t.Parallel()
	ctx, api := tenantCtx(), newFakeDriveFilesAPI()
	dir := api.placeTyped(folder, "reports", "application/vnd.google-apps.folder")
	conn := driveConnector(t, api, documents.NewMemoryStore())

	_, err := conn.Execute(ctx, driveInput("delete", "reports"))
	require.NoError(t, err, "nothing of that name is a file: already deleted")
	assert.Equal(t, "application/vnd.google-apps.folder", api.mimeOf(dir), "the folder survives")

	file := api.placeFile(folder, "reports", "", []byte("placed by a person"))
	_, err = conn.Execute(ctx, driveInput("delete", "reports"))
	require.NoError(t, err)
	assert.Empty(t, api.mimeOf(file), "the same-name file is deleted")
	assert.Equal(t, "application/vnd.google-apps.folder", api.mimeOf(dir), "the folder still survives")
}

// WS-12: an upload crashed after writing its file, leaving its row busy with
// a lease that has since expired. The worker's fetch reads the file on the
// first attempt; before the fix it got InProgressError (transient) on every
// attempt until the worker's limit, though nothing was in progress.
func TestWorker_DriveFetchAfterCrashedUpload_ReadsTheFile(t *testing.T) {
	t.Parallel()
	ctx, api, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
	doc := claimCrashed(t, ctx, store, "a.pdf", documents.StateUploading)
	api.placeFile(folder, "a.pdf", doc.ID, []byte("written before the crash"))
	conn := driveConnector(t, api, store)

	const maxAttempts = 5
	var (
		out      map[string]any
		err      error
		attempts int
	)
	for attempts = 1; attempts <= maxAttempts; attempts++ {
		out, err = conn.Execute(ctx, driveInput("fetch", "a.pdf"))
		if err == nil || !connectors.DecideRetry(registry.TypeStorage, err, "").Retry {
			break
		}
	}
	require.NoError(t, err)
	assert.Equal(t, 1, attempts)
	assert.Equal(t, "written before the crash", out["content"])

	// While the upload is live, the same fetch is in progress (transient,
	// retried): the expired lease is what lets it through.
	ctx2, api2, store2 := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
	_, claimed, err := store2.Claim(ctx2, identityOf(t, ctx2, "a.pdf"), "live", documents.DefaultLease)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = driveConnector(t, api2, store2).Execute(ctx2, driveInput("fetch", "a.pdf"))
	require.ErrorIs(t, err, documents.ErrUploadInProgress)
	d := connectors.DecideRetry(registry.TypeStorage, err, "")
	assert.True(t, d.Retry)
	assert.Equal(t, "upload in progress", d.Reason)
}
