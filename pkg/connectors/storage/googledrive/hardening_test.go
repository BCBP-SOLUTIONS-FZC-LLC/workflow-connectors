package googledrive

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func taggedFiles(api *fakeDriveFilesAPI, documentID string) int {
	api.mu.Lock()
	defer api.mu.Unlock()
	n := 0
	for _, f := range api.files {
		if f.documentID == documentID {
			n++
		}
	}
	return n
}

func docFor(t *testing.T, ctx context.Context, store documents.Store, name string) documents.Document {
	t.Helper()
	tenant, _ := shared.TenantFromContext(ctx)
	doc, found, err := store.Get(ctx, documents.Identity{TenantID: tenant, Provider: providerName, Container: folder, Filename: name})
	require.NoError(t, err)
	require.True(t, found)
	return doc
}

// A Drive write that hangs is cut off before the claim's lease expires, so
// it can never be running when another call takes the row over.
func TestDrive_WriteIsCutOffBeforeTheLeaseExpires(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		api.createGate = make(chan struct{}) // never opened: Drive hangs
		c := newTestClient(api, store)
		// A lease under leaseMargin gets half of it as its write budget:
		// the write is cut off at ~1s, a full second before the lease
		// expires, so a slow (-race, PostgreSQL) run still has room.
		c.lease = 2 * time.Second

		start := time.Now()
		err := c.Upload(ctx, folder, "slow.pdf", []byte("x"), "")
		require.Error(t, err)
		assert.Less(t, time.Since(start), c.lease, "the write stops inside the lease")
		assert.Equal(t, documents.StateFailed, docFor(t, ctx, store, "slow.pdf").State, "and the row is released at once")
	})
}

// A stale owner's create that completed on Drive's side leaves a second
// file tagged with the document; the owner's write removes it.
func TestDrive_Create_RemovesLeftoverFilesTaggedWithTheDocument(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)
		require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("v1"), ""))
		docID := docFor(t, ctx, store, "a.pdf").ID

		// A leftover from an interrupted attempt, then the recorded file is
		// removed outside the connector so the next upload creates afresh.
		api.mu.Lock()
		api.files = append(api.files, &fakeDriveFile{id: uuid.NewString(), folder: folder, name: "a.pdf", documentID: docID})
		recorded := docFor(t, ctx, store, "a.pdf").ObjectID
		for i, f := range api.files {
			if f.id == recorded {
				api.files = append(api.files[:i], api.files[i+1:]...)
				break
			}
		}
		api.mu.Unlock()

		require.NoError(t, c.Upload(ctx, folder, "a.pdf", []byte("v2"), ""))
		assert.Equal(t, 1, taggedFiles(api, docID), "exactly one file carries the document's tag")
	})
}

// Delete removes every file tagged with the document, not only the recorded
// one, so no orphan of an interrupted attempt survives.
func TestDrive_Delete_RemovesEveryTaggedFile(t *testing.T) {
	forEachStore(t, func(t *testing.T, store documents.Store) {
		ctx, api := tenantCtx(), newFakeDriveFilesAPI()
		c := newTestClient(api, store)
		require.NoError(t, c.Upload(ctx, folder, "b.pdf", []byte("x"), ""))
		docID := docFor(t, ctx, store, "b.pdf").ID
		api.mu.Lock()
		api.files = append(api.files, &fakeDriveFile{id: uuid.NewString(), folder: folder, name: "b.pdf", documentID: docID})
		api.mu.Unlock()

		require.NoError(t, c.Delete(ctx, folder, "b.pdf"))
		assert.Zero(t, taggedFiles(api, docID))
	})
}

type failingComplete struct{ documents.Store }

func (failingComplete) Complete(context.Context, string, string, documents.Result) (documents.Document, error) {
	return documents.Document{}, errors.New("database unavailable")
}

// A completion that cannot be recorded releases the row at once instead of
// leaving it UPLOADING (and every retry InProgress) until the lease expires.
func TestDrive_CompleteFailure_ReleasesTheRow(t *testing.T) {
	ctx, api, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
	c := newTestClient(api, failingComplete{store})

	require.Error(t, c.Upload(ctx, folder, "c.pdf", []byte("x"), ""))
	assert.Equal(t, documents.StateFailed, docFor(t, ctx, store, "c.pdf").State)

	c = newTestClient(api, store)
	require.NoError(t, c.Upload(ctx, folder, "c.pdf", []byte("x"), ""), "a retry proceeds, adopting the written file")
	assert.Equal(t, 1, api.filesNamed(folder, "c.pdf"))
}

// A service-account key may only name Google's token endpoint: the token
// request would otherwise go to any host the tenant chose.
func TestNewDriveStorageClient_RejectsForeignTokenEndpoint(t *testing.T) {
	t.Parallel()
	for _, key := range []string{
		`{"type":"service_account","token_uri":"http://internal-svc:8080/admin"}`,
		`{"type":"service_account","universe_domain":"evil.example"}`,
		`{"type":"authorized_user"}`,
		`not json`,
	} {
		_, err := newDriveStorageClient(context.Background(), map[string]any{"driveServiceAccountKey": key}, documents.NewMemoryStore())
		assert.ErrorIs(t, err, shared.ErrValidation, key)
	}
}

// The lease runs from the claim: time spent before the write (a slow claim or
// Advance) comes out of the write's budget, so the write still ends inside
// the lease.
func TestDrive_WriteBudgetIsMeasuredFromTheClaim(t *testing.T) {
	t.Parallel()
	ctx, api := tenantCtx(), newFakeDriveFilesAPI()
	api.createGate = make(chan struct{}) // Drive hangs
	c := newTestClient(api, slowAdvance{documents.NewMemoryStore(), 1500 * time.Millisecond})
	c.lease = 4 * time.Second // budget 2s from the claim, of which Advance takes 1.5s

	// Measured from the claim the write ends at ~2s; measured from the
	// write it would end at ~3.5s. The 3s bound leaves a second either side.
	start := time.Now()
	require.Error(t, c.Upload(ctx, folder, "slow.pdf", []byte("x"), ""))
	assert.Less(t, time.Since(start), 3*time.Second, "the write's deadline counts the time already spent since the claim")
}

type slowAdvance struct {
	documents.Store
	delay time.Duration
}

func (s slowAdvance) Advance(ctx context.Context, docID, attempt string, to documents.State) (documents.Document, error) {
	time.Sleep(s.delay)
	return s.Store.Advance(ctx, docID, attempt, to)
}

// Delete is bounded by its lease too: a Drive delete that hangs is cut off
// before another call could take the row over.
func TestDrive_DeleteIsBoundedByTheLease(t *testing.T) {
	t.Parallel()
	ctx, api, store := tenantCtx(), newFakeDriveFilesAPI(), documents.NewMemoryStore()
	c := newTestClient(api, store)
	require.NoError(t, c.Upload(ctx, folder, "d.pdf", []byte("x"), ""))

	api.deleteGate = make(chan struct{}) // Drive hangs on delete
	c.lease = 2 * time.Second            // cut off at ~1s, a second inside the lease
	start := time.Now()
	require.Error(t, c.Delete(ctx, folder, "d.pdf"))
	assert.Less(t, time.Since(start), c.lease)
}
