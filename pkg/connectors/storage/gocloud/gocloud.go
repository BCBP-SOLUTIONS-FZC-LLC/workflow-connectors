package gocloud

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"gocloud.dev/blob"
	"gocloud.dev/blob/azureblob"
	"gocloud.dev/blob/gcsblob"
	"gocloud.dev/blob/s3blob"
	"gocloud.dev/gcerrors"
	"gocloud.dev/gcp"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
)

const gcsScope = "https://www.googleapis.com/auth/devstorage.read_write"

type gocloudStorageClient struct {
	bucket *blob.Bucket
	// httpClient is this client's own transport, when it has one (aws-s3),
	// so Close can drop its idle connections.
	httpClient *http.Client
}

func NewProvider(ctx context.Context, params map[string]any) (storage.ProviderClient, error) {
	provider := shared.StringField(params, "provider")
	if provider == "" {
		provider = "aws-s3"
	}
	return newGocloudStorageClient(ctx, provider, shared.StringField(params, "bucket"), params)
}

func newGocloudStorageClient(ctx context.Context, provider string, bucketName string, params map[string]any) (storage.ProviderClient, error) {
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

func openS3Bucket(ctx context.Context, bucketName string, params map[string]any) (storage.ProviderClient, error) {
	accessKey := shared.StringField(params, "accessKey")
	secretKey := shared.StringField(params, "secretKey")
	region := shared.StringField(params, "region")
	if accessKey == "" || secretKey == "" || region == "" {
		return nil, fmt.Errorf("%w: accessKey, secretKey, and region are required for aws-s3", shared.ErrValidation)
	}

	// Built from the tenant's values only. config.LoadDefaultConfig would also
	// read the worker's own environment and shared config (AWS_REGION,
	// AWS_ENDPOINT_URL, profiles), leaking them into every tenant's client.
	httpClient := newTransportClient()
	cfg := aws.Config{
		Region:      region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		HTTPClient:  httpClient,
	}

	bucket, err := s3blob.OpenBucket(ctx, s3.NewFromConfig(cfg), bucketName, nil)
	if err != nil {
		return nil, upstream(fmt.Sprintf("aws-s3 open bucket %q", bucketName), err)
	}
	return &gocloudStorageClient{bucket: bucket, httpClient: httpClient}, nil
}

func openAzureBucket(ctx context.Context, containerName string, params map[string]any) (storage.ProviderClient, error) {
	accountName := shared.StringField(params, "azureAccountName")
	accountKey := shared.StringField(params, "azureAccountKey")
	if accountName == "" || accountKey == "" {
		return nil, fmt.Errorf("%w: azureAccountName and azureAccountKey are required for azure-blob", shared.ErrValidation)
	}
	// Both names go into the service URL; anything but Azure's own naming
	// rules could change its host or path (an account of "internal:8443/x?"
	// would send the request, and its signed key, elsewhere).
	if !azureAccountPattern.MatchString(accountName) {
		return nil, fmt.Errorf("%w: azureAccountName must be 3-24 lowercase letters and digits", shared.ErrValidation)
	}
	if !validAzureContainer(containerName) {
		return nil, fmt.Errorf("%w: azure-blob container must be 3-63 lowercase letters, digits and single hyphens", shared.ErrValidation)
	}

	cred, err := container.NewSharedKeyCredential(accountName, accountKey)
	if err != nil {
		return nil, fmt.Errorf("%w: azure-blob credential: %s", shared.ErrValidation, err)
	}
	containerClient, err := newAzureContainerClient(
		fmt.Sprintf("https://%s.blob.core.windows.net/%s", accountName, containerName),
		cred,
		nil,
	)
	if err != nil {
		return nil, upstream("azure-blob client", err)
	}

	bucket, err := openAzureBlobBucket(ctx, containerClient, nil)
	if err != nil {
		return nil, upstream(fmt.Sprintf("azure-blob open container %q", containerName), err)
	}
	return &gocloudStorageClient{bucket: bucket}, nil
}

// SDK constructors, replaceable in tests. With the arguments passed here they
// do not fail today, but their errors are still handled: an SDK upgrade must
// surface as an error, never as a nil client.
var (
	newAzureContainerClient = container.NewClientWithSharedKeyCredential
	openAzureBlobBucket     = azureblob.OpenBucket
	newGCSHTTPClient        = gcp.NewHTTPClient
)

var azureAccountPattern = regexp.MustCompile(`^[a-z0-9]{3,24}$`)

// validAzureContainer applies Azure's container naming rules: 3-63
// characters, lowercase letters, digits and hyphens, starting and ending
// with a letter or digit, with no consecutive hyphens.
func validAzureContainer(name string) bool {
	if len(name) < 3 || len(name) > 63 || strings.Contains(name, "--") {
		return false
	}
	return azureContainerPattern.MatchString(name)
}

var azureContainerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

func openGCSBucket(ctx context.Context, bucketName string, params map[string]any) (storage.ProviderClient, error) {
	serviceAccountKey := shared.StringField(params, "gcpServiceAccountKey")
	if serviceAccountKey == "" {
		return nil, fmt.Errorf("%w: gcpServiceAccountKey is required for gcp-gcs", shared.ErrValidation)
	}

	if err := shared.ValidateGoogleServiceAccountKey([]byte(serviceAccountKey), "gcpServiceAccountKey"); err != nil {
		return nil, err
	}
	creds, err := google.CredentialsFromJSONWithType(tokenContext(), []byte(serviceAccountKey), google.ServiceAccount, gcsScope)
	if err != nil {
		return nil, fmt.Errorf("%w: gcp-gcs credentials: %w", shared.ErrValidation, err)
	}
	httpClient, err := newGCSHTTPClient(gcp.DefaultTransport(), gcp.CredentialsTokenSource(creds))
	if err != nil {
		return nil, upstream("gcp-gcs http client", err)
	}
	// Never follow a redirect: the OAuth transport would re-send the request
	// and its bearer token to whichever host it names.
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	bucket, err := gcsblob.OpenBucket(ctx, httpClient, bucketName, nil)
	if err != nil {
		return nil, upstream(fmt.Sprintf("gcp-gcs open bucket %q", bucketName), err)
	}
	return &gocloudStorageClient{bucket: bucket}, nil
}

func (c *gocloudStorageClient) Fetch(ctx context.Context, _, key string, maxBytes int64) ([]byte, string, error) {
	r, err := c.bucket.NewReader(ctx, key, nil)
	if err != nil {
		return nil, "", upstream(fmt.Sprintf("gocloud fetch %q", key), err)
	}
	defer func() { _ = r.Close() }()

	// Refused from the object's size before reading any of it.
	if r.Size() > maxBytes {
		return nil, "", shared.TooLarge(fmt.Sprintf("object %q", key), maxBytes)
	}
	content, err := shared.ReadAllLimited(r, maxBytes, fmt.Sprintf("object %q", key))
	if err != nil {
		return nil, "", shared.Classify(fmt.Sprintf("gocloud fetch %q: read", key), err)
	}
	return content, r.ContentType(), nil
}

func (c *gocloudStorageClient) Upload(ctx context.Context, _, key string, content []byte, contentType string) error {
	w, err := c.bucket.NewWriter(ctx, key, &blob.WriterOptions{ContentType: contentType})
	if err != nil {
		return upstream(fmt.Sprintf("gocloud upload %q", key), err)
	}
	if _, err := w.Write(content); err != nil {
		_ = w.Close()
		return upstream(fmt.Sprintf("gocloud upload %q: write", key), err)
	}
	if err := w.Close(); err != nil {
		return upstream(fmt.Sprintf("gocloud upload %q: commit", key), err)
	}
	return nil
}

// Delete removes the object. An object that does not exist is already
// deleted: Delete is idempotent, so a retry after a lost response, or a
// delete of an object removed elsewhere, succeeds.
func (c *gocloudStorageClient) Delete(ctx context.Context, _, key string) error {
	if err := c.bucket.Delete(ctx, key); err != nil && gcerrors.Code(err) != gcerrors.NotFound {
		return upstream(fmt.Sprintf("gocloud delete %q", key), err)
	}
	return nil
}

// upstream wraps a provider error as ErrUpstream with its class: the
// provider's own signal first (an S3 error code such as NoSuchKey or SlowDown,
// an HTTP status, a network failure), else gocloud's portable error code,
// which covers Azure and GCS.
func upstream(op string, err error) error {
	class, reason := shared.ClassifyCause(err)
	if class == shared.ClassUnknown {
		class, reason = classifyCode(gcerrors.Code(err))
	}
	return fmt.Errorf("%w: %s: %w", shared.ErrUpstream, op, shared.WithClass(class, reason, err))
}

func classifyCode(code gcerrors.ErrorCode) (shared.Class, string) {
	reason := "gocloud " + code.String()
	switch code {
	case gcerrors.NotFound, gcerrors.PermissionDenied, gcerrors.InvalidArgument,
		gcerrors.FailedPrecondition, gcerrors.AlreadyExists, gcerrors.Unimplemented:
		return shared.ClassPermanent, reason
	case gcerrors.ResourceExhausted, gcerrors.DeadlineExceeded, gcerrors.Internal:
		return shared.ClassTransient, reason
	default:
		return shared.ClassUnknown, reason
	}
}

var _ storage.ProviderClient = (*gocloudStorageClient)(nil)

// tokenContext gives OAuth token requests a bounded HTTP client. The token
// source outlives the call that built it (clients are cached), so it must
// not capture that call's context, and the default client has no timeout.
func tokenContext() context.Context {
	return context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: shared.DefaultHTTPTimeout})
}

// Close releases the bucket and this client's idle connections. The client
// cache calls it once the client is retired and no call is using it.
func (c *gocloudStorageClient) Close() error {
	err := c.bucket.Close()
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	return err
}

// newTransportClient gives a client its own connection pool, so closing it
// never affects another tenant's client.
func newTransportClient() *http.Client {
	return &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
}
