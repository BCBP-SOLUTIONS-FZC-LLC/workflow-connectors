package googledrive

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// Drive's update and download succeed on a file in the trash and on a file
// moved to another folder. A recorded file in either state is no longer the
// document's: the upload falls back as for a deleted file (here: creates a
// new file in the folder) and never writes to the trashed or moved file.
func TestDrive_Upload_RecordedFileTrashedOrMoved_WritesANewFile(t *testing.T) {
	for name, displace := range map[string]func(api *fakeDriveFilesAPI, id string){
		"trashed": func(api *fakeDriveFilesAPI, id string) { api.trash(id) },
		"moved":   func(api *fakeDriveFilesAPI, id string) { api.move(id, "other-folder") },
	} {
		t.Run(name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, store documents.Store) {
				ctx, api := tenantCtx(), newFakeDriveFilesAPI()
				c := newTestClient(api, store)
				require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("v1"), ""))
				recorded := getDoc(t, ctx, store, "a.pdf").ObjectID
				displace(api, recorded)

				require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("v2"), ""))
				doc := getDoc(t, ctx, store, "a.pdf")
				assert.NotEqual(t, recorded, doc.ObjectID, "a new file is recorded")
				content, ok := api.contentOf(recorded)
				require.True(t, ok)
				assert.Equal(t, "v1", content, "the trashed or moved file is never written")

				got, _, err := c.Fetch(ctx, folder, "a.pdf", 1<<20)
				require.NoError(t, err)
				assert.Equal(t, "v2", string(got))
			})
		})
	}
}

// A fetch never reads a trashed or moved recorded file: it reads what a
// re-upload would write to (an untagged file of the name placed in the
// folder), else the file is not found (permanent).
func TestDrive_Fetch_RecordedFileTrashedOrMoved_FallsBack(t *testing.T) {
	for name, displace := range map[string]func(api *fakeDriveFilesAPI, id string){
		"trashed": func(api *fakeDriveFilesAPI, id string) { api.trash(id) },
		"moved":   func(api *fakeDriveFilesAPI, id string) { api.move(id, "other-folder") },
	} {
		t.Run(name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, store documents.Store) {
				ctx, api := tenantCtx(), newFakeDriveFilesAPI()
				c := newTestClient(api, store)
				require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("v1"), ""))
				displace(api, getDoc(t, ctx, store, "a.pdf").ObjectID)

				_, _, err := c.Fetch(ctx, folder, "a.pdf", 1<<20)
				require.ErrorIs(t, err, shared.ErrUpstream)
				assert.True(t, shared.IsPermanent(err))
				assert.Contains(t, err.Error(), `no file named "a.pdf"`)

				api.placeFile(folder, "a.pdf", "", []byte("placed"))
				got, _, err := c.Fetch(ctx, folder, "a.pdf", 1<<20)
				require.NoError(t, err)
				assert.Equal(t, "placed", string(got))
			})
		})
	}
}

// A delete leaves a trashed or moved recorded file alone (it never purges
// the trash or reaches outside the folder), nor deletes an untagged file by
// name, and removes the row.
func TestDrive_Delete_RecordedFileTrashedOrMoved_IsLeftAlone(t *testing.T) {
	for name, displace := range map[string]func(api *fakeDriveFilesAPI, id string){
		"trashed": func(api *fakeDriveFilesAPI, id string) { api.trash(id) },
		"moved":   func(api *fakeDriveFilesAPI, id string) { api.move(id, "other-folder") },
	} {
		t.Run(name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, store documents.Store) {
				ctx, api := tenantCtx(), newFakeDriveFilesAPI()
				c := newTestClient(api, store)
				require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("v1"), ""))
				recorded := getDoc(t, ctx, store, "a.pdf").ObjectID
				displace(api, recorded)
				api.placeFile(folder, "a.pdf", "", []byte("placed"))

				require.NoError(t, c.Delete(ctx, folder, "a.pdf"))
				_, ok := api.contentOf(recorded)
				assert.True(t, ok, "the trashed or moved file is not deleted")
				assert.Contains(t, api.contentNamed(folder, "a.pdf"), "placed", "an untagged file is never deleted by name")
				tenant, _ := shared.TenantFromContext(ctx)
				_, found, err := store.Get(ctx, documents.Identity{TenantID: tenant, Provider: providerName, Container: folder, Filename: "a.pdf"})
				require.NoError(t, err)
				assert.False(t, found)
			})
		})
	}
}

// A failed check of the recorded file (files.get) fails the call; nothing
// falls back.
func TestDrive_RecordedFileCheckFailure_IsUpstream(t *testing.T) {
	t.Parallel()
	ctx, api, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
	require.NoError(t, newTestClient(api, store).Upload(ctx, folder, "a.pdf", []byte("v1"), ""))
	api.placeFile(folder, "a.pdf", "", []byte("placed"))
	c := newTestClient(faultyAPI{fakeDriveFilesAPI: api, getErr: errFakeDrive}, store)

	_, _, err := c.Fetch(ctx, folder, "a.pdf", 1<<20)
	require.ErrorIs(t, err, shared.ErrUpstream)
	assert.ErrorIs(t, err, errFakeDrive)

	err = c.Delete(ctx, folder, "a.pdf")
	require.ErrorIs(t, err, shared.ErrUpstream)
	assert.ErrorIs(t, err, errFakeDrive)
	assert.Len(t, api.contentNamed(folder, "a.pdf"), 2, "nothing deleted")
}

// A folder given by an alias never matches a file's parent IDs: the recorded
// file is then found again by its document tag, so the calls still work.
func TestDrive_CheckRecorded(t *testing.T) {
	t.Parallel()
	ctx, api := context.Background(), newFakeDriveFilesAPI()
	id := api.placeFile(folder, "a.pdf", "doc-1", []byte("x"))
	c := newTestClient(api, documents.NewMemoryStore())

	require.NoError(t, c.checkRecorded(ctx, folder, id))
	assert.ErrorIs(t, c.checkRecorded(ctx, "root", id), errDriveNotFound)
	assert.ErrorIs(t, c.checkRecorded(ctx, folder, "gone"), errDriveNotFound)
	api.trash(id)
	err := c.checkRecorded(ctx, folder, id)
	require.ErrorIs(t, err, errDriveNotFound)
	assert.Contains(t, err.Error(), "in the trash")
}

func TestRealDrive_Get(t *testing.T) {
	t.Parallel()
	srv, api := newFakeDriveServer(t)
	ctx := context.Background()
	srv.meta["f1"] = map[string]any{"id": "f1", "mimeType": "application/pdf", "trashed": true, "parents": []string{"folder"},
		"appProperties": map[string]string{appPropertyDocumentID: "doc-1"}}

	f, err := api.get(ctx, "f1")
	require.NoError(t, err)
	assert.Equal(t, &driveFile{ID: "f1", MimeType: "application/pdf", DocumentID: "doc-1", Trashed: true, Parents: []string{"folder"}}, f)
	assert.Equal(t, driveGetFields, srv.lastFields)

	_, err = api.get(ctx, "missing")
	assert.ErrorIs(t, err, errDriveNotFound)

	srv.status["GET locked"] = http.StatusForbidden
	_, err = api.get(ctx, "locked")
	require.Error(t, err)
	assert.NotErrorIs(t, err, errDriveNotFound)
}
