package googledrive

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// Items in a folder that are not stored files: a delete by name would remove
// a folder past the trash, an upload would overwrite it, a fetch cannot read it.
var nonFileTypes = []string{
	"application/vnd.google-apps.folder",
	"application/vnd.google-apps.shortcut",
	"application/vnd.google-apps.document",
	"application/vnd.google-apps.spreadsheet",
}

// A delete of a name with no document never removes a same-name folder,
// shortcut or Google-native file.
func TestDrive_Delete_NeverRemovesAFolderOrNativeItemByName(t *testing.T) {
	t.Parallel()
	for _, mime := range nonFileTypes {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, documents.NewMemoryStore())
		item := api.placeTyped(folder, "reports", mime)

		require.NoError(t, c.Delete(ctx, folder, "reports"), mime)
		assert.Equal(t, mime, api.mimeOf(item), "%s survives the delete", mime)
	}
}

// With a hand-placed file and a same-name folder, the delete removes the
// file only, even when the folder is older.
func TestDrive_Delete_RemovesTheFileNotTheOlderFolder(t *testing.T) {
	t.Parallel()
	ctx, api := tenantCtx(), newFakeDriveFilesAPI()
	c := newTestClient(api, documents.NewMemoryStore())
	dir := api.placeTyped(folder, "a.pdf", "application/vnd.google-apps.folder")
	file := api.placeFile(folder, "a.pdf", "", []byte("placed by a person"))

	require.NoError(t, c.Delete(ctx, folder, "a.pdf"))
	assert.Empty(t, api.mimeOf(file), "the file is deleted")
	assert.Equal(t, "application/vnd.google-apps.folder", api.mimeOf(dir), "the folder is not")
}

// An upload never adopts a same-name folder or native item: it creates a new
// file and leaves the item as it was.
func TestDrive_Upload_NeverAdoptsAFolderOrNativeItem(t *testing.T) {
	t.Parallel()
	for _, mime := range nonFileTypes {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		store := documents.NewMemoryStore()
		c := newTestClient(api, store)
		item := api.placeTyped(folder, "a.pdf", mime)

		require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("new"), "application/pdf"), mime)
		doc := getDoc(t, ctx, store, "a.pdf")
		assert.NotEqual(t, item, doc.ObjectID, mime)
		assert.Equal(t, mime, api.mimeOf(item), "%s is untouched", mime)
		creates, updates := api.calls()
		assert.Equal(t, 1, creates, mime)
		assert.Zero(t, updates, mime)
	}
}

// A fetch by name never reads a folder or native item: only a content file.
func TestDrive_Fetch_NeverReadsAFolderOrNativeItem(t *testing.T) {
	t.Parallel()
	for _, mime := range nonFileTypes {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, documents.NewMemoryStore())
		api.placeTyped(folder, "a.pdf", mime)

		_, _, err := c.Fetch(ctx, folder, "a.pdf", 1<<20)
		require.ErrorIs(t, err, shared.ErrUpstream, mime)
		class, _ := shared.ClassOf(err)
		assert.Equal(t, shared.ClassPermanent, class, mime)
		assert.Contains(t, err.Error(), `no file named "a.pdf"`, mime)

		api.placeFile(folder, "a.pdf", "", []byte("the file"))
		content, _, err := c.Fetch(ctx, folder, "a.pdf", 1<<20)
		require.NoError(t, err, mime)
		assert.Equal(t, "the file", string(content), mime)
	}
}

// ---- Fetch and the row's lease ---------------------------------------------

func identityOf(t *testing.T, ctx context.Context, name string) documents.Identity {
	t.Helper()
	tenant, ok := shared.TenantFromContext(ctx)
	require.True(t, ok)
	return documents.Identity{TenantID: tenant, Provider: providerName, Container: folder, Filename: name}
}

// claimCrashed leaves the row of name owned by a call that crashed an hour
// ago: busy, with a lease long expired.
func claimCrashed(t *testing.T, ctx context.Context, store *documents.MemoryStore, name string, to documents.State) documents.Document {
	t.Helper()
	store.SetClock(func() time.Time { return time.Now().Add(-time.Hour) })
	doc, claimed, err := store.Claim(ctx, identityOf(t, ctx, name), "crashed-attempt", documents.DefaultLease)
	require.NoError(t, err)
	require.True(t, claimed)
	if to != documents.StatePendingUpload {
		doc, err = store.Advance(ctx, doc.ID, "crashed-attempt", to)
		require.NoError(t, err)
	}
	store.SetClock(time.Now)
	require.True(t, doc.LeaseExpiresAt.Before(time.Now()))
	return doc
}

// A crashed upload's row (busy, lease expired) is not in progress for ever: a
// fetch reads past it — the file the crashed attempt tagged, else the
// untagged file of the name.
func TestDrive_Fetch_ExpiredBusyRow_ReadsPastIt(t *testing.T) {
	t.Parallel()
	for _, state := range []documents.State{documents.StatePendingUpload, documents.StateUploading, documents.StateDeleting} {
		ctx, api, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
		c := newTestClient(api, store)
		doc := claimCrashed(t, ctx, store, "a.pdf", state)

		_, _, err := c.Fetch(ctx, folder, "a.pdf", 1<<20)
		require.ErrorIs(t, err, shared.ErrUpstream, state)
		assert.NotErrorIs(t, err, documents.ErrUploadInProgress, state)
		assert.Contains(t, err.Error(), `no file named "a.pdf"`, state)

		api.placeFile(folder, "a.pdf", "", []byte("untagged"))
		content, _, err := c.Fetch(ctx, folder, "a.pdf", 1<<20)
		require.NoError(t, err, state)
		assert.Equal(t, "untagged", string(content), state)

		api.placeFile(folder, "a.pdf", doc.ID, []byte("left by the crashed attempt"))
		content, _, err = c.Fetch(ctx, folder, "a.pdf", 1<<20)
		require.NoError(t, err, state)
		assert.Equal(t, "left by the crashed attempt", string(content), "the document's own file wins (%s)", state)
	}
}

// A live upload writing a document for the first time is in progress.
func TestDrive_Fetch_LiveFirstUpload_IsInProgress(t *testing.T) {
	t.Parallel()
	ctx, api, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
	c := newTestClient(api, store)
	api.placeFile(folder, "a.pdf", "", []byte("about to be adopted"))
	_, claimed, err := store.Claim(ctx, identityOf(t, ctx, "a.pdf"), "live", documents.DefaultLease)
	require.NoError(t, err)
	require.True(t, claimed)

	_, _, err = c.Fetch(ctx, folder, "a.pdf", 1<<20)
	require.ErrorIs(t, err, documents.ErrUploadInProgress)
	class, _ := shared.ClassOf(err)
	assert.Equal(t, shared.ClassTransient, class)
}

// A document a live call is deleting is in progress, not a permanent 404 on
// a file about to vanish; once the delete's lease expires (a crash), the
// recorded file is read again.
func TestDrive_Fetch_LiveDelete_IsInProgress(t *testing.T) {
	t.Parallel()
	ctx, api, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
	c := newTestClient(api, store)
	require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("stored"), ""))
	doc, claimed, err := store.Claim(ctx, identityOf(t, ctx, "a.pdf"), "deleter", documents.DefaultLease)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = store.Advance(ctx, doc.ID, "deleter", documents.StateDeleting)
	require.NoError(t, err)

	_, _, err = c.Fetch(ctx, folder, "a.pdf", 1<<20)
	var inProgress *documents.InProgressError
	require.ErrorAs(t, err, &inProgress)
	assert.Equal(t, documents.StateDeleting, inProgress.State)
}

func TestDrive_Fetch_ExpiredDeleteWithRecordedFile_ReadsIt(t *testing.T) {
	t.Parallel()
	ctx, api, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
	c := newTestClient(api, store)
	require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("stored"), ""))
	claimCrashed(t, ctx, store, "a.pdf", documents.StateDeleting)

	content, _, err := c.Fetch(ctx, folder, "a.pdf", 1<<20)
	require.NoError(t, err)
	assert.Equal(t, "stored", string(content))
}

// A live re-upload of a document that has a file still reads the recorded
// file (old or new content, never a missing one).
func TestDrive_Fetch_LiveReUpload_ReadsTheRecordedFile(t *testing.T) {
	t.Parallel()
	ctx, api, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
	c := newTestClient(api, store)
	require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("v1"), ""))
	_, claimed, err := store.Claim(ctx, identityOf(t, ctx, "a.pdf"), "re-upload", documents.DefaultLease)
	require.NoError(t, err)
	require.True(t, claimed)

	content, _, err := c.Fetch(ctx, folder, "a.pdf", 1<<20)
	require.NoError(t, err)
	assert.Equal(t, "v1", string(content))
}

// A Drive failure looking up a row's tagged file fails the fetch, classified.
func TestDrive_Fetch_TaggedLookupFailure_IsUpstream(t *testing.T) {
	t.Parallel()
	ctx, api, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
	c := newTestClient(api, store)
	claimCrashed(t, ctx, store, "a.pdf", documents.StateUploading)
	api.err = errFakeDrive

	_, _, err := c.Fetch(ctx, folder, "a.pdf", 1<<20)
	require.ErrorIs(t, err, shared.ErrUpstream)
	assert.Contains(t, err.Error(), `drive fetch "a.pdf"`)
}
