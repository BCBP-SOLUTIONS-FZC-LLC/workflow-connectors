package connectors

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeDriveFilesAPI struct {
	files map[string]map[string]driveFileRecord // folderID -> name -> record
	err   error
}

type driveFileRecord struct {
	id          string
	content     []byte
	contentType string
}

func newFakeDriveFilesAPI() *fakeDriveFilesAPI {
	return &fakeDriveFilesAPI{files: make(map[string]map[string]driveFileRecord)}
}

func (f *fakeDriveFilesAPI) findByName(_ context.Context, folderID, name string) (*driveFile, error) {
	if f.err != nil {
		return nil, f.err
	}
	rec, ok := f.files[folderID][name]
	if !ok {
		return nil, nil
	}
	return &driveFile{ID: rec.id, MimeType: rec.contentType}, nil
}

func (f *fakeDriveFilesAPI) download(_ context.Context, fileID string) ([]byte, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	for _, byName := range f.files {
		for _, rec := range byName {
			if rec.id == fileID {
				return rec.content, rec.contentType, nil
			}
		}
	}
	return nil, "", errors.New("fake drive: file not found")
}

func (f *fakeDriveFilesAPI) create(_ context.Context, folderID, name, contentType string, content []byte) (*driveFile, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.files[folderID] == nil {
		f.files[folderID] = make(map[string]driveFileRecord)
	}
	id := uuid.New().String()
	f.files[folderID][name] = driveFileRecord{id: id, content: content, contentType: contentType}
	return &driveFile{ID: id, MimeType: contentType}, nil
}

func (f *fakeDriveFilesAPI) update(_ context.Context, fileID, contentType string, content []byte) error {
	if f.err != nil {
		return f.err
	}
	for folderID, byName := range f.files {
		for name, rec := range byName {
			if rec.id == fileID {
				f.files[folderID][name] = driveFileRecord{id: fileID, content: content, contentType: contentType}
				return nil
			}
		}
	}
	return errors.New("fake drive: file not found")
}

func (f *fakeDriveFilesAPI) delete(_ context.Context, fileID string) error {
	if f.err != nil {
		return f.err
	}
	for _, byName := range f.files {
		for name, rec := range byName {
			if rec.id == fileID {
				delete(byName, name)
				return nil
			}
		}
	}
	return nil
}

var _ driveFilesAPI = (*fakeDriveFilesAPI)(nil)

func TestDriveStorageClient_UploadThenFetch_RoundTrips(t *testing.T) {
	t.Parallel()

	api := newFakeDriveFilesAPI()
	client := &driveStorageClient{api: api}
	ctx := context.Background()

	require.NoError(t, client.Upload(ctx, "folder1", "report.pdf", []byte("bytes"), "application/pdf"))

	content, contentType, err := client.Fetch(ctx, "folder1", "report.pdf")
	require.NoError(t, err)
	assert.Equal(t, []byte("bytes"), content)
	assert.Equal(t, "application/pdf", contentType)
}

func TestDriveStorageClient_UploadTwice_UpdatesInPlace_NoDuplicate(t *testing.T) {
	t.Parallel()

	api := newFakeDriveFilesAPI()
	client := &driveStorageClient{api: api}
	ctx := context.Background()

	require.NoError(t, client.Upload(ctx, "folder1", "same-name.txt", []byte("v1"), "text/plain"))
	firstID := api.files["folder1"]["same-name.txt"].id

	require.NoError(t, client.Upload(ctx, "folder1", "same-name.txt", []byte("v2"), "text/plain"))

	assert.Len(t, api.files["folder1"], 1, "must still be exactly one file, not a duplicate")
	assert.Equal(t, firstID, api.files["folder1"]["same-name.txt"].id, "must be the same file, updated in place")

	content, _, err := client.Fetch(ctx, "folder1", "same-name.txt")
	require.NoError(t, err)
	assert.Equal(t, []byte("v2"), content)
}

func TestDriveStorageClient_Delete_AlreadyAbsent_IsIdempotent(t *testing.T) {
	t.Parallel()

	api := newFakeDriveFilesAPI()
	client := &driveStorageClient{api: api}

	require.NoError(t, client.Delete(context.Background(), "folder1", "never-existed.txt"))
}

func TestDriveStorageClient_Delete_RemovesFile(t *testing.T) {
	t.Parallel()

	api := newFakeDriveFilesAPI()
	client := &driveStorageClient{api: api}
	ctx := context.Background()

	require.NoError(t, client.Upload(ctx, "folder1", "k.txt", []byte("x"), "text/plain"))
	require.NoError(t, client.Delete(ctx, "folder1", "k.txt"))

	_, _, err := client.Fetch(ctx, "folder1", "k.txt")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUpstream))
}

func TestDriveStorageClient_APIError_WrappedAsUpstream(t *testing.T) {
	t.Parallel()

	api := newFakeDriveFilesAPI()
	api.err = errors.New("quota exceeded")
	client := &driveStorageClient{api: api}

	_, _, err := client.Fetch(context.Background(), "folder1", "k.txt")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUpstream))
}

func TestNewDriveStorageClient_MissingCredentials_IsValidationError(t *testing.T) {
	t.Parallel()

	_, err := newDriveStorageClient(context.Background(), map[string]any{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrValidation))
}
