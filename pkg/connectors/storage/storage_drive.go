package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
)

type driveFile struct {
	ID       string
	MimeType string
}

type driveFilesAPI interface {
	findByName(ctx context.Context, folderID, name string) (*driveFile, error)
	download(ctx context.Context, fileID string) (content []byte, contentType string, err error)
	create(ctx context.Context, folderID, name, contentType string, content []byte) (*driveFile, error)
	update(ctx context.Context, fileID, contentType string, content []byte) error
	delete(ctx context.Context, fileID string) error
}

type driveStorageClient struct {
	api driveFilesAPI
}

func NewDriveStorageProvider(ctx context.Context, params map[string]any) (ProviderClient, error) {
	return newDriveStorageClient(ctx, params)
}

func newDriveStorageClient(ctx context.Context, params map[string]any) (ProviderClient, error) {
	serviceAccountKey := shared.StringField(params, "driveServiceAccountKey")
	if serviceAccountKey == "" {
		return nil, fmt.Errorf("%w: driveServiceAccountKey is required for google-drive", shared.ErrValidation)
	}

	svc, err := drive.NewService(ctx,
		option.WithAuthCredentialsJSON(option.ServiceAccount, []byte(serviceAccountKey)),
		option.WithScopes(drive.DriveScope),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: google-drive service: %s", shared.ErrUpstream, err)
	}
	return &driveStorageClient{api: &realDriveFilesAPI{files: svc.Files}}, nil
}

func (c *driveStorageClient) Fetch(ctx context.Context, folderID, name string) ([]byte, string, error) {
	f, err := c.api.findByName(ctx, folderID, name)
	if err != nil {
		return nil, "", fmt.Errorf("%w: drive fetch %q: %s", shared.ErrUpstream, name, err)
	}
	if f == nil {
		return nil, "", fmt.Errorf("%w: drive fetch: no file named %q in folder %q", shared.ErrUpstream, name, folderID)
	}
	content, contentType, err := c.api.download(ctx, f.ID)
	if err != nil {
		return nil, "", fmt.Errorf("%w: drive fetch %q: %s", shared.ErrUpstream, name, err)
	}
	return content, contentType, nil
}

func (c *driveStorageClient) Upload(ctx context.Context, folderID, name string, content []byte, contentType string) error {
	existing, err := c.api.findByName(ctx, folderID, name)
	if err != nil {
		return fmt.Errorf("%w: drive upload %q: %s", shared.ErrUpstream, name, err)
	}
	if existing != nil {
		if err := c.api.update(ctx, existing.ID, contentType, content); err != nil {
			return fmt.Errorf("%w: drive upload %q: update: %s", shared.ErrUpstream, name, err)
		}
		return nil
	}
	if _, err := c.api.create(ctx, folderID, name, contentType, content); err != nil {
		return fmt.Errorf("%w: drive upload %q: create: %s", shared.ErrUpstream, name, err)
	}
	return nil
}

func (c *driveStorageClient) Delete(ctx context.Context, folderID, name string) error {
	f, err := c.api.findByName(ctx, folderID, name)
	if err != nil {
		return fmt.Errorf("%w: drive delete %q: %s", shared.ErrUpstream, name, err)
	}
	if f == nil {
		return nil
	}
	if err := c.api.delete(ctx, f.ID); err != nil {
		return fmt.Errorf("%w: drive delete %q: %s", shared.ErrUpstream, name, err)
	}
	return nil
}

type realDriveFilesAPI struct {
	files *drive.FilesService
}

func (r *realDriveFilesAPI) findByName(ctx context.Context, folderID, name string) (*driveFile, error) {
	query := fmt.Sprintf("%q in parents and name = %q and trashed = false", folderID, name)
	list, err := r.files.List().Q(query).Fields("files(id, mimeType)").PageSize(1).
		IncludeItemsFromAllDrives(true).SupportsAllDrives(true).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	if len(list.Files) == 0 {
		return nil, nil
	}
	f := list.Files[0]
	return &driveFile{ID: f.Id, MimeType: f.MimeType}, nil
}

func (r *realDriveFilesAPI) download(ctx context.Context, fileID string) ([]byte, string, error) {
	resp, err := r.files.Get(fileID).SupportsAllDrives(true).Context(ctx).Download()
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return content, resp.Header.Get("Content-Type"), nil
}

func (r *realDriveFilesAPI) create(ctx context.Context, folderID, name, contentType string, content []byte) (*driveFile, error) {
	f := &drive.File{Name: name, Parents: []string{folderID}, MimeType: contentType}
	created, err := r.files.Create(f).Media(bytes.NewReader(content)).SupportsAllDrives(true).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	return &driveFile{ID: created.Id, MimeType: created.MimeType}, nil
}

func (r *realDriveFilesAPI) update(ctx context.Context, fileID, contentType string, content []byte) error {
	_, err := r.files.Update(fileID, &drive.File{MimeType: contentType}).Media(bytes.NewReader(content)).SupportsAllDrives(true).Context(ctx).Do()
	return err
}

func (r *realDriveFilesAPI) delete(ctx context.Context, fileID string) error {
	return r.files.Delete(fileID).SupportsAllDrives(true).Context(ctx).Do()
}

var (
	_ ProviderClient = (*driveStorageClient)(nil)
	_ driveFilesAPI  = (*realDriveFilesAPI)(nil)
)
