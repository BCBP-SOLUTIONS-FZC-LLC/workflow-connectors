// Package googledrive is the google-drive provider adapter for the storage
// connector.
//
// Drive has no atomic create-if-absent, so two uploads that each check for a
// file and then create one can both create it. This adapter never decides
// uniqueness from Drive: a documents.Store row per tenant + folder + filename
// is the authority (see package documents). Only the call that claims the row
// writes to Drive, files are addressed by the Drive file ID recorded on the
// row, and the Drive file name is metadata only.
//
// # One tenant per folder
//
// The registry identity includes the tenant, so a Drive folder must belong to
// one tenant. Two tenants configured with the same folder each get their own
// row for the same file name: both may adopt (update and tag) the same
// hand-placed untagged file at once, and one may delete a file the other has
// just adopted. Drive offers no conditional update to prevent it and the
// adapter cannot tell that a folder is shared; give each tenant its own
// folder or shared drive.
//
// # Clocks
//
// Leases are set by the store (the database's clock, in production), while
// the Drive write deadline and Fetch's "is this lease still live" check use
// this worker's clock. Worker hosts and the database must be kept in sync
// (NTP); the one-minute leaseMargin absorbs ordinary skew.
package googledrive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
)

const (
	providerName = "google-drive"
	// appPropertyDocumentID tags every file this adapter creates with its
	// registry document ID, so a retry after a crash adopts the file instead
	// of creating a second one.
	appPropertyDocumentID = "connectorDocumentId"
)

// errDriveNotFound is what driveFilesAPI returns for a file that no longer
// exists (deleted outside the connector).
var errDriveNotFound = errors.New("drive: file not found")

type driveFile struct {
	ID         string
	MimeType   string
	DocumentID string // appProperties[connectorDocumentId], empty for files made elsewhere
	// Trashed and Parents are read by get only (list queries already keep
	// to untrashed files in the folder).
	Trashed bool
	Parents []string
}

type driveFilesAPI interface {
	// listByName returns every file called name in the folder, oldest first.
	listByName(ctx context.Context, folderID, name string) ([]*driveFile, error)
	findByDocumentID(ctx context.Context, folderID, documentID string) (*driveFile, error)
	// listByDocumentID returns every file tagged with documentID, oldest first.
	listByDocumentID(ctx context.Context, folderID, documentID string) ([]*driveFile, error)
	// get returns a file's metadata, with Trashed and Parents.
	get(ctx context.Context, fileID string) (*driveFile, error)
	download(ctx context.Context, fileID string, maxBytes int64) (content []byte, contentType string, err error)
	create(ctx context.Context, folderID, name, contentType, documentID string, content []byte) (*driveFile, error)
	// update replaces the file's content and tags it with documentID.
	update(ctx context.Context, fileID, contentType, documentID string, content []byte) error
	delete(ctx context.Context, fileID string) error
}

type driveStorageClient struct {
	api   driveFilesAPI
	docs  documents.Store
	lease time.Duration
	// httpClient owns the transport under the OAuth layer, so Close can drop
	// its idle connections; nil in tests.
	httpClient *http.Client
}

// leaseMargin is how long before its lease expires an upload's Drive write
// is cut off, so the write never outlives the ownership that allows it.
const leaseMargin = time.Minute

// leaseDeadline bounds work done as a row's owner to the claim's lease
// (minus a margin), measured from just before the claim was made: once the
// lease expires another call may take the row over, so a Drive write or
// delete still running then could create, or remove, another owner's file.
// A caller deadline that is earlier still wins.
func (c *driveStorageClient) leaseDeadline(ctx context.Context, claimedAt time.Time) (context.Context, context.CancelFunc) {
	budget := c.lease - leaseMargin
	if budget <= 0 {
		budget = c.lease / 2
	}
	return context.WithDeadline(ctx, claimedAt.Add(budget))
}

// Close drops the client's idle connections when the cache retires it.
func (c *driveStorageClient) Close() error {
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	return nil
}

// NewProvider returns the google-drive ProviderConstructor. docs is the
// uniqueness authority for uploads — in production a sqlstore.Store on the
// worker's database, so uniqueness holds across worker replicas.
func NewProvider(docs documents.Store) storage.ProviderConstructor {
	return func(ctx context.Context, params map[string]any) (storage.ProviderClient, error) {
		return newDriveStorageClient(ctx, params, docs)
	}
}

func newDriveStorageClient(ctx context.Context, params map[string]any, docs documents.Store) (storage.ProviderClient, error) {
	if docs == nil {
		return nil, fmt.Errorf("%w: google-drive needs a document store (googledrive.NewProvider(store))", shared.ErrValidation)
	}
	serviceAccountKey := shared.StringField(params, "driveServiceAccountKey")
	if serviceAccountKey == "" {
		return nil, fmt.Errorf("%w: driveServiceAccountKey is required for google-drive", shared.ErrValidation)
	}

	if err := shared.ValidateGoogleServiceAccountKey([]byte(serviceAccountKey), "driveServiceAccountKey"); err != nil {
		return nil, err
	}
	// The token source outlives this call (clients are cached): its token
	// requests run on a background context with a bounded client.
	tokenCtx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: shared.DefaultHTTPTimeout})
	creds, err := google.CredentialsFromJSONWithType(tokenCtx, []byte(serviceAccountKey), google.ServiceAccount, drive.DriveScope)
	if err != nil {
		return nil, fmt.Errorf("%w: google-drive credentials: %w", shared.ErrValidation, err)
	}
	// Drive calls are bounded by each call's context (an upload by its
	// lease); the client never follows a redirect, which would re-send the
	// request and its bearer token to another host.
	base := shared.ProviderHTTPClient()
	base.Timeout = 0
	httpClient := &http.Client{
		Transport:     &oauth2.Transport{Source: creds.TokenSource, Base: base.Transport},
		CheckRedirect: base.CheckRedirect,
	}
	svc, err := drive.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, upstream("google-drive service", err)
	}
	return &driveStorageClient{api: &realDriveFilesAPI{files: svc.Files}, docs: docs, lease: documents.DefaultLease, httpClient: base}, nil
}

func identityFor(ctx context.Context, folderID, name string) (documents.Identity, error) {
	tenant, ok := shared.TenantFromContext(ctx)
	if !ok {
		return documents.Identity{}, shared.ErrMissingTenant
	}
	return documents.Identity{TenantID: tenant, Provider: providerName, Container: folderID, Filename: name}, nil
}

// Upload claims the document row, writes the file as its owner, and records
// the outcome. A concurrent upload or delete of the same document gets an
// *documents.InProgressError and never reaches Drive.
func (c *driveStorageClient) Upload(ctx context.Context, folderID, name string, content []byte, contentType string) error {
	identity, err := identityFor(ctx, folderID, name)
	if err != nil {
		return err
	}
	attempt := uuid.NewString()

	claimedAt := time.Now() // the lease runs from the claim, not from the write
	doc, claimed, err := c.docs.Claim(ctx, identity, attempt, c.lease)
	if err != nil {
		return upstream(fmt.Sprintf("drive upload %q: claim", name), err)
	}
	if !claimed {
		return &documents.InProgressError{DocumentID: doc.ID, State: doc.State}
	}
	advanced, err := c.docs.Advance(ctx, doc.ID, attempt, documents.StateUploading)
	if err != nil {
		c.release(ctx, doc.ID, attempt, "advance: "+err.Error())
		return upstream(fmt.Sprintf("drive upload %q", name), err)
	}
	doc = advanced

	writeCtx, cancel := c.leaseDeadline(ctx, claimedAt)
	fileID, err := c.write(writeCtx, doc, folderID, name, contentType, content)
	cancel()
	if err != nil {
		c.release(ctx, doc.ID, attempt, err.Error())
		return upstream(fmt.Sprintf("drive upload %q", name), err)
	}

	result := documents.Result{ObjectID: fileID, ContentType: contentType, SizeBytes: int64(len(content))}
	recordCtx, cancelRecord := recordContext(ctx)
	_, err = c.docs.Complete(recordCtx, doc.ID, attempt, result)
	cancelRecord()
	if err != nil {
		// Release the row now rather than when the lease expires; a retry
		// adopts the written file by its document tag.
		c.release(ctx, doc.ID, attempt, "record completion: "+err.Error())
		return upstream(fmt.Sprintf("drive upload %q: record", name), err)
	}
	return nil
}

// recordTimeout bounds each registry write made after a Drive call or a
// failed transition. The write runs on a context detached from the caller's
// cancellation, so the outcome is recorded (or the row released) even when
// the call's context is done, but it never hangs on an unreachable database.
const recordTimeout = 10 * time.Second

func recordContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
}

// release marks an owned row FAILED with reason, so a retry can claim it at
// once rather than when its lease expires. Best effort: if the registry
// refuses this too, the lease expiry still frees the row.
func (c *driveStorageClient) release(ctx context.Context, docID, attempt, reason string) {
	recordCtx, cancel := recordContext(ctx)
	defer cancel()
	_, _ = c.docs.Fail(recordCtx, docID, attempt, reason)
}

// write puts content into the document's Drive file and returns its ID. The
// file is, in order: the recorded file; else the oldest file tagged with the
// document ID (left by an attempt that crashed before recording it); else the
// oldest untagged file of the same name, placed in the folder outside the
// connector, which the document adopts; else a new file. Every write tags the
// file with the document ID, so an adopted file belongs to the document from
// then on and a later delete removes it.
func (c *driveStorageClient) write(ctx context.Context, doc documents.Document, folderID, name, contentType string, content []byte) (string, error) {
	if doc.ObjectID != "" {
		err := c.checkRecorded(ctx, folderID, doc.ObjectID)
		if err == nil {
			err = c.api.update(ctx, doc.ObjectID, contentType, doc.ID, content)
		}
		if err == nil {
			return doc.ObjectID, nil
		}
		if !errors.Is(err, errDriveNotFound) {
			return "", err
		}
		// The recorded file was deleted, trashed or moved out of the folder
		// outside the connector: it is no longer the document's file.
	}

	fileID, err := c.existingFile(ctx, folderID, name, doc.ID)
	if err != nil {
		return "", err
	}
	if fileID != "" {
		err := c.api.update(ctx, fileID, contentType, doc.ID, content)
		if err == nil {
			c.removeOtherTaggedFiles(ctx, folderID, doc.ID, fileID)
			return fileID, nil
		}
		if !errors.Is(err, errDriveNotFound) {
			return "", err
		}
		// Deleted between the lookup and the update: write a new one.
	}

	created, err := c.api.create(ctx, folderID, name, contentType, doc.ID, content)
	if err != nil {
		return "", err
	}
	// A create cut off by the lease deadline may still have completed on
	// Drive's side; any other file tagged with this document is such a
	// leftover. Remove them (best effort: delete removes them too).
	c.removeOtherTaggedFiles(ctx, folderID, doc.ID, created.ID)
	return created.ID, nil
}

// checkRecorded confirms that a document's recorded file is still its file:
// it exists, is not in the trash, and is still in the folder. Drive's get,
// update and download all succeed on a trashed file and on one moved to
// another folder, so without this check an upload would overwrite (and a
// fetch read, and a delete permanently remove) a file the folder no longer
// holds. A file that fails the check is errDriveNotFound, so callers fall
// back as for a deleted file. It costs one files.get per recorded-file
// upload, fetch and delete.
//
// The folder is compared by ID: a folder given by an alias ("root") never
// matches a parent, so its recorded file is always looked up again by its
// document tag (which the folder query does resolve) — correct, at the cost
// of a list call.
func (c *driveStorageClient) checkRecorded(ctx context.Context, folderID, fileID string) error {
	f, err := c.api.get(ctx, fileID)
	if err != nil {
		return err
	}
	if f.Trashed {
		return fmt.Errorf("%w: file %q is in the trash", errDriveNotFound, fileID)
	}
	if !slices.Contains(f.Parents, folderID) {
		return fmt.Errorf("%w: file %q is no longer in folder %q", errDriveNotFound, fileID, folderID)
	}
	return nil
}

// existingFile returns the file a document with no recorded file takes over:
// the oldest file tagged with its ID, else the oldest untagged file of the
// same name; "" when there is neither.
func (c *driveStorageClient) existingFile(ctx context.Context, folderID, name, documentID string) (string, error) {
	tagged, err := c.api.findByDocumentID(ctx, folderID, documentID)
	if err != nil {
		return "", err
	}
	if tagged != nil {
		return tagged.ID, nil
	}
	untagged, err := c.untaggedByName(ctx, folderID, name)
	if err != nil || untagged == nil {
		return "", err
	}
	return untagged.ID, nil
}

// untaggedByName returns the oldest file called name in the folder that
// carries no document tag: a file placed there outside the connector. A
// tagged file belongs to its document's row and is never adopted, read or
// deleted by name. Neither is anything that is not a content file — a
// folder, a shortcut, or a Google-native file (Docs, Sheets, forms…, every
// application/vnd.google-apps.* type): adopting one would overwrite it,
// fetching one cannot download it, and deleting one bypasses the trash (a
// folder with everything in it). The Drive query already excludes folders
// and shortcuts; the MIME type is checked here as well, for every type.
func (c *driveStorageClient) untaggedByName(ctx context.Context, folderID, name string) (*driveFile, error) {
	files, err := c.api.listByName(ctx, folderID, name)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.DocumentID == "" && isContentFile(f.MimeType) {
			return f, nil
		}
	}
	return nil, nil
}

// googleAppsMimePrefix starts the MIME type of every Drive item that is not
// a stored file: folders, shortcuts and Google-native documents.
const googleAppsMimePrefix = "application/vnd.google-apps."

func isContentFile(mimeType string) bool {
	return !strings.HasPrefix(mimeType, googleAppsMimePrefix)
}

func (c *driveStorageClient) removeOtherTaggedFiles(ctx context.Context, folderID, documentID, keepID string) {
	files, err := c.api.listByDocumentID(ctx, folderID, documentID)
	if err != nil {
		return
	}
	for _, f := range files {
		if f.ID != keepID {
			_ = c.api.delete(ctx, f.ID)
		}
	}
}

// Fetch reads the document's recorded file. A document with no recorded
// file — no row, a FAILED upload that never wrote, or an upload or delete
// whose lease expired (its call crashed) — or whose recorded file was
// deleted, trashed or moved out of the folder outside the connector
// (checkRecorded) is read from the file a retry of that upload
// would adopt: the oldest file tagged with the document's ID,
// else the oldest untagged content file of that name, placed in the folder
// outside the connector. A document a live call is writing for the first
// time, or deleting, is in progress (transient): its file is about to appear
// or disappear.
//
// Whether a lease is live is judged by this worker's clock against the
// lease the database set, so worker and database clocks must be in sync
// (NTP); a skew of seconds only shifts when a crashed call's row is read
// past.
func (c *driveStorageClient) Fetch(ctx context.Context, folderID, name string, maxBytes int64) ([]byte, string, error) {
	identity, err := identityFor(ctx, folderID, name)
	if err != nil {
		return nil, "", err
	}
	doc, found, err := c.docs.Get(ctx, identity)
	if err != nil {
		return nil, "", upstream(fmt.Sprintf("drive fetch %q", name), err)
	}

	live := found && doc.State.Busy() && time.Now().Before(doc.LeaseExpiresAt)
	if live && (doc.State == documents.StateDeleting || doc.ObjectID == "") {
		return nil, "", &documents.InProgressError{DocumentID: doc.ID, State: doc.State}
	}
	if found && doc.ObjectID != "" {
		var content []byte
		var contentType string
		err := c.checkRecorded(ctx, folderID, doc.ObjectID)
		if err == nil {
			content, contentType, err = c.api.download(ctx, doc.ObjectID, maxBytes)
		}
		if !errors.Is(err, errDriveNotFound) {
			if err != nil {
				return nil, "", upstream(fmt.Sprintf("drive fetch %q", name), err)
			}
			return content, contentType, nil
		}
		// The recorded file was deleted, trashed or moved outside the
		// connector: read what a re-upload would write to (write's same
		// fallback).
	}

	f, err := c.unrecordedFile(ctx, doc, found, folderID, name)
	if err != nil {
		return nil, "", upstream(fmt.Sprintf("drive fetch %q", name), err)
	}
	if f == nil {
		return nil, "", fmt.Errorf("%w: %w", shared.ErrUpstream, shared.Permanent("drive file not found",
			fmt.Errorf("drive fetch: no file named %q in folder %q", name, folderID)))
	}
	content, contentType, err := c.api.download(ctx, f.ID, maxBytes)
	if err != nil {
		return nil, "", upstream(fmt.Sprintf("drive fetch %q", name), err)
	}
	return content, contentType, nil
}

// unrecordedFile finds the file of a document with no recorded file: the
// oldest file tagged with its ID (left by a crashed attempt), else the oldest
// untagged content file of the name; nil when there is neither.
func (c *driveStorageClient) unrecordedFile(ctx context.Context, doc documents.Document, found bool, folderID, name string) (*driveFile, error) {
	if found {
		tagged, err := c.api.findByDocumentID(ctx, folderID, doc.ID)
		if err != nil || tagged != nil {
			return tagged, err
		}
	}
	return c.untaggedByName(ctx, folderID, name)
}

// Delete claims the document row — inserting one when the name has none, so a
// delete never runs alongside an upload that could adopt the same file —
// deletes the document's files, and removes the row. Deleting an absent
// document succeeds.
func (c *driveStorageClient) Delete(ctx context.Context, folderID, name string) error {
	identity, err := identityFor(ctx, folderID, name)
	if err != nil {
		return err
	}
	attempt := uuid.NewString()

	claimedAt := time.Now()
	doc, claimed, err := c.docs.Claim(ctx, identity, attempt, c.lease)
	if err != nil {
		return upstream(fmt.Sprintf("drive delete %q: claim", name), err)
	}
	if !claimed {
		return &documents.InProgressError{DocumentID: doc.ID, State: doc.State}
	}
	advanced, err := c.docs.Advance(ctx, doc.ID, attempt, documents.StateDeleting)
	if err != nil {
		c.release(ctx, doc.ID, attempt, "advance: "+err.Error())
		return upstream(fmt.Sprintf("drive delete %q", name), err)
	}
	doc = advanced

	deleteCtx, cancel := c.leaseDeadline(ctx, claimedAt)
	err = c.deleteFiles(deleteCtx, doc, folderID, name)
	cancel()
	if err != nil {
		c.release(ctx, doc.ID, attempt, err.Error())
		return upstream(fmt.Sprintf("drive delete %q", name), err)
	}

	recordCtx, cancelRecord := recordContext(ctx)
	err = c.docs.Remove(recordCtx, doc.ID, attempt)
	cancelRecord()
	if err != nil {
		// The files are gone. Release the row so a retry claims it at once,
		// finds nothing left to delete and removes the row.
		c.release(ctx, doc.ID, attempt, "record deletion: "+err.Error())
		return upstream(fmt.Sprintf("drive delete %q: record", name), err)
	}
	return nil
}

// deleteFiles removes every file the document holds: its recorded file and
// every file tagged with its ID, so no file an interrupted attempt created is
// left behind. A document that holds neither — no row before this delete, or
// one whose upload never wrote — holds what an upload would adopt and a fetch
// would read: the oldest untagged file of the same name, which is deleted
// instead. A file already gone counts as deleted. A recorded file that was
// trashed or moved out of the folder (checkRecorded) is no longer the
// document's and is left as it is: a delete never reaches outside the folder,
// and never purges the trash. A document with a recorded file never deletes
// an untagged file by name, even when the recorded file is gone.
func (c *driveStorageClient) deleteFiles(ctx context.Context, doc documents.Document, folderID, name string) error {
	var ids []string
	if doc.ObjectID != "" {
		switch err := c.checkRecorded(ctx, folderID, doc.ObjectID); {
		case err == nil:
			ids = append(ids, doc.ObjectID)
		case !errors.Is(err, errDriveNotFound):
			return err
		}
	}
	tagged, err := c.api.listByDocumentID(ctx, folderID, doc.ID)
	if err != nil {
		return err
	}
	for _, f := range tagged {
		if !slices.Contains(ids, f.ID) {
			ids = append(ids, f.ID)
		}
	}
	if len(ids) == 0 && doc.ObjectID == "" {
		f, err := c.untaggedByName(ctx, folderID, name)
		if err != nil {
			return err
		}
		if f != nil {
			ids = append(ids, f.ID)
		}
	}
	for _, id := range ids {
		if err := c.api.delete(ctx, id); err != nil && !errors.Is(err, errDriveNotFound) {
			return err
		}
	}
	return nil
}

type realDriveFilesAPI struct {
	files *drive.FilesService
}

const driveFileFields = "files(id, mimeType, appProperties)"

func (r *realDriveFilesAPI) listByName(ctx context.Context, folderID, name string) ([]*driveFile, error) {
	return r.list(ctx, fmt.Sprintf("%s in parents and name = %s and trashed = false and mimeType != %s and mimeType != %s",
		driveQuote(folderID), driveQuote(name), driveQuote(driveFolderMime), driveQuote(driveShortcutMime)))
}

const (
	driveFolderMime   = googleAppsMimePrefix + "folder"
	driveShortcutMime = googleAppsMimePrefix + "shortcut"
)

func (r *realDriveFilesAPI) findByDocumentID(ctx context.Context, folderID, documentID string) (*driveFile, error) {
	return r.findOne(ctx, documentQuery(folderID, documentID))
}

func (r *realDriveFilesAPI) listByDocumentID(ctx context.Context, folderID, documentID string) ([]*driveFile, error) {
	return r.list(ctx, documentQuery(folderID, documentID))
}

func documentQuery(folderID, documentID string) string {
	return fmt.Sprintf("%s in parents and appProperties has { key=%s and value=%s } and trashed = false",
		driveQuote(folderID), driveQuote(appPropertyDocumentID), driveQuote(documentID))
}

// list returns every match, oldest first, following pages.
func (r *realDriveFilesAPI) list(ctx context.Context, query string) ([]*driveFile, error) {
	var out []*driveFile
	err := r.files.List().Q(query).Fields("nextPageToken, "+driveFileFields).OrderBy("createdTime").PageSize(100).
		IncludeItemsFromAllDrives(true).SupportsAllDrives(true).
		Pages(ctx, func(list *drive.FileList) error {
			for _, f := range list.Files {
				out = append(out, &driveFile{ID: f.Id, MimeType: f.MimeType, DocumentID: f.AppProperties[appPropertyDocumentID]})
			}
			return nil
		})
	return out, err
}

// findOne returns the oldest match, so repeated lookups agree.
func (r *realDriveFilesAPI) findOne(ctx context.Context, query string) (*driveFile, error) {
	list, err := r.files.List().Q(query).Fields(driveFileFields).OrderBy("createdTime").PageSize(1).
		IncludeItemsFromAllDrives(true).SupportsAllDrives(true).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	if len(list.Files) == 0 {
		return nil, nil
	}
	f := list.Files[0]
	return &driveFile{ID: f.Id, MimeType: f.MimeType, DocumentID: f.AppProperties[appPropertyDocumentID]}, nil
}

// driveGetFields are the metadata get reads: enough for checkRecorded.
const driveGetFields = "id, mimeType, trashed, parents, appProperties"

func (r *realDriveFilesAPI) get(ctx context.Context, fileID string) (*driveFile, error) {
	f, err := r.files.Get(fileID).Fields(driveGetFields).SupportsAllDrives(true).Context(ctx).Do()
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &driveFile{ID: f.Id, MimeType: f.MimeType, DocumentID: f.AppProperties[appPropertyDocumentID], Trashed: f.Trashed, Parents: f.Parents}, nil
}

func (r *realDriveFilesAPI) download(ctx context.Context, fileID string, maxBytes int64) ([]byte, string, error) {
	resp, err := r.files.Get(fileID).SupportsAllDrives(true).Context(ctx).Download()
	if err != nil {
		return nil, "", mapNotFound(err)
	}
	defer func() { _ = resp.Body.Close() }()

	content, err := shared.ReadAllLimited(resp.Body, maxBytes, fmt.Sprintf("drive file %q", fileID))
	if err != nil {
		return nil, "", err
	}
	return content, resp.Header.Get("Content-Type"), nil
}

func (r *realDriveFilesAPI) create(ctx context.Context, folderID, name, contentType, documentID string, content []byte) (*driveFile, error) {
	f := &drive.File{
		Name:          name,
		Parents:       []string{folderID},
		MimeType:      contentType,
		AppProperties: map[string]string{appPropertyDocumentID: documentID},
	}
	created, err := r.files.Create(f).Media(bytes.NewReader(content)).SupportsAllDrives(true).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	return &driveFile{ID: created.Id, MimeType: created.MimeType, DocumentID: documentID}, nil
}

func (r *realDriveFilesAPI) update(ctx context.Context, fileID, contentType, documentID string, content []byte) error {
	f := &drive.File{MimeType: contentType, AppProperties: map[string]string{appPropertyDocumentID: documentID}}
	_, err := r.files.Update(fileID, f).Media(bytes.NewReader(content)).SupportsAllDrives(true).Context(ctx).Do()
	return mapNotFound(err)
}

func (r *realDriveFilesAPI) delete(ctx context.Context, fileID string) error {
	return mapNotFound(r.files.Delete(fileID).SupportsAllDrives(true).Context(ctx).Do())
}

// upstream wraps a Drive or registry error as ErrUpstream with its class.
func upstream(op string, err error) error {
	if errors.Is(err, shared.ErrValidation) {
		return shared.Classify(op, err) // input errors stay validation-only, never ErrUpstream
	}
	class, reason := classify(err)
	return fmt.Errorf("%w: %s: %w", shared.ErrUpstream, op, shared.WithClass(class, reason, err))
}

// driveTransientReasons are the googleapi error reasons Drive returns for
// rate limiting and backend trouble — often with status 403, which would
// otherwise read as a permanent "forbidden".
var driveTransientReasons = map[string]bool{
	"rateLimitExceeded":        true,
	"userRateLimitExceeded":    true,
	"sharingRateLimitExceeded": true,
	"backendError":             true,
	"internalError":            true,
}

// classify: a class already attached (an upload in progress) wins; then a
// Drive API error by reason, then by HTTP status; then the network cause.
func classify(err error) (shared.Class, string) {
	if class, reason := shared.ClassOf(err); class != shared.ClassUnknown {
		return class, reason
	}
	if errors.Is(err, documents.ErrOwnershipLost) {
		// Another call took over after our lease expired; a retry sees its
		// progress (in progress, then available) and converges.
		return shared.ClassTransient, "ownership lost"
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		for _, item := range apiErr.Errors {
			if driveTransientReasons[item.Reason] {
				return shared.ClassTransient, "drive " + item.Reason
			}
		}
		return shared.ClassifyHTTPStatus(apiErr.Code), fmt.Sprintf("drive http %d", apiErr.Code)
	}
	return shared.ClassifyCause(err)
}

func mapNotFound(err error) error {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound {
		return fmt.Errorf("%w: %w", errDriveNotFound, err)
	}
	return err
}

var (
	_ storage.ProviderClient = (*driveStorageClient)(nil)
	_ driveFilesAPI          = (*realDriveFilesAPI)(nil)
)

// driveQuote renders s as a Drive search-query string literal: single quotes,
// with backslash and single quote escaped by a backslash. Go's %q produces
// double-quoted Go escapes, which are not Drive query syntax.
func driveQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}
