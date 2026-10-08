package storage

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

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
)

const gcsScope = "https://www.googleapis.com/auth/devstorage.read_write"

type gocloudStorageClient struct {
	bucket *blob.Bucket
}

func NewGocloudStorageProvider(ctx context.Context, params map[string]any) (ProviderClient, error) {
	provider := shared.StringField(params, "provider")
	if provider == "" {
		provider = "aws-s3"
	}
	return newGocloudStorageClient(ctx, provider, shared.StringField(params, "bucket"), params)
}

func newGocloudStorageClient(ctx context.Context, provider string, bucketName string, params map[string]any) (ProviderClient, error) {
	switch provider {
	case "aws-s3":
		return openS3Bucket(ctx, bucketName, params)
	case "azure-blob":
		return openAzureBucket(ctx, bucketName, params)
	case "gcp-gcs":
		return openGCSBucket(ctx, bucketName, params)
	default:
		return nil, fmt.Errorf("%w: gocloud storage client does not support provider %q", shared.ErrValidation, provider)
	}
}

func openS3Bucket(ctx context.Context, bucketName string, params map[string]any) (ProviderClient, error) {
	accessKey := shared.StringField(params, "accessKey")
	secretKey := shared.StringField(params, "secretKey")
	region := shared.StringField(params, "region")
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("%w: accessKey and secretKey are required for aws-s3", shared.ErrValidation)
	}

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: aws-s3 config: %s", shared.ErrUpstream, err)
	}

	bucket, err := s3blob.OpenBucket(ctx, s3.NewFromConfig(cfg), bucketName, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: aws-s3 open bucket %q: %s", shared.ErrUpstream, bucketName, err)
	}
	return &gocloudStorageClient{bucket: bucket}, nil
}

func openAzureBucket(ctx context.Context, containerName string, params map[string]any) (ProviderClient, error) {
	accountName := shared.StringField(params, "azureAccountName")
	accountKey := shared.StringField(params, "azureAccountKey")
	if accountName == "" || accountKey == "" {
		return nil, fmt.Errorf("%w: azureAccountName and azureAccountKey are required for azure-blob", shared.ErrValidation)
	}

	cred, err := container.NewSharedKeyCredential(accountName, accountKey)
	if err != nil {
		return nil, fmt.Errorf("%w: azure-blob credential: %s", shared.ErrValidation, err)
	}
	containerClient, err := container.NewClientWithSharedKeyCredential(
		fmt.Sprintf("https://%s.blob.core.windows.net/%s", accountName, containerName),
		cred,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: azure-blob client: %s", shared.ErrUpstream, err)
	}

	bucket, err := azureblob.OpenBucket(ctx, containerClient, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: azure-blob open container %q: %s", shared.ErrUpstream, containerName, err)
	}
	return &gocloudStorageClient{bucket: bucket}, nil
}

func openGCSBucket(ctx context.Context, bucketName string, params map[string]any) (ProviderClient, error) {
	serviceAccountKey := shared.StringField(params, "gcpServiceAccountKey")
	if serviceAccountKey == "" {
		return nil, fmt.Errorf("%w: gcpServiceAccountKey is required for gcp-gcs", shared.ErrValidation)
	}

	creds, err := google.CredentialsFromJSONWithType(ctx, []byte(serviceAccountKey), google.ServiceAccount, gcsScope)
	if err != nil {
		return nil, fmt.Errorf("%w: gcp-gcs credentials: %s", shared.ErrUpstream, err)
	}
	httpClient, err := gcp.NewHTTPClient(gcp.DefaultTransport(), gcp.CredentialsTokenSource(creds))
	if err != nil {
		return nil, fmt.Errorf("%w: gcp-gcs http client: %s", shared.ErrUpstream, err)
	}

	bucket, err := gcsblob.OpenBucket(ctx, httpClient, bucketName, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: gcp-gcs open bucket %q: %s", shared.ErrUpstream, bucketName, err)
	}
	return &gocloudStorageClient{bucket: bucket}, nil
}

func (c *gocloudStorageClient) Fetch(ctx context.Context, _, key string) ([]byte, string, error) {
	r, err := c.bucket.NewReader(ctx, key, nil)
	if err != nil {
		return nil, "", fmt.Errorf("%w: gocloud fetch %q: %s", shared.ErrUpstream, key, err)
	}
	defer func() { _ = r.Close() }()

	content, err := io.ReadAll(r)
	if err != nil {
		return nil, "", fmt.Errorf("%w: gocloud fetch %q: read: %s", shared.ErrUpstream, key, err)
	}
	return content, r.ContentType(), nil
}

func (c *gocloudStorageClient) Upload(ctx context.Context, _, key string, content []byte, contentType string) error {
	w, err := c.bucket.NewWriter(ctx, key, &blob.WriterOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("%w: gocloud upload %q: %s", shared.ErrUpstream, key, err)
	}
	if _, err := w.Write(content); err != nil {
		_ = w.Close()
		return fmt.Errorf("%w: gocloud upload %q: write: %s", shared.ErrUpstream, key, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("%w: gocloud upload %q: commit: %s", shared.ErrUpstream, key, err)
	}
	return nil
}

func (c *gocloudStorageClient) Delete(ctx context.Context, _, key string) error {
	if err := c.bucket.Delete(ctx, key); err != nil {
		return fmt.Errorf("%w: gocloud delete %q: %s", shared.ErrUpstream, key, err)
	}
	return nil
}

var _ ProviderClient = (*gocloudStorageClient)(nil)
