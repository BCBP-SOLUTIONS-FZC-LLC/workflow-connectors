package googledrive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// fakeDriveServer is an httptest stand-in for the Drive v3 REST API, enough
// for realDriveFilesAPI: list (paged), media download, multipart create,
// media update and delete. Requests are recorded for assertions.
type fakeDriveServer struct {
	t *testing.T

	mu      sync.Mutex
	queries []string

	listStatus int                       // non-zero: GET /files fails with this status
	listPages  map[string]map[string]any // pageToken ("" for the first) -> response body
	media      map[string]string         // fileID -> content served by alt=media
	status     map[string]int            // "METHOD fileID" -> forced status
	created    map[string]map[string]any // fileID -> metadata seen on create
	updated    map[string]map[string]any // fileID -> metadata seen on update
	uploads    map[string]string         // fileID -> uploaded content (create or update)
	nextID     string                    // id returned by create
}

func newFakeDriveServer(t *testing.T) (*fakeDriveServer, *realDriveFilesAPI) {
	t.Helper()
	s := &fakeDriveServer{
		t:         t,
		listPages: map[string]map[string]any{},
		media:     map[string]string{},
		status:    map[string]int{},
		created:   map[string]map[string]any{},
		updated:   map[string]map[string]any{},
		uploads:   map[string]string{},
		nextID:    "new-file",
	}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	svc, err := drive.NewService(context.Background(), option.WithHTTPClient(srv.Client()), option.WithEndpoint(srv.URL+"/"))
	require.NoError(t, err)
	return s, &realDriveFilesAPI{files: svc.Files}
}

func writeAPIError(w http.ResponseWriter, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `{"error":{"code":%d,"message":"fake","errors":[{"reason":"fake"}]}}`, code)
}

func (s *fakeDriveServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := strings.TrimPrefix(r.URL.Path, "/upload/drive/v3")
	id := strings.TrimPrefix(path, "/files/")
	if code := s.status[r.Method+" "+id]; code != 0 {
		writeAPIError(w, code)
		return
	}

	switch {
	case r.Method == http.MethodGet && path == "/files":
		s.queries = append(s.queries, r.URL.Query().Get("q"))
		if s.listStatus != 0 {
			writeAPIError(w, s.listStatus)
			return
		}
		body, ok := s.listPages[r.URL.Query().Get("pageToken")]
		if !ok {
			body = map[string]any{"files": []any{}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)

	case r.Method == http.MethodGet && r.URL.Query().Get("alt") == "media":
		content, ok := s.media[id]
		if !ok {
			writeAPIError(w, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = io.WriteString(w, content)

	case r.Method == http.MethodPost && path == "/files":
		meta, content := s.readMultipart(r)
		s.created[s.nextID] = meta
		s.uploads[s.nextID] = content
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": s.nextID, "mimeType": meta["mimeType"]})

	case r.Method == http.MethodPatch:
		meta, content := s.readMultipart(r)
		s.updated[id] = meta
		s.uploads[id] = content
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id})

	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)

	default:
		writeAPIError(w, http.StatusNotImplemented)
	}
}

// readMultipart splits a Drive multipart/related upload into its metadata
// part and its media part. It runs on the server goroutine, so it reports
// with assert (not require) and returns what it could read.
func (s *fakeDriveServer) readMultipart(r *http.Request) (map[string]any, string) {
	meta := map[string]any{}
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !assert.NoError(s.t, err) {
		return meta, ""
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	part, err := mr.NextPart()
	if !assert.NoError(s.t, err) || !assert.NoError(s.t, json.NewDecoder(part).Decode(&meta)) {
		return meta, ""
	}
	part, err = mr.NextPart()
	if !assert.NoError(s.t, err) {
		return meta, ""
	}
	content, err := io.ReadAll(part)
	assert.NoError(s.t, err)
	return meta, string(content)
}

func TestRealDrive_FindOne(t *testing.T) {
	t.Parallel()
	srv, api := newFakeDriveServer(t)
	ctx := context.Background()

	// No match.
	files, err := api.listByName(ctx, "fold'er", "a.pdf")
	require.NoError(t, err)
	assert.Empty(t, files)
	f, err := api.findByDocumentID(ctx, "folder", "doc-1")
	require.NoError(t, err)
	assert.Nil(t, f)

	// A match: the oldest file, with its document tag.
	srv.listPages[""] = map[string]any{"files": []any{
		map[string]any{"id": "f1", "mimeType": "application/pdf", "appProperties": map[string]string{appPropertyDocumentID: "doc-1"}},
	}}
	f, err = api.findByDocumentID(ctx, "folder", "doc-1")
	require.NoError(t, err)
	assert.Equal(t, &driveFile{ID: "f1", MimeType: "application/pdf", DocumentID: "doc-1"}, f)

	assert.Equal(t, `'fold\'er' in parents and name = 'a.pdf' and trashed = false`, srv.queries[0])
	assert.Equal(t, `'folder' in parents and appProperties has { key='connectorDocumentId' and value='doc-1' } and trashed = false`, srv.queries[2])

	// A Drive error is returned as is.
	srv.listStatus = http.StatusServiceUnavailable
	_, err = api.findByDocumentID(ctx, "folder", "doc-1")
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.Code)
}

func TestRealDrive_ListByDocumentID_FollowsPages(t *testing.T) {
	t.Parallel()
	srv, api := newFakeDriveServer(t)
	ctx := context.Background()

	srv.listPages[""] = map[string]any{"nextPageToken": "p2", "files": []any{
		map[string]any{"id": "f1", "appProperties": map[string]string{appPropertyDocumentID: "doc-1"}},
	}}
	srv.listPages["p2"] = map[string]any{"files": []any{
		map[string]any{"id": "f2", "mimeType": "text/plain", "appProperties": map[string]string{appPropertyDocumentID: "doc-1"}},
	}}
	files, err := api.listByDocumentID(ctx, "folder", "doc-1")
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, "f1", files[0].ID)
	assert.Equal(t, &driveFile{ID: "f2", MimeType: "text/plain", DocumentID: "doc-1"}, files[1])

	srv.listStatus = http.StatusInternalServerError
	_, err = api.listByDocumentID(ctx, "folder", "doc-1")
	require.Error(t, err)
}

func TestRealDrive_ListByName_FollowsPagesWithTags(t *testing.T) {
	t.Parallel()
	srv, api := newFakeDriveServer(t)
	srv.listPages[""] = map[string]any{"nextPageToken": "p2", "files": []any{
		map[string]any{"id": "f1", "appProperties": map[string]string{appPropertyDocumentID: "doc-1"}},
	}}
	srv.listPages["p2"] = map[string]any{"files": []any{map[string]any{"id": "f2"}}}

	files, err := api.listByName(context.Background(), "folder", "a.pdf")
	require.NoError(t, err)
	assert.Equal(t, []*driveFile{{ID: "f1", DocumentID: "doc-1"}, {ID: "f2"}}, files)
	assert.Equal(t, `'folder' in parents and name = 'a.pdf' and trashed = false`, srv.queries[0])
}

func TestRealDrive_Download(t *testing.T) {
	t.Parallel()
	srv, api := newFakeDriveServer(t)
	ctx := context.Background()
	srv.media["f1"] = "hello"

	content, ct, err := api.download(ctx, "f1", 1<<20)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(content))
	assert.Equal(t, "application/pdf", ct)

	_, _, err = api.download(ctx, "f1", 2)
	assert.ErrorIs(t, err, shared.ErrTooLarge)

	_, _, err = api.download(ctx, "missing", 1<<20)
	assert.ErrorIs(t, err, errDriveNotFound)
}

func TestRealDrive_CreateUpdateDelete(t *testing.T) {
	t.Parallel()
	srv, api := newFakeDriveServer(t)
	ctx := context.Background()

	f, err := api.create(ctx, "folder", "a.pdf", "application/pdf", "doc-1", []byte("v1"))
	require.NoError(t, err)
	assert.Equal(t, &driveFile{ID: "new-file", MimeType: "application/pdf", DocumentID: "doc-1"}, f)
	assert.Equal(t, "v1", srv.uploads["new-file"])
	assert.Equal(t, "a.pdf", srv.created["new-file"]["name"])
	assert.Equal(t, []any{"folder"}, srv.created["new-file"]["parents"])
	assert.Equal(t, map[string]any{appPropertyDocumentID: "doc-1"}, srv.created["new-file"]["appProperties"])

	require.NoError(t, api.update(ctx, "new-file", "application/pdf", "doc-1", []byte("v2")))
	assert.Equal(t, "v2", srv.uploads["new-file"])
	assert.Equal(t, map[string]any{appPropertyDocumentID: "doc-1"}, srv.updated["new-file"]["appProperties"],
		"an update tags the file, so an adopted file belongs to the document")

	require.NoError(t, api.delete(ctx, "new-file"))

	// Failures: a 404 maps to errDriveNotFound; anything else stays a Drive error.
	srv.status["PATCH gone"] = http.StatusNotFound
	assert.ErrorIs(t, api.update(ctx, "gone", "", "doc-1", nil), errDriveNotFound)
	srv.status["DELETE gone"] = http.StatusNotFound
	assert.ErrorIs(t, api.delete(ctx, "gone"), errDriveNotFound)

	srv.status["DELETE locked"] = http.StatusForbidden
	err = api.delete(ctx, "locked")
	require.Error(t, err)
	assert.NotErrorIs(t, err, errDriveNotFound)
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.Code)

	srv.status["POST /files"] = http.StatusInternalServerError
	_, err = api.create(ctx, "folder", "b.pdf", "", "doc-2", []byte("x"))
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusInternalServerError, apiErr.Code)
}

// ---- Construction -----------------------------------------------------------

const testServiceAccountKey = `{"type":"service_account","client_email":"svc@project.iam.gserviceaccount.com",` +
	`"private_key":"-----BEGIN PRIVATE KEY-----\nnot-used-until-a-token-is-requested\n-----END PRIVATE KEY-----\n",` +
	`"token_uri":"https://oauth2.googleapis.com/token"}`

func TestNewProvider_BuildsAClosableClient(t *testing.T) {
	t.Parallel()
	client, err := NewProvider(documents.NewMemoryStore())(context.Background(), map[string]any{"driveServiceAccountKey": testServiceAccountKey})
	require.NoError(t, err)
	dc, ok := client.(*driveStorageClient)
	require.True(t, ok)
	assert.NotNil(t, dc.httpClient)
	assert.Equal(t, documents.DefaultLease, dc.lease)
	assert.NoError(t, dc.Close())

	assert.NoError(t, newTestClient(newFakeDriveFilesAPI(), documents.NewMemoryStore()).Close(), "Close with no HTTP client")
}

// A key that passes the endpoint checks but cannot be parsed as credentials is
// a validation error.
func TestNewDriveStorageClient_UnparseableCredentials(t *testing.T) {
	t.Parallel()
	_, err := newDriveStorageClient(context.Background(),
		map[string]any{"driveServiceAccountKey": `{"type":"service_account","private_key":123}`}, documents.NewMemoryStore())
	require.ErrorIs(t, err, shared.ErrValidation)
	assert.Contains(t, err.Error(), "google-drive credentials")
}

// A Drive service that cannot be built (here: client certificates enabled
// with a corrupt certificate config) is an upstream error. Not parallel: it
// sets process environment.
func TestNewDriveStorageClient_ServiceBuildFailure(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "certificate_config.json")
	require.NoError(t, os.WriteFile(cfg, []byte("not json"), 0o600))
	t.Setenv("GOOGLE_API_USE_CLIENT_CERTIFICATE", "true")
	t.Setenv("GOOGLE_API_CERTIFICATE_CONFIG", cfg)

	_, err := newDriveStorageClient(context.Background(), map[string]any{"driveServiceAccountKey": testServiceAccountKey}, documents.NewMemoryStore())
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrUpstream), "got %v", err)
	assert.Contains(t, err.Error(), "google-drive service")
}
