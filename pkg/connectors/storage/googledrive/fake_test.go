package googledrive

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// fakeDriveFilesAPI models Drive faithfully for these tests: it is safe for
// concurrent use and, like Drive, happily stores two files with the same name
// in one folder — so a duplicate is visible if the registry fails to prevent it.
type fakeDriveFilesAPI struct {
	mu    sync.Mutex
	files []*fakeDriveFile
	err   error // returned by every call while set

	failCreates int           // number of upcoming creates to fail
	createGate  chan struct{} // when set, create blocks until it is closed
	deleteGate  chan struct{} // when set, delete blocks until it is closed or ctx ends
	createdHook func()        // called when a create starts, before the gate

	createCalls, updateCalls int
}

type fakeDriveFile struct {
	id, folder, name, documentID, contentType string
	content                                   []byte
}

var errFakeDrive = &fakeError{"fake drive: injected failure"}

type fakeError struct{ msg string }

func (e *fakeError) Error() string { return e.msg }

func newFakeDriveFilesAPI() *fakeDriveFilesAPI { return &fakeDriveFilesAPI{} }

func (f *fakeDriveFilesAPI) listByName(_ context.Context, folderID, name string) ([]*driveFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var out []*driveFile
	for _, file := range f.files {
		if file.folder == folderID && file.name == name {
			out = append(out, &driveFile{ID: file.id, MimeType: file.contentType, DocumentID: file.documentID})
		}
	}
	return out, nil
}

func (f *fakeDriveFilesAPI) findByDocumentID(_ context.Context, folderID, documentID string) (*driveFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	for _, file := range f.files {
		if file.folder == folderID && file.documentID == documentID {
			return &driveFile{ID: file.id, MimeType: file.contentType, DocumentID: file.documentID}, nil
		}
	}
	return nil, nil
}

func (f *fakeDriveFilesAPI) listByDocumentID(_ context.Context, folderID, documentID string) ([]*driveFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var out []*driveFile
	for _, file := range f.files {
		if file.folder == folderID && file.documentID == documentID {
			out = append(out, &driveFile{ID: file.id, MimeType: file.contentType, DocumentID: file.documentID})
		}
	}
	return out, nil
}

func (f *fakeDriveFilesAPI) download(_ context.Context, fileID string, _ int64) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, "", f.err
	}
	for _, file := range f.files {
		if file.id == fileID {
			return file.content, file.contentType, nil
		}
	}
	return nil, "", errDriveNotFound
}

func (f *fakeDriveFilesAPI) create(ctx context.Context, folderID, name, contentType, documentID string, content []byte) (*driveFile, error) {
	f.mu.Lock()
	f.createCalls++
	hook, gate := f.createdHook, f.createGate
	f.mu.Unlock()

	if hook != nil {
		hook()
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.failCreates > 0 {
		f.failCreates--
		return nil, errFakeDrive
	}
	file := &fakeDriveFile{id: uuid.NewString(), folder: folderID, name: name, documentID: documentID, contentType: contentType, content: content}
	f.files = append(f.files, file)
	return &driveFile{ID: file.id, MimeType: contentType, DocumentID: documentID}, nil
}

func (f *fakeDriveFilesAPI) update(_ context.Context, fileID, contentType, documentID string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	if f.err != nil {
		return f.err
	}
	for _, file := range f.files {
		if file.id == fileID {
			file.content, file.contentType, file.documentID = content, contentType, documentID
			return nil
		}
	}
	return errDriveNotFound
}

func (f *fakeDriveFilesAPI) delete(ctx context.Context, fileID string) error {
	f.mu.Lock()
	gate := f.deleteGate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	for i, file := range f.files {
		if file.id == fileID {
			f.files = append(f.files[:i], f.files[i+1:]...)
			return nil
		}
	}
	return errDriveNotFound
}

// filesNamed counts the files in folder with name: a duplicate shows as > 1.
func (f *fakeDriveFilesAPI) filesNamed(folder, name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, file := range f.files {
		if file.folder == folder && file.name == name {
			n++
		}
	}
	return n
}

// contentNamed returns the content of every file in folder with name, oldest first.
func (f *fakeDriveFilesAPI) contentNamed(folder, name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, file := range f.files {
		if file.folder == folder && file.name == name {
			out = append(out, string(file.content))
		}
	}
	return out
}

func (f *fakeDriveFilesAPI) calls() (creates, updates int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createCalls, f.updateCalls
}

// placeFile adds a file as if put in the folder outside the connector (or, with
// a document ID, as if an earlier crashed attempt created it).
func (f *fakeDriveFilesAPI) placeFile(folder, name, documentID string, content []byte) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	file := &fakeDriveFile{id: uuid.NewString(), folder: folder, name: name, documentID: documentID, content: content, contentType: "text/plain"}
	f.files = append(f.files, file)
	return file.id
}

var _ driveFilesAPI = (*fakeDriveFilesAPI)(nil)

// storeCase is one documents.Store the scenarios run against.
type storeCase struct {
	name string
	new  func(t *testing.T) documents.Store
}

// storeCases is the in-memory store always, plus PostgreSQL when built with
// the integration tag (postgresStoreCases; make test-postgres and CI's
// postgres suite run this package with it).
func storeCases(t *testing.T) []storeCase {
	cases := []storeCase{{name: "memory", new: func(*testing.T) documents.Store { return documents.NewMemoryStore() }}}
	return append(cases, postgresStoreCases(t)...)
}

// tenantCtx gives each test its own tenant, isolating its rows in a shared database.
func tenantCtx() context.Context {
	return shared.WithTenant(context.Background(), "tenant-"+uuid.NewString())
}

func newTestClient(api driveFilesAPI, store documents.Store) *driveStorageClient {
	return &driveStorageClient{api: api, docs: store, lease: documents.DefaultLease}
}
