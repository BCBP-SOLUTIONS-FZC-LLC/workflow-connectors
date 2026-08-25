package connectors

import (
	"context"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/oauth2/google"

	"gocloud.dev/blob"
	"gocloud.dev/blob/azureblob"
	"gocloud.dev/blob/gcsblob"
	"gocloud.dev/blob/s3blob"
	"gocloud.dev/gcp"
)

const gcsScope = "https://www.googleapis.com/auth/devstorage.read_write"

type gocloudStorageClient struct {
	bucket *blob.Bucket
}

func NewGocloudStorageProvider(ctx context.Context, params map[string]any) (StorageProviderClient, error) {
	provider := stringField(params, "provider")
	if provider == "" {
		provider = "aws-s3"
	}
	return newGocloudStorageClient(ctx, provider, stringField(params, "bucket"), params)
}

func newGocloudStorageClient(ctx context.Context, provider string, bucketName string, params map[string]any) (StorageProviderClient, error) {
	switch provider {
	case "aws-s3":
		return openS3Bucket(ctx, bucketName, params)
	case "azure-blob":
		return openAzureBucket(ctx, bucketName, params)
	case "gcp-gcs":
		return openGCSBucket(ctx, bucketName, params)
	default:
		return nil, fmt.Errorf("%w: gocloud storage client does not support provider %q", ErrValidation, provider)
	}
}

func openS3Bucket(ctx context.Context, bucketName string, params map[string]any) (StorageProviderClient, error) {
	accessKey := stringField(params, "accessKey")
	secretKey := stringField(params, "secretKey")
	region := stringField(params, "region")
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("%w: accessKey and secretKey are required for aws-s3", ErrValidation)
	}

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: aws-s3 config: %s", ErrUpstream, err)
	}

	bucket, err := s3blob.OpenBucket(ctx, s3.NewFromConfig(cfg), bucketName, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: aws-s3 open bucket %q: %s", ErrUpstream, bucketName, err)
	}
	return &gocloudStorageClient{bucket: bucket}, nil
}

func openAzureBucket(ctx context.Context, containerName string, params map[string]any) (StorageProviderClient, error) {
	accountName := stringField(params, "azureAccountName")
	accountKey := stringField(params, "azureAccountKey")
	if accountName == "" || accountKey == "" {
		return nil, fmt.Errorf("%w: azureAccountName and azureAccountKey are required for azure-blob", ErrValidation)
	}

	cred, err := container.NewSharedKeyCredential(accountName, accountKey)
	if err != nil {
		return nil, fmt.Errorf("%w: azure-blob credential: %s", ErrValidation, err)
	}
	containerClient, err := container.NewClientWithSharedKeyCredential(
		fmt.Sprintf("https://%s.blob.core.windows.net/%s", accountName, containerName),
		cred,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: azure-blob client: %s", ErrUpstream, err)
	}

	bucket, err := azureblob.OpenBucket(ctx, containerClient, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: azure-blob open container %q: %s", ErrUpstream, containerName, err)
	}
	return &gocloudStorageClient{bucket: bucket}, nil
}

func openGCSBucket(ctx context.Context, bucketName string, params map[string]any) (StorageProviderClient, error) {
	serviceAccountKey := stringField(params, "gcpServiceAccountKey")
	if serviceAccountKey == "" {
		return nil, fmt.Errorf("%w: gcpServiceAccountKey is required for gcp-gcs", ErrValidation)
	}

	creds, err := google.CredentialsFromJSON(ctx, []byte(serviceAccountKey), gcsScope)
	if err != nil {
		return nil, fmt.Errorf("%w: gcp-gcs credentials: %s", ErrUpstream, err)
	}
	httpClient, err := gcp.NewHTTPClient(gcp.DefaultTransport(), gcp.CredentialsTokenSource(creds))
	if err != nil {
		return nil, fmt.Errorf("%w: gcp-gcs http client: %s", ErrUpstream, err)
	}

	bucket, err := gcsblob.OpenBucket(ctx, httpClient, bucketName, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: gcp-gcs open bucket %q: %s", ErrUpstream, bucketName, err)
	}
	return &gocloudStorageClient{bucket: bucket}, nil
}

func (c *gocloudStorageClient) Fetch(ctx context.Context, _, key string) ([]byte, string, error) {
	r, err := c.bucket.NewReader(ctx, key, nil)
	if err != nil {
		return nil, "", fmt.Errorf("%w: gocloud fetch %q: %s", ErrUpstream, key, err)
	}
	defer func() { _ = r.Close() }()

	content, err := io.ReadAll(r)
	if err != nil {
		return nil, "", fmt.Errorf("%w: gocloud fetch %q: read: %s", ErrUpstream, key, err)
	}
	return content, r.ContentType(), nil
}

func (c *gocloudStorageClient) Upload(ctx context.Context, _, key string, content []byte, contentType string) error {
	w, err := c.bucket.NewWriter(ctx, key, &blob.WriterOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("%w: gocloud upload %q: %s", ErrUpstream, key, err)
	}
	if _, err := w.Write(content); err != nil {
		_ = w.Close()
		return fmt.Errorf("%w: gocloud upload %q: write: %s", ErrUpstream, key, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("%w: gocloud upload %q: commit: %s", ErrUpstream, key, err)
	}
	return nil
}

func (c *gocloudStorageClient) Delete(ctx context.Context, _, key string) error {
	if err := c.bucket.Delete(ctx, key); err != nil {
		return fmt.Errorf("%w: gocloud delete %q: %s", ErrUpstream, key, err)
	}
	return nil
}

var _ StorageProviderClient = (*gocloudStorageClient)(nil)
