package googledrive

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// A file a person put in the folder is adopted by the first upload of its
// name — updated and tagged, not duplicated — so the delete removes it and a
// fetch afterwards finds nothing.
func TestDrive_UploadOverHandPlacedFile_AdoptsIt(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)
		manual := api.placeFile(folder, "inv.pdf", "", []byte("old manual"))

		require.NoError(t, c.Upload(ctx, folder, "inv.pdf", []byte("new"), "application/pdf"))
		assert.Equal(t, []string{"new"}, api.contentNamed(folder, "inv.pdf"), "one file, with the new content")
		doc := getDoc(t, ctx, store, "inv.pdf")
		assert.Equal(t, manual, doc.ObjectID)
		assert.Equal(t, 1, taggedFiles(api, doc.ID), "the adopted file carries the document's tag")
		creates, _ := api.calls()
		assert.Zero(t, creates)

		content, _, err := c.Fetch(ctx, folder, "inv.pdf", 1<<20)
		require.NoError(t, err)
		assert.Equal(t, "new", string(content))

		require.NoError(t, c.Delete(ctx, folder, "inv.pdf"))
		assert.Zero(t, api.filesNamed(folder, "inv.pdf"), "no same-name file survives the delete")
		_, _, err = c.Fetch(ctx, folder, "inv.pdf", 1<<20)
		require.ErrorIs(t, err, shared.ErrUpstream)
		class, _ := shared.ClassOf(err)
		assert.Equal(t, shared.ClassPermanent, class, "not found, never the stale manual content")
	})
}

// The adopted file is the oldest untagged one; files tagged with another
// document (another tenant's, in a shared folder) and newer untagged copies
// are left alone.
func TestDrive_Adoption_TakesTheOldestUntaggedFile(t *testing.T) {
	t.Parallel()
	ctx, api, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
	c := newTestClient(api, store)
	api.placeFile(folder, "a.pdf", "another-document", []byte("tagged"))
	oldest := api.placeFile(folder, "a.pdf", "", []byte("first copy"))
	api.placeFile(folder, "a.pdf", "", []byte("second copy"))

	require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("new"), ""))
	assert.Equal(t, oldest, getDoc(t, ctx, store, "a.pdf").ObjectID)
	assert.Equal(t, []string{"tagged", "new", "second copy"}, api.contentNamed(folder, "a.pdf"))
	assert.Equal(t, 1, taggedFiles(api, "another-document"))
}

// A fetch with no registry row reads only an untagged file: a tagged file
// belongs to another document's row.
func TestDrive_FetchByName_SkipsTaggedFiles(t *testing.T) {
	t.Parallel()
	ctx, api := tenantCtx(), newFakeDriveFilesAPI()
	c := newTestClient(api, documents.NewMemoryStore())
	api.placeFile(folder, "a.pdf", "another-document", []byte("someone else's"))

	_, _, err := c.Fetch(ctx, folder, "a.pdf", 1<<20)
	require.ErrorIs(t, err, shared.ErrUpstream)
	assert.Contains(t, err.Error(), `no file named "a.pdf"`)

	api.placeFile(folder, "a.pdf", "", []byte("placed by a person"))
	content, _, err := c.Fetch(ctx, folder, "a.pdf", 1<<20)
	require.NoError(t, err)
	assert.Equal(t, "placed by a person", string(content))
}

// A document that has its own file never deletes a same-name file a person
// placed later: that file was never the document's.
func TestDrive_Delete_LeavesFilesTheDocumentNeverHeld(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)
		require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("document"), ""))
		api.placeFile(folder, "a.pdf", "", []byte("placed later"))

		require.NoError(t, c.Delete(ctx, folder, "a.pdf"))
		assert.Equal(t, []string{"placed later"}, api.contentNamed(folder, "a.pdf"))
	})
}

// A file found for adoption but deleted before the update is replaced by a
// new file; any other update failure fails the upload without creating.
func TestDrive_Adoption_UpdateFailures(t *testing.T) {
	t.Parallel()

	t.Run("gone", func(t *testing.T) {
		t.Parallel()
		ctx, fake, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
		fake.placeFile(folder, "a.pdf", "", []byte("manual"))
		c := newTestClient(faultyAPI{fakeDriveFilesAPI: fake, updateErr: errDriveNotFound}, store)

		require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("new"), ""))
		creates, _ := fake.calls()
		assert.Equal(t, 1, creates)
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()
		ctx, fake, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
		fake.placeFile(folder, "a.pdf", "", []byte("manual"))
		c := newTestClient(faultyAPI{fakeDriveFilesAPI: fake, updateErr: errFakeDrive}, store)

		require.ErrorIs(t, c.Upload(ctx, folder, "a.pdf", []byte("new"), ""), shared.ErrUpstream)
		creates, _ := fake.calls()
		assert.Zero(t, creates)
		assert.Equal(t, documents.StateFailed, getDoc(t, ctx, store, "a.pdf").State)
	})
}

// A failing by-name lookup fails the upload, the fetch and the delete — the
// delete releasing its row — rather than guessing.
func TestDrive_ListByNameFailure(t *testing.T) {
	t.Parallel()
	ctx, fake, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
	c := newTestClient(faultyAPI{fakeDriveFilesAPI: fake, listByNameErr: errFakeDrive}, store)

	require.ErrorIs(t, c.Upload(ctx, folder, "a.pdf", []byte("x"), ""), errFakeDrive)
	assert.Equal(t, documents.StateFailed, getDoc(t, ctx, store, "a.pdf").State)

	_, _, err := c.Fetch(ctx, folder, "b.pdf", 1<<20)
	require.ErrorIs(t, err, errFakeDrive)

	require.ErrorIs(t, c.Delete(ctx, folder, "b.pdf"), errFakeDrive)
	assert.Equal(t, documents.StateFailed, getDoc(t, ctx, store, "b.pdf").State)
}

// Concurrent uploads and deletes of a hand-placed file's name from separate
// replicas (own clients, one registry, one Drive) always converge: a row is
// AVAILABLE with exactly one same-name file — its recorded one — or there is
// no row and no file. Losers get a transient error (InProgressError, or claim
// contention with a concurrent delete) and retry, as the worker would.
func TestDrive_HandPlacedFile_ConcurrentUploadAndDelete_Converge(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		for round := range 20 {
			ctx, api := tenantCtx(), newFakeDriveFilesAPI()
			name := fmt.Sprintf("r%d.pdf", round)
			api.placeFile(folder, name, "", []byte("manual"))

			var wg sync.WaitGroup
			errs := make(chan error, 6)
			for i := range 6 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					c := newTestClient(api, store) // a replica
					for {
						var err error
						if i%2 == 0 {
							err = c.Upload(ctx, folder, name, []byte("new"), "")
						} else {
							err = c.Delete(ctx, folder, name)
						}
						// The worker retries what is transient: a call in
						// progress, or a claim that met a concurrent delete.
						if !shared.IsTransient(err) {
							errs <- err
							return
						}
					}
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}

			tenant, _ := shared.TenantFromContext(ctx)
			doc, found, err := store.Get(ctx, documents.Identity{TenantID: tenant, Provider: providerName, Container: folder, Filename: name})
			require.NoError(t, err)
			files := api.contentNamed(folder, name)
			if found {
				assert.Equal(t, documents.StateAvailable, doc.State)
				assert.Equal(t, []string{"new"}, files, "round %d", round)
				content, _, err := newTestClient(api, store).Fetch(ctx, folder, name, 1<<20)
				require.NoError(t, err)
				assert.Equal(t, "new", string(content))
			} else {
				assert.Empty(t, files, "round %d: no row, no file", round)
			}
		}
	})
}

// With the recorded file gone, the oldest file tagged with the document is
// adopted and every other tagged leftover removed.
func TestDrive_TaggedAdoption_RemovesOtherLeftovers(t *testing.T) {
	t.Parallel()
	ctx, api, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
	c := newTestClient(api, store)
	require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("v1"), ""))
	doc := getDoc(t, ctx, store, "a.pdf")
	require.NoError(t, api.delete(ctx, doc.ObjectID)) // removed in the Drive UI
	first := api.placeFile(folder, "a.pdf", doc.ID, []byte("leftover 1"))
	api.placeFile(folder, "a.pdf", doc.ID, []byte("leftover 2"))

	require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("v2"), ""))
	assert.Equal(t, first, getDoc(t, ctx, store, "a.pdf").ObjectID)
	assert.Equal(t, []string{"v2"}, api.contentNamed(folder, "a.pdf"))
}
