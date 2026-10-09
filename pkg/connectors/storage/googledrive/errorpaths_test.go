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

var errRegistry = errors.New("registry unavailable")

// faultyStore fails the registry calls whose error is set and passes the
// rest through to the wrapped store.
type faultyStore struct {
	documents.Store
	claimErr, claimExistingErr, advanceErr, removeErr, getErr error
}

func (s faultyStore) Claim(ctx context.Context, id documents.Identity, attempt string, lease time.Duration) (documents.Document, bool, error) {
	if s.claimErr != nil {
		return documents.Document{}, false, s.claimErr
	}
	return s.Store.Claim(ctx, id, attempt, lease)
}

func (s faultyStore) ClaimExisting(ctx context.Context, id documents.Identity, attempt string, lease time.Duration) (documents.Document, bool, error) {
	if s.claimExistingErr != nil {
		return documents.Document{}, false, s.claimExistingErr
	}
	return s.Store.ClaimExisting(ctx, id, attempt, lease)
}

func (s faultyStore) Advance(ctx context.Context, docID, attempt string, to documents.State) (documents.Document, error) {
	if s.advanceErr != nil {
		return documents.Document{}, s.advanceErr
	}
	return s.Store.Advance(ctx, docID, attempt, to)
}

func (s faultyStore) Remove(ctx context.Context, docID, attempt string) error {
	if s.removeErr != nil {
		return s.removeErr
	}
	return s.Store.Remove(ctx, docID, attempt)
}

func (s faultyStore) Get(ctx context.Context, id documents.Identity) (documents.Document, bool, error) {
	if s.getErr != nil {
		return documents.Document{}, false, s.getErr
	}
	return s.Store.Get(ctx, id)
}

// faultyAPI fails the Drive calls whose error is set and passes the rest
// through to the fake Drive.
type faultyAPI struct {
	*fakeDriveFilesAPI
	listErr, listByNameErr, updateErr, deleteErr error
}

func (a faultyAPI) listByName(ctx context.Context, folderID, name string) ([]*driveFile, error) {
	if a.listByNameErr != nil {
		return nil, a.listByNameErr
	}
	return a.fakeDriveFilesAPI.listByName(ctx, folderID, name)
}

func (a faultyAPI) update(ctx context.Context, fileID, contentType, documentID string, content []byte) error {
	if a.updateErr != nil {
		return a.updateErr
	}
	return a.fakeDriveFilesAPI.update(ctx, fileID, contentType, documentID, content)
}

func (a faultyAPI) listByDocumentID(ctx context.Context, folderID, documentID string) ([]*driveFile, error) {
	if a.listErr != nil {
		return nil, a.listErr
	}
	return a.fakeDriveFilesAPI.listByDocumentID(ctx, folderID, documentID)
}

func (a faultyAPI) delete(ctx context.Context, fileID string) error {
	if a.deleteErr != nil {
		return a.deleteErr
	}
	return a.fakeDriveFilesAPI.delete(ctx, fileID)
}

// A registry failure surfaces as ErrUpstream from every call path, and the
// failed row is left for a retry.
func TestDrive_RegistryFailures_AreUpstream(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		store func(documents.Store) documents.Store
		call  func(ctx context.Context, c *driveStorageClient) error
		msg   string
	}{
		"upload claim": {
			store: func(s documents.Store) documents.Store { return faultyStore{Store: s, claimErr: errRegistry} },
			call: func(ctx context.Context, c *driveStorageClient) error {
				return c.Upload(ctx, folder, "a.pdf", []byte("x"), "")
			},
			msg: `drive upload "a.pdf": claim`,
		},
		"upload advance": {
			store: func(s documents.Store) documents.Store { return faultyStore{Store: s, advanceErr: errRegistry} },
			call: func(ctx context.Context, c *driveStorageClient) error {
				return c.Upload(ctx, folder, "a.pdf", []byte("x"), "")
			},
			msg: `drive upload "a.pdf"`,
		},
		"fetch get": {
			store: func(s documents.Store) documents.Store { return faultyStore{Store: s, getErr: errRegistry} },
			call: func(ctx context.Context, c *driveStorageClient) error {
				_, _, err := c.Fetch(ctx, folder, "a.pdf", 1<<20)
				return err
			},
			msg: `drive fetch "a.pdf"`,
		},
		"delete claim": {
			store: func(s documents.Store) documents.Store { return faultyStore{Store: s, claimErr: errRegistry} },
			call: func(ctx context.Context, c *driveStorageClient) error {
				return c.Delete(ctx, folder, "a.pdf")
			},
			msg: `drive delete "a.pdf": claim`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, api := tenantCtx(), newFakeDriveFilesAPI()
			err := tc.call(ctx, newTestClient(api, tc.store(documents.NewMemoryStore())))
			require.ErrorIs(t, err, shared.ErrUpstream)
			require.ErrorIs(t, err, errRegistry)
			assert.Contains(t, err.Error(), tc.msg)
			creates, updates := api.calls()
			assert.Zero(t, creates+updates, "Drive is never written when the registry fails first")
		})
	}
}

// A validation error from the registry stays validation-only, never ErrUpstream.
func TestDrive_RegistryValidationError_IsNotUpstream(t *testing.T) {
	t.Parallel()
	ctx := tenantCtx()
	invalid := fmt.Errorf("%w: filename too long", shared.ErrValidation)
	c := newTestClient(newFakeDriveFilesAPI(), faultyStore{Store: documents.NewMemoryStore(), claimErr: invalid})

	err := c.Upload(ctx, folder, "a.pdf", []byte("x"), "")
	require.ErrorIs(t, err, shared.ErrValidation)
	assert.NotErrorIs(t, err, shared.ErrUpstream)
}

func TestDrive_Delete_AdvanceAndRemoveFailures(t *testing.T) {
	t.Parallel()

	t.Run("advance", func(t *testing.T) {
		t.Parallel()
		ctx, api, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
		require.NoError(t, newTestClient(api, store).Upload(ctx, folder, "a.pdf", []byte("x"), ""))

		err := newTestClient(api, faultyStore{Store: store, advanceErr: errRegistry}).Delete(ctx, folder, "a.pdf")
		require.ErrorIs(t, err, shared.ErrUpstream)
		assert.Equal(t, 1, api.filesNamed(folder, "a.pdf"), "nothing is deleted without the row in DELETING")
	})

	t.Run("remove", func(t *testing.T) {
		t.Parallel()
		ctx, api, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
		require.NoError(t, newTestClient(api, store).Upload(ctx, folder, "a.pdf", []byte("x"), ""))

		err := newTestClient(api, faultyStore{Store: store, removeErr: errRegistry}).Delete(ctx, folder, "a.pdf")
		require.ErrorIs(t, err, shared.ErrUpstream)
		assert.Contains(t, err.Error(), `drive delete "a.pdf": record`)
		assert.Zero(t, api.filesNamed(folder, "a.pdf"), "the file was deleted before the record failed")
	})
}

func TestDrive_Delete_MissingTenant_IsRejected(t *testing.T) {
	t.Parallel()
	c := newTestClient(newFakeDriveFilesAPI(), documents.NewMemoryStore())
	assert.ErrorIs(t, c.Delete(context.Background(), folder, "a.pdf"), shared.ErrMissingTenant)
}

// Re-uploading a recorded file whose update fails with anything but "not
// found" fails the upload; it does not create a second file.
func TestDrive_ReUpload_UpdateFailure_FailsWithoutCreating(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)
		require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("v1"), ""))

		api.mu.Lock()
		api.err = errFakeDrive
		api.mu.Unlock()
		err := c.Upload(ctx, folder, "a.pdf", []byte("v2"), "")
		require.ErrorIs(t, err, shared.ErrUpstream)

		creates, updates := api.calls()
		assert.Equal(t, 1, creates)
		assert.Equal(t, 1, updates)
		assert.Equal(t, documents.StateFailed, getDoc(t, ctx, store, "a.pdf").State)
	})
}

// The leftover cleanup after a create is best effort: a failing list does not
// fail the upload.
func TestDrive_Create_LeftoverListFailure_IsIgnored(t *testing.T) {
	t.Parallel()
	ctx, fake, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
	c := newTestClient(faultyAPI{fakeDriveFilesAPI: fake, listErr: errFakeDrive}, store)

	require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("x"), ""))
	assert.Equal(t, documents.StateAvailable, docFor(t, ctx, store, "a.pdf").State)
	assert.Equal(t, 1, fake.filesNamed(folder, "a.pdf"))
}

// A row whose upload failed before any file was written has nothing to fetch:
// a permanent error, not a lookup by name.
func TestDrive_Fetch_FailedRowWithoutFile_IsPermanent(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		api.failCreates = 1
		c := newTestClient(api, store)
		require.Error(t, c.Upload(ctx, folder, "a.pdf", []byte("x"), ""))

		_, _, err := c.Fetch(ctx, folder, "a.pdf", 1<<20)
		require.ErrorIs(t, err, shared.ErrUpstream)
		class, _ := shared.ClassOf(err)
		assert.Equal(t, shared.ClassPermanent, class)
		assert.Contains(t, err.Error(), "has no stored file (state FAILED)")
	})
}

func TestDrive_Fetch_Unregistered_LookupFailures(t *testing.T) {
	t.Parallel()

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		_, _, err := newTestClient(newFakeDriveFilesAPI(), documents.NewMemoryStore()).Fetch(tenantCtx(), folder, "none.pdf", 1<<20)
		require.ErrorIs(t, err, shared.ErrUpstream)
		class, _ := shared.ClassOf(err)
		assert.Equal(t, shared.ClassPermanent, class)
		assert.Contains(t, err.Error(), `no file named "none.pdf"`)
	})

	t.Run("drive error", func(t *testing.T) {
		t.Parallel()
		api := newFakeDriveFilesAPI()
		api.err = errFakeDrive
		_, _, err := newTestClient(api, documents.NewMemoryStore()).Fetch(tenantCtx(), folder, "a.pdf", 1<<20)
		require.ErrorIs(t, err, shared.ErrUpstream)
		assert.Contains(t, err.Error(), `drive fetch "a.pdf"`)
	})
}

func TestDrive_Delete_Unregistered_Failures(t *testing.T) {
	t.Parallel()

	t.Run("lookup", func(t *testing.T) {
		t.Parallel()
		api := newFakeDriveFilesAPI()
		api.err = errFakeDrive
		err := newTestClient(api, documents.NewMemoryStore()).Delete(tenantCtx(), folder, "a.pdf")
		require.ErrorIs(t, err, shared.ErrUpstream)
	})

	t.Run("delete", func(t *testing.T) {
		t.Parallel()
		fake := newFakeDriveFilesAPI()
		fake.placeFile(folder, "a.pdf", "", []byte("x"))
		err := newTestClient(faultyAPI{fakeDriveFilesAPI: fake, deleteErr: errFakeDrive}, documents.NewMemoryStore()).
			Delete(tenantCtx(), folder, "a.pdf")
		require.ErrorIs(t, err, shared.ErrUpstream)
		assert.Equal(t, 1, fake.filesNamed(folder, "a.pdf"))
	})

	t.Run("already gone", func(t *testing.T) {
		t.Parallel()
		fake := newFakeDriveFilesAPI()
		fake.placeFile(folder, "a.pdf", "", []byte("x"))
		err := newTestClient(faultyAPI{fakeDriveFilesAPI: fake, deleteErr: errDriveNotFound}, documents.NewMemoryStore()).
			Delete(tenantCtx(), folder, "a.pdf")
		assert.NoError(t, err, "a file deleted concurrently is already gone")
	})
}

// A registry failure after a successful claim releases the row at once
// (FAILED), so a retry is not refused as in progress until the lease expires.
func TestDrive_AdvanceFailure_ReleasesTheRow(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()

		err := newTestClient(api, faultyStore{Store: store, advanceErr: errRegistry}).Upload(ctx, folder, "u.pdf", []byte("x"), "")
		require.ErrorIs(t, err, errRegistry)
		assert.Equal(t, documents.StateFailed, getDoc(t, ctx, store, "u.pdf").State)
		require.NoError(t, newTestClient(api, store).Upload(ctx, folder, "u.pdf", []byte("x"), ""), "the retry claims the row at once")

		err = newTestClient(api, faultyStore{Store: store, advanceErr: errRegistry}).Delete(ctx, folder, "u.pdf")
		require.ErrorIs(t, err, errRegistry)
		assert.Equal(t, documents.StateFailed, getDoc(t, ctx, store, "u.pdf").State)
		assert.Equal(t, 1, api.filesNamed(folder, "u.pdf"), "nothing is deleted without the row in DELETING")
		require.NoError(t, newTestClient(api, store).Delete(ctx, folder, "u.pdf"), "the retry claims the row at once")
		assert.Zero(t, api.filesNamed(folder, "u.pdf"))
	})
}

// A delete whose files are gone but whose row could not be removed releases
// the row; the retry finds nothing left to delete and removes it.
func TestDrive_RemoveFailure_RetrySucceeds(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		require.NoError(t, newTestClient(api, store).Upload(ctx, folder, "r.pdf", []byte("x"), ""))

		err := newTestClient(api, faultyStore{Store: store, removeErr: errRegistry}).Delete(ctx, folder, "r.pdf")
		require.ErrorIs(t, err, errRegistry)
		assert.Zero(t, api.filesNamed(folder, "r.pdf"))
		assert.Equal(t, documents.StateFailed, getDoc(t, ctx, store, "r.pdf").State, "released, not DELETING")

		// A person puts a same-name file in the folder before the retry: it
		// never belonged to the document, so the retry leaves it alone.
		api.placeFile(folder, "r.pdf", "", []byte("placed later"))
		require.NoError(t, newTestClient(api, store).Delete(ctx, folder, "r.pdf"))
		tenant, _ := shared.TenantFromContext(ctx)
		_, found, err := store.Get(ctx, documents.Identity{TenantID: tenant, Provider: providerName, Container: folder, Filename: "r.pdf"})
		require.NoError(t, err)
		assert.False(t, found)
		assert.Equal(t, []string{"placed later"}, api.contentNamed(folder, "r.pdf"))
	})
}

// recordingStore records the context of every registry write made after a
// Drive call, and cancels the caller's context first (from Advance), so the
// writes must not depend on it.
type recordingStore struct {
	documents.Store
	cancelCaller context.CancelFunc
	failAdvance  bool

	mu   *sync.Mutex
	ctxs map[string]ctxSnapshot
}

// ctxSnapshot is a context's state when the registry was called.
type ctxSnapshot struct {
	err         error
	deadline    time.Time
	hasDeadline bool
}

func (s recordingStore) record(op string, ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	deadline, ok := ctx.Deadline()
	s.ctxs[op] = ctxSnapshot{err: ctx.Err(), deadline: deadline, hasDeadline: ok}
}

func (s recordingStore) Advance(ctx context.Context, docID, attempt string, to documents.State) (documents.Document, error) {
	s.cancelCaller()
	if s.failAdvance {
		return documents.Document{}, errRegistry
	}
	return s.Store.Advance(context.WithoutCancel(ctx), docID, attempt, to)
}

func (s recordingStore) Complete(ctx context.Context, docID, attempt string, r documents.Result) (documents.Document, error) {
	s.record("complete", ctx)
	return s.Store.Complete(ctx, docID, attempt, r)
}

func (s recordingStore) Fail(ctx context.Context, docID, attempt, reason string) (documents.Document, error) {
	s.record("fail", ctx)
	return s.Store.Fail(ctx, docID, attempt, reason)
}

func (s recordingStore) Remove(ctx context.Context, docID, attempt string) error {
	s.record("remove", ctx)
	return s.Store.Remove(ctx, docID, attempt)
}

// Registry writes after a Drive call (and the release after a failed
// transition) run detached from the caller's cancellation but bounded by
// recordTimeout.
func TestDrive_RegistryWritesAfterDrive_AreDetachedAndBounded(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, snap ctxSnapshot, seen bool) {
		t.Helper()
		require.True(t, seen, "the registry write was made")
		assert.NoError(t, snap.err, "not cancelled with the caller")
		require.True(t, snap.hasDeadline, "bounded")
		assert.WithinDuration(t, time.Now().Add(recordTimeout), snap.deadline, 2*time.Second)
	}
	run := func(failAdvance bool, call func(ctx context.Context, c *driveStorageClient) error) map[string]ctxSnapshot {
		base := documents.NewMemoryStore()
		ctx, cancel := context.WithCancel(tenantCtx())
		defer cancel()
		store := recordingStore{Store: base, cancelCaller: cancel, failAdvance: failAdvance, mu: &sync.Mutex{}, ctxs: map[string]ctxSnapshot{}}
		api := newFakeDriveFilesAPI()
		require.NoError(t, newTestClient(api, base).Upload(tenantCtxFrom(ctx), folder, "seed.pdf", []byte("x"), ""))
		_ = call(ctx, newTestClient(api, store))
		return store.ctxs
	}

	snap, seen := run(false, func(ctx context.Context, c *driveStorageClient) error {
		return c.Upload(ctx, folder, "a.pdf", []byte("x"), "")
	})["complete"]
	check(t, snap, seen)
	snap, seen = run(false, func(ctx context.Context, c *driveStorageClient) error { return c.Delete(ctx, folder, "seed.pdf") })["remove"]
	check(t, snap, seen)
	snap, seen = run(true, func(ctx context.Context, c *driveStorageClient) error {
		return c.Upload(ctx, folder, "a.pdf", []byte("x"), "")
	})["fail"]
	check(t, snap, seen)
}

// tenantCtxFrom keeps ctx's tenant on a context that is never cancelled.
func tenantCtxFrom(ctx context.Context) context.Context { return context.WithoutCancel(ctx) }
