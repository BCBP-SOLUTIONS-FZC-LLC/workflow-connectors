package googledrive

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

const folder = "folder-1"

func forEachStore(t *testing.T, fn func(t *testing.T, store documents.Store)) {
	for _, sc := range storeCases(t) {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			fn(t, sc.new(t))
		})
	}
}

func getDoc(t *testing.T, ctx context.Context, store documents.Store, name string) documents.Document {
	t.Helper()
	tenant, ok := shared.TenantFromContext(ctx)
	require.True(t, ok)
	doc, found, err := store.Get(ctx, documents.Identity{TenantID: tenant, Provider: providerName, Container: folder, Filename: name})
	require.NoError(t, err)
	require.True(t, found, "document row for %q", name)
	return doc
}

// ---- Basic behaviour --------------------------------------------------------

func TestDrive_UploadThenFetch_RoundTrips(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)

		require.NoError(t, c.Upload(ctx, folder, "a.txt", []byte("hello"), "text/plain"))
		content, ct, err := c.Fetch(ctx, folder, "a.txt", 50<<20)
		require.NoError(t, err)
		assert.Equal(t, "hello", string(content))
		assert.Equal(t, "text/plain", ct)

		doc := getDoc(t, ctx, store, "a.txt")
		assert.Equal(t, documents.StateAvailable, doc.State)
		assert.NotEmpty(t, doc.ObjectID)
		assert.Empty(t, doc.Owner)
	})
}

func TestDrive_ReUpload_ReplacesSameFileInPlace(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)

		require.NoError(t, c.Upload(ctx, folder, "a.txt", []byte("v1"), "text/plain"))
		first := getDoc(t, ctx, store, "a.txt")
		require.NoError(t, c.Upload(ctx, folder, "a.txt", []byte("v2"), "text/plain"))
		second := getDoc(t, ctx, store, "a.txt")

		assert.Equal(t, first.ID, second.ID, "one canonical document")
		assert.Equal(t, first.ObjectID, second.ObjectID, "the same Drive file is replaced")
		assert.Equal(t, 1, api.filesNamed(folder, "a.txt"))
		content, _, err := c.Fetch(ctx, folder, "a.txt", 50<<20)
		require.NoError(t, err)
		assert.Equal(t, "v2", string(content))
	})
}

func TestDrive_MissingTenant_IsRejected(t *testing.T) {
	t.Parallel()
	c := newTestClient(newFakeDriveFilesAPI(), documents.NewMemoryStore())

	err := c.Upload(context.Background(), folder, "a.txt", []byte("x"), "")
	assert.ErrorIs(t, err, shared.ErrMissingTenant)
	_, _, err = c.Fetch(context.Background(), folder, "a.txt", 50<<20)
	assert.ErrorIs(t, err, shared.ErrMissingTenant)
}

func TestNewDriveStorageClient_Validation(t *testing.T) {
	t.Parallel()

	_, err := newDriveStorageClient(context.Background(), map[string]any{}, documents.NewMemoryStore())
	assert.ErrorIs(t, err, shared.ErrValidation)

	_, err = newDriveStorageClient(context.Background(), map[string]any{"driveServiceAccountKey": "{}"}, nil)
	assert.ErrorIs(t, err, shared.ErrValidation, "a missing document store is a configuration error")
}

// ---- Scenario 1: two simultaneous uploads of one filename -------------------

func TestDrive_TwoSimultaneousUploads_OneWinsOneGetsInProgress(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)

		entered := make(chan struct{})
		api.createdHook = func() { close(entered) }
		api.createGate = make(chan struct{})

		first := make(chan error, 1)
		go func() { first <- c.Upload(ctx, folder, "report.pdf", []byte("A"), "application/pdf") }()
		<-entered // the first upload owns the document and is writing to Drive

		err := c.Upload(ctx, folder, "report.pdf", []byte("B"), "application/pdf")
		var inProgress *documents.InProgressError
		require.ErrorAs(t, err, &inProgress, "the second upload gets a deterministic in-progress result")
		assert.ErrorIs(t, err, documents.ErrUploadInProgress)
		assert.ErrorIs(t, err, shared.ErrUpstream, "in-progress is retryable")

		close(api.createGate)
		require.NoError(t, <-first)

		doc := getDoc(t, ctx, store, "report.pdf")
		assert.Equal(t, doc.ID, inProgress.DocumentID, "the error names the canonical document")
		assert.Equal(t, 1, api.filesNamed(folder, "report.pdf"))
	})
}

// ---- Scenario 2: second request while an upload is in progress --------------

func TestDrive_UploadInProgress_SecondRequestNeverTouchesDrive(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)

		entered := make(chan struct{})
		api.createdHook = func() { close(entered) }
		api.createGate = make(chan struct{})
		first := make(chan error, 1)
		go func() { first <- c.Upload(ctx, folder, "x.bin", []byte("1"), "") }()
		<-entered

		for range 5 {
			err := c.Upload(ctx, folder, "x.bin", []byte("2"), "")
			assert.ErrorIs(t, err, documents.ErrUploadInProgress)
		}
		_, _, fetchErr := c.Fetch(ctx, folder, "x.bin", 50<<20)
		assert.ErrorIs(t, fetchErr, documents.ErrUploadInProgress, "nothing to read until the first upload lands")
		assert.ErrorIs(t, c.Delete(ctx, folder, "x.bin"), documents.ErrUploadInProgress)

		creates, updates := api.calls()
		assert.Equal(t, 1, creates, "only the owner reaches Drive")
		assert.Zero(t, updates)

		close(api.createGate)
		require.NoError(t, <-first)
		assert.Equal(t, 1, api.filesNamed(folder, "x.bin"))
	})
}

// ---- Scenario 3: failure after reservation, then retry ----------------------

func TestDrive_UploadFailsAfterReservation_FailedThenRetryReusesRow(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)

		api.failCreates = 1
		err := c.Upload(ctx, folder, "f.txt", []byte("data"), "text/plain")
		require.Error(t, err)
		assert.ErrorIs(t, err, shared.ErrUpstream)

		failed := getDoc(t, ctx, store, "f.txt")
		assert.Equal(t, documents.StateFailed, failed.State)
		assert.Contains(t, failed.LastError, "injected failure", "the failure is recorded for audit")
		assert.Equal(t, 1, failed.FailedAttempts)
		assert.Empty(t, failed.Owner, "a FAILED row is not owned")

		require.NoError(t, c.Upload(ctx, folder, "f.txt", []byte("data"), "text/plain"))
		retried := getDoc(t, ctx, store, "f.txt")
		assert.Equal(t, failed.ID, retried.ID, "the retry reuses the FAILED row")
		assert.Equal(t, documents.StateAvailable, retried.State)
		assert.Empty(t, retried.LastError)
		assert.Equal(t, 1, retried.FailedAttempts, "the failure count is kept")
		assert.Equal(t, 1, api.filesNamed(folder, "f.txt"))

		if mem, ok := store.(*documents.MemoryStore); ok {
			var outcomes []string
			for _, a := range mem.Attempts() {
				if a.DocumentID == retried.ID {
					outcomes = append(outcomes, a.Outcome)
				}
			}
			assert.Equal(t, []string{"failed", "available"}, outcomes)
		}
	})
}

func TestDrive_CrashAfterDriveCreate_RetryAdoptsFileInsteadOfDuplicating(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		tenant, _ := shared.TenantFromContext(ctx)
		identity := documents.Identity{TenantID: tenant, Provider: providerName, Container: folder, Filename: "c.txt"}

		// A worker claimed the document, created the Drive file, and died
		// before recording it. Its short lease then expires. The lease must
		// outlast the steps that expect it live, even under -race on CI.
		const lease = time.Second
		start := time.Now()
		doc, claimed, err := store.Claim(ctx, identity, "crashed-attempt", lease)
		require.NoError(t, err)
		require.True(t, claimed)
		_, err = store.Advance(ctx, doc.ID, "crashed-attempt", documents.StateUploading)
		require.NoError(t, err)
		orphan := api.placeFile(folder, "c.txt", doc.ID, []byte("partial"))

		c := newTestClient(api, store)
		assert.ErrorIs(t, c.Upload(ctx, folder, "c.txt", []byte("full"), "text/plain"), documents.ErrUploadInProgress,
			"a live lease still protects the document")

		time.Sleep(time.Until(start.Add(lease + 250*time.Millisecond)))
		require.NoError(t, c.Upload(ctx, folder, "c.txt", []byte("full"), "text/plain"))

		got := getDoc(t, ctx, store, "c.txt")
		assert.Equal(t, orphan, got.ObjectID, "the retry adopts the crashed attempt's file")
		assert.Equal(t, 1, api.filesNamed(folder, "c.txt"))
		creates, _ := api.calls()
		assert.Zero(t, creates)
		content, _, err := c.Fetch(ctx, folder, "c.txt", 50<<20)
		require.NoError(t, err)
		assert.Equal(t, "full", string(content))
	})
}

func TestDrive_StaleOwner_CannotRecordAfterTakeover(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx := tenantCtx()
		tenant, _ := shared.TenantFromContext(ctx)
		identity := documents.Identity{TenantID: tenant, Provider: providerName, Container: folder, Filename: "s.txt"}

		doc, _, err := store.Claim(ctx, identity, "slow", 50*time.Millisecond)
		require.NoError(t, err)
		time.Sleep(100 * time.Millisecond)
		_, claimed, err := store.Claim(ctx, identity, "new-owner", time.Minute)
		require.NoError(t, err)
		require.True(t, claimed, "an expired lease can be taken over")

		_, err = store.Complete(ctx, doc.ID, "slow", documents.Result{ObjectID: "late"})
		assert.ErrorIs(t, err, documents.ErrOwnershipLost, "the previous owner must not overwrite the new owner's state")
		_, err = store.Fail(ctx, doc.ID, "slow", "late")
		assert.ErrorIs(t, err, documents.ErrOwnershipLost)
	})
}

// ---- Scenario 4: different filenames ----------------------------------------

func TestDrive_DifferentFilenames_UploadIndependently(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)

		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, name := range []string{"one.txt", "two.txt"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = c.Upload(ctx, folder, name, []byte(name), "text/plain")
			}()
		}
		wg.Wait()

		require.NoError(t, errs[0])
		require.NoError(t, errs[1])
		assert.Equal(t, 1, api.filesNamed(folder, "one.txt"))
		assert.Equal(t, 1, api.filesNamed(folder, "two.txt"))
		assert.NotEqual(t, getDoc(t, ctx, store, "one.txt").ID, getDoc(t, ctx, store, "two.txt").ID)
	})
}

func TestDrive_SameFilenameDifferentTenants_AreSeparateDocuments(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		api := newFakeDriveFilesAPI()
		c := newTestClient(api, store)
		ctxA, ctxB := tenantCtx(), tenantCtx()

		require.NoError(t, c.Upload(ctxA, folder, "same.txt", []byte("a"), ""))
		require.NoError(t, c.Upload(ctxB, folder, "same.txt", []byte("b"), ""))
		assert.NotEqual(t, getDoc(t, ctxA, store, "same.txt").ID, getDoc(t, ctxB, store, "same.txt").ID)
	})
}

// ---- Scenario 5: high concurrency on one filename ---------------------------

func TestDrive_HighConcurrency_ExactlyOneCanonicalDocument(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)
		api.createdHook = func() { time.Sleep(20 * time.Millisecond) } // widen the race window

		const uploaders = 24
		start := make(chan struct{})
		errs := make([]error, uploaders)
		var wg sync.WaitGroup
		for i := range uploaders {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = c.Upload(ctx, folder, "hot.pdf", []byte(fmt.Sprintf("v%d", i)), "application/pdf")
			}()
		}
		close(start)
		wg.Wait()

		succeeded := 0
		for _, err := range errs {
			if err == nil {
				succeeded++
				continue
			}
			assert.ErrorIs(t, err, documents.ErrUploadInProgress, "every loser gets the deterministic in-progress result")
		}
		assert.GreaterOrEqual(t, succeeded, 1)
		assert.Equal(t, 1, api.filesNamed(folder, "hot.pdf"), "exactly one Drive file")
		creates, _ := api.calls()
		assert.Equal(t, 1, creates, "exactly one upload ever created the file")

		doc := getDoc(t, ctx, store, "hot.pdf")
		assert.Equal(t, documents.StateAvailable, doc.State)
		if mem, ok := store.(*documents.MemoryStore); ok {
			assert.Equal(t, 1, mem.Len(), "exactly one canonical document record")
		}
	})
}

// ---- Delete and files outside the registry ---------------------------------

func TestDrive_Delete_RemovesFileAndRow(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)

		require.NoError(t, c.Upload(ctx, folder, "d.txt", []byte("x"), ""))
		require.NoError(t, c.Delete(ctx, folder, "d.txt"))

		assert.Zero(t, api.filesNamed(folder, "d.txt"))
		tenant, _ := shared.TenantFromContext(ctx)
		_, found, err := store.Get(ctx, documents.Identity{TenantID: tenant, Provider: providerName, Container: folder, Filename: "d.txt"})
		require.NoError(t, err)
		assert.False(t, found)

		require.NoError(t, c.Delete(ctx, folder, "d.txt"), "deleting an absent document is idempotent")
		require.NoError(t, c.Upload(ctx, folder, "d.txt", []byte("again"), ""), "a deleted name can be uploaded again")
	})
}

func TestDrive_FileOutsideRegistry_FetchAndDeleteByName(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)
		api.placeFile(folder, "manual.pdf", "", []byte("put there by a person"))

		content, _, err := c.Fetch(ctx, folder, "manual.pdf", 50<<20)
		require.NoError(t, err)
		assert.Equal(t, "put there by a person", string(content))

		require.NoError(t, c.Delete(ctx, folder, "manual.pdf"))
		assert.Zero(t, api.filesNamed(folder, "manual.pdf"))
	})
}

func TestDrive_UnregisteredDelete_LeavesTaggedFilesAlone(t *testing.T) {
	t.Parallel()
	ctx, api := tenantCtx(), newFakeDriveFilesAPI()
	c := newTestClient(api, documents.NewMemoryStore())
	api.placeFile(folder, "t.txt", "some-document-id", []byte("x"))

	require.NoError(t, c.Delete(ctx, folder, "t.txt"))
	assert.Equal(t, 1, api.filesNamed(folder, "t.txt"), "a registry-tagged file is never deleted by name")
}

func TestDrive_DriveError_MarksFailedAndIsUpstream(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)
		api.err = errors.New("drive down")

		err := c.Upload(ctx, folder, "e.txt", []byte("x"), "")
		require.Error(t, err)
		assert.ErrorIs(t, err, shared.ErrUpstream)
		assert.Equal(t, documents.StateFailed, getDoc(t, ctx, store, "e.txt").State)
	})
}

func TestDrive_RecordedFileDeletedOutsideConnector_ReuploadCreatesNewFile(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)

		require.NoError(t, c.Upload(ctx, folder, "g.txt", []byte("v1"), ""))
		first := getDoc(t, ctx, store, "g.txt")
		require.NoError(t, api.delete(ctx, first.ObjectID)) // removed in the Drive UI

		require.NoError(t, c.Upload(ctx, folder, "g.txt", []byte("v2"), ""))
		second := getDoc(t, ctx, store, "g.txt")
		assert.Equal(t, first.ID, second.ID)
		assert.NotEqual(t, first.ObjectID, second.ObjectID)
		assert.Equal(t, 1, api.filesNamed(folder, "g.txt"))
	})
}
