package gocloud

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	gcs "cloud.google.com/go/storage"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

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
	// provider selects the key rules (unaddressableKey); "" (in-memory test
	// buckets only) applies the rules every provider shares.
	provider string
	// httpClient holds this client's own transport, so Close can drop its
	// idle connections; nil for in-memory test buckets.
	httpClient *http.Client
	// objectMissing reports whether a failed delete means only that the
	// object does not exist, in a bucket that does: gocloud maps a missing
	// bucket, a wrong S3 region, a GCS 403 and an unknown Azure account to
	// the same NotFound code, and none of those is "already deleted". A
	// non-nil error replaces the delete's own and names what failed (the
	// provider's bucket check, or the delete with an explanation added).
	// Every provider sets it; nil (in-memory test buckets only) falls back
	// to gocloud's NotFound code.
	objectMissing func(ctx context.Context, err error) (bool, error)
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
		// The library never retries: one request per call, and the worker
		// decides from the error's class (connectors.DecideRetry).
		Retryer: func() aws.Retryer { return aws.NopRetryer{} },
	}

	client := newS3Client(cfg)
	bucket, err := s3blob.OpenBucket(ctx, client, bucketName, nil)
	if err != nil {
		return nil, upstream(fmt.Sprintf("aws-s3 open bucket %q", bucketName), err)
	}
	headBucket := func(ctx context.Context) error {
		_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucketName)})
		return err
	}
	return &gocloudStorageClient{bucket: bucket, provider: "aws-s3", httpClient: httpClient, objectMissing: s3ObjectMissing(headBucket)}, nil
}

// s3ListBucketHint explains an S3 403 on a delete: S3 answers 403 rather
// than 404 for a missing object to a credential without s3:ListBucket, so
// such a credential can never delete an already-deleted object.
const s3ListBucketHint = "access denied: the aws-s3 credential needs s3:GetObject and s3:DeleteObject on the objects " +
	"and s3:ListBucket on the bucket (without s3:ListBucket, S3 answers 403 rather than 404 for a missing object, " +
	"so a delete of an already-deleted object fails)"

// s3Forbidden returns an HTTP 403 with s3ListBucketHint added, keeping err
// (and its class) in the chain; nil for any other error.
func s3Forbidden(err error) error {
	var status interface{ HTTPStatusCode() int }
	if errors.As(err, &status) && status.HTTPStatusCode() == http.StatusForbidden {
		return fmt.Errorf("%s: %w", s3ListBucketHint, err)
	}
	return nil
}

// s3ObjectMissing: gocloud deletes an S3 object by reading its attributes
// first (HeadObject), and a HEAD response has no body, so a missing object
// and a missing bucket both come back as a bare 404 "NotFound". The bucket is
// then checked with HeadBucket: an object can only be missing from a bucket
// that exists. (HeadObject answers 404 rather than 403 only to a credential
// with s3:ListBucket, which is what HeadBucket needs.) A wrong region (301),
// a forbidden key or any other code is never "already deleted"; a 403, from
// the delete or the bucket check, says which permissions are needed.
func s3ObjectMissing(headBucket func(context.Context) error) func(context.Context, error) (bool, error) {
	return func(ctx context.Context, err error) (bool, error) {
		if forbidden := s3Forbidden(err); forbidden != nil {
			return false, forbidden
		}
		var coded interface{ ErrorCode() string }
		if !errors.As(err, &coded) {
			return false, nil
		}
		switch coded.ErrorCode() {
		case "NoSuchKey":
			return true, nil
		case "NotFound":
			if err := headBucket(ctx); err != nil {
				if forbidden := s3Forbidden(err); forbidden != nil {
					err = forbidden
				}
				return false, fmt.Errorf("aws-s3 bucket check (HeadBucket): %w", err)
			}
			return true, nil
		}
		return false, nil
	}
}

// azureObjectMissing: only Azure's BlobNotFound means the blob is missing
// from a container that exists; ContainerNotFound, an unknown account (DNS
// "no such host") and any other 404 are failures. A blob with snapshots
// (409 SnapshotsPresent) is never deleted together with them: the error says
// so, and stays permanent.
func azureObjectMissing(_ context.Context, err error) (bool, error) {
	if bloberror.HasCode(err, bloberror.SnapshotsPresent) {
		return false, fmt.Errorf("%s: %w", azureSnapshotsHint, err)
	}
	return bloberror.HasCode(err, bloberror.BlobNotFound), nil
}

// azureSnapshotsHint explains a 409 SnapshotsPresent on a delete.
const azureSnapshotsHint = "the blob has snapshots (azure SnapshotsPresent): the connector never deletes snapshots, " +
	"so delete the snapshots, or the blob with its snapshots, outside the connector, then retry"

// gcsObjectMissing: the GCS client reports a 404 on an object call as
// "object doesn't exist" whether the object or its bucket is missing. The
// API's message differs, but its wording is not a contract (and a proxy can
// rewrite it), so a 404 is "already deleted" only once listBucket confirms
// the bucket exists: a missing bucket fails the list with its own 404,
// permanent. Listing needs storage.objects.list, which every object role
// (roles/storage.objectAdmin, objectUser, objectViewer) grants, where reading
// the bucket's metadata needs storage.buckets.get, which they do not. A 403
// (which gocloud also maps to NotFound) is never "already deleted".
func gcsObjectMissing(listBucket func(context.Context) error) func(context.Context, error) (bool, error) {
	return func(ctx context.Context, err error) (bool, error) {
		var apiErr *googleapi.Error
		if !errors.As(err, &apiErr) || apiErr.Code != http.StatusNotFound {
			return false, nil
		}
		if err := listBucket(ctx); err != nil {
			return false, fmt.Errorf("gcp-gcs bucket check (objects.list): %w", err)
		}
		return true, nil
	}
}

// gcsListBucket lists at most one object of the bucket: it succeeds exactly
// when the bucket exists and the credential may list it.
func gcsListBucket(bucket *blob.Bucket) func(context.Context) error {
	return func(ctx context.Context) error {
		_, _, err := bucket.ListPage(ctx, blob.FirstPageToken, 1, nil)
		return err
	}
}

// codeNotFound is the fallback for a bucket with no provider check
// (in-memory test buckets).
func codeNotFound(_ context.Context, err error) (bool, error) {
	return gcerrors.Code(err) == gcerrors.NotFound, nil
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
	httpClient := newTransportClient()
	containerClient, err := newAzureContainerClient(
		fmt.Sprintf("https://%s.blob.core.windows.net/%s", accountName, containerName),
		cred,
		&container.ClientOptions{ClientOptions: azcore.ClientOptions{
			// One try per call (a negative MaxRetries): the library never
			// retries.
			Retry:     policy.RetryOptions{MaxRetries: -1},
			Transport: httpClient,
		}},
	)
	if err != nil {
		return nil, upstream("azure-blob client", err)
	}

	bucket, err := openAzureBlobBucket(ctx, containerClient, nil)
	if err != nil {
		return nil, upstream(fmt.Sprintf("azure-blob open container %q", containerName), err)
	}
	return &gocloudStorageClient{bucket: bucket, provider: "azure-blob", httpClient: httpClient, objectMissing: azureObjectMissing}, nil
}

// SDK constructors, replaceable in tests. With the arguments passed here they
// do not fail today, but their errors are still handled: an SDK upgrade must
// surface as an error, never as a nil client.
var (
	newAzureContainerClient = container.NewClientWithSharedKeyCredential
	openAzureBlobBucket     = azureblob.OpenBucket
	newGCSHTTPClient        = gcp.NewHTTPClient
	newGCSClient            = gcs.NewClient
	newS3Client             = func(cfg aws.Config) *s3.Client { return s3.NewFromConfig(cfg) }
	// newGCSTransport is the transport under the GCS OAuth layer: this
	// client's own, so Close drops only its idle connections.
	newGCSTransport = func() http.RoundTripper { return storageTransport() }
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
	// The GCS SDK (and gocloud's gcsblob) honours STORAGE_EMULATOR_HOST from
	// the process environment and would then send this tenant's requests,
	// without credentials, to that host. The library reads no other
	// environment; this one is refused rather than obeyed.
	if host := os.Getenv(storageEmulatorHostEnv); host != "" {
		return nil, upstream("gcp-gcs", fmt.Errorf("%w: %s is set on the worker (%q); unset it", errEmulatorHostSet, storageEmulatorHostEnv, host))
	}
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
	transport := newGCSTransport()
	httpClient, err := newGCSHTTPClient(transport, gcp.CredentialsTokenSource(creds))
	if err != nil {
		return nil, upstream("gcp-gcs http client", err)
	}
	// Never follow a redirect: the OAuth transport would re-send the request
	// and its bearer token to whichever host it names.
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	httpClient.Timeout = storageHTTPTimeout

	client, err := newGCSClient(ctx, option.WithHTTPClient(&httpClient.Client))
	if err != nil {
		return nil, upstream("gcp-gcs client", err)
	}
	// One try per call: the library never retries.
	client.SetRetry(gcs.WithPolicy(gcs.RetryNever))

	bucket, err := gcsblob.OpenBucket(ctx, nil, bucketName, &gcsblob.Options{Client: client})
	if err != nil {
		_ = client.Close()
		return nil, upstream(fmt.Sprintf("gcp-gcs open bucket %q", bucketName), err)
	}
	return &gocloudStorageClient{
		bucket:   bucket,
		provider: "gcp-gcs",
		// The OAuth layer cannot drop idle connections; a client on the
		// transport under it can.
		httpClient:    &http.Client{Transport: transport},
		objectMissing: gcsObjectMissing(gcsListBucket(bucket)),
	}, nil
}

func (c *gocloudStorageClient) Fetch(ctx context.Context, _, key string, maxBytes int64) ([]byte, string, error) {
	if err := c.checkKey(key); err != nil {
		return nil, "", err
	}
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
	if err := c.checkKey(key); err != nil {
		return err
	}
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

// Delete removes the object. An object that does not exist, in a bucket that
// does, is already deleted: Delete is idempotent, so a retry after a lost
// response, or a delete of an object removed elsewhere, succeeds. A missing
// bucket or container, a wrong region, a forbidden request or an unknown
// account is an error (permanent), never success.
func (c *gocloudStorageClient) Delete(ctx context.Context, _, key string) error {
	if err := c.checkKey(key); err != nil {
		return err
	}
	err := c.bucket.Delete(ctx, key)
	if err == nil {
		return nil
	}
	check := c.objectMissing
	if check == nil {
		check = codeNotFound
	}
	missing, checkErr := check(ctx, err)
	switch {
	case checkErr != nil:
		return upstream(fmt.Sprintf("gocloud delete %q", key), checkErr)
	case missing:
		return nil
	}
	return upstream(fmt.Sprintf("gocloud delete %q", key), err)
}

// upstream wraps a provider error as ErrUpstream with its class.
func upstream(op string, err error) error {
	class, reason := classify(err)
	return fmt.Errorf("%w: %s: %w", shared.ErrUpstream, op, shared.WithClass(class, reason, err))
}

// classify reads the provider's own signal first: an Azure or GCS response's
// HTTP status (neither SDK error exposes one the shared classifier reads), an
// Azure account host that does not resolve, then an S3 error code, an HTTP
// status or a network failure (shared.ClassifyCause), else gocloud's portable
// error code.
//
// An Azure NXDOMAIN is unknown, neither transient nor permanent: the host is
// derived from the account name, so it usually means the account does not
// exist, but a private-endpoint or split-horizon DNS outage answers NXDOMAIN
// for a real account too. Unknown is never retried automatically
// (connectors.DecideRetry), as a permanent error would not be, but it does
// not tell the worker the account is misconfigured when it may be fine: the
// call can be re-run by a person once DNS is back.
func classify(err error) (shared.Class, string) {
	var azErr *azcore.ResponseError
	if errors.As(err, &azErr) && azErr.StatusCode != 0 {
		return shared.ClassifyHTTPStatus(azErr.StatusCode), fmt.Sprintf("azure http %d", azErr.StatusCode)
	}
	var gErr *googleapi.Error
	if errors.As(err, &gErr) && gErr.Code != 0 {
		return shared.ClassifyHTTPStatus(gErr.Code), fmt.Sprintf("gcs http %d", gErr.Code)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound && isAzureBlobHost(dnsErr.Name) {
		return shared.ClassUnknown, "azure account host not found (NXDOMAIN)"
	}
	class, reason := shared.ClassifyCause(err)
	if class == shared.ClassUnknown {
		class, reason = classifyCode(gcerrors.Code(err))
	}
	return class, reason
}

func isAzureBlobHost(host string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSuffix(host, ".")), ".blob.core.windows.net")
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

// storageHTTPTimeout bounds a whole storage request, body included. It is
// longer than shared.ProviderHTTPTimeout because a request carries up to
// shared.MaxObjectBytes (50 MiB): 10 minutes still lets that through at about
// 0.7 Mbit/s. A server that does not start answering is cut off sooner, by
// the transport's ResponseHeaderTimeout (shared.ProviderHTTPTimeout).
const storageHTTPTimeout = 10 * time.Minute

const storageEmulatorHostEnv = "STORAGE_EMULATOR_HOST"

// errEmulatorHostSet: the worker's environment would redirect GCS traffic.
var errEmulatorHostSet = errors.New("gcp-gcs refuses to run with an emulator host configured")

// storageTransport is a storage client's own transport: Go's defaults
// (shared.BaseTransport), with a bound on the wait for response headers.
func storageTransport() *http.Transport {
	t := shared.BaseTransport()
	t.ResponseHeaderTimeout = shared.ProviderHTTPTimeout
	return t
}

// newTransportClient gives a client its own connection pool, so closing it
// never affects another tenant's client. Each request is bounded by
// storageHTTPTimeout (a call's earlier deadline still wins), and a redirect
// is returned, never followed: it would re-send the signed request to
// whichever host the response names.
func newTransportClient() *http.Client {
	return &http.Client{
		Transport:     storageTransport(),
		Timeout:       storageHTTPTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// checkKey refuses, before any request, a key the provider's gocloud driver
// cannot address (unaddressableKey).
func (c *gocloudStorageClient) checkKey(key string) error {
	if problem := unaddressableKey(c.provider, key); problem != "" {
		return fmt.Errorf("%w: key %q %s, which the gocloud %s driver cannot address "+
			"(it would rewrite the name to a hex escape and read, write or delete a different object)",
			shared.ErrValidation, key, problem, providerLabel(c.provider))
	}
	return nil
}

func providerLabel(provider string) string {
	if provider == "" {
		return "storage"
	}
	return provider
}

// azureEscapedChars are the printable characters gocloud's azureblob driver
// hex-escapes in a blob name.
const azureEscapedChars = "\"#%?\\"

// unaddressableKey says why gocloud's driver for provider would not use key
// as the object name, or "" when it would. The drivers (gocloud.dev v0.46)
// rewrite such characters to "__0xNN__": a blob named "Invoice #123.pdf" on
// Azure would be read as "Invoice __0x23__123.pdf" (never found) and its
// delete would succeed on a name that does not exist. Every driver escapes
// control characters and the "/" of "../"; azureblob also escapes
// " # % ? \ and DEL, and a trailing "/" (refused here for any name, since
// Azure cannot address it consistently).
func unaddressableKey(provider, key string) string {
	if strings.Contains(key, "../") {
		return `contains "../"`
	}
	for _, r := range key {
		switch {
		case r < 0x20:
			return fmt.Sprintf("contains the control character %q", r)
		case provider == "azure-blob" && (r == 0x7f || strings.ContainsRune(azureEscapedChars, r)):
			return fmt.Sprintf("contains %q", r)
		}
	}
	if provider == "azure-blob" && strings.HasSuffix(key, "/") {
		return `ends with "/"`
	}
	return ""
}
