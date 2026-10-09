package gocloud

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gocloud.dev/blob"
	"gocloud.dev/blob/azureblob"
	"gocloud.dev/blob/driver"
	"gocloud.dev/gcerrors"
	"gocloud.dev/gcp"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// fakeDriver is a blob driver whose operations fail on demand, for the error
// paths no in-memory bucket can produce.
type fakeDriver struct {
	driver.Bucket // unused methods panic

	readErr, writeErr, commitErr, deleteErr, openWriterErr error
}

func (f *fakeDriver) ErrorCode(err error) gcerrors.ErrorCode {
	if errors.Is(err, errNotFound) {
		return gcerrors.NotFound
	}
	return gcerrors.Unknown
}
func (f *fakeDriver) ErrorAs(error, any) bool { return false }
func (f *fakeDriver) Close() error            { return nil }

func (f *fakeDriver) NewRangeReader(context.Context, string, int64, int64, *driver.ReaderOptions) (driver.Reader, error) {
	return &fakeReader{err: f.readErr}, nil
}

func (f *fakeDriver) NewTypedWriter(context.Context, string, string, *driver.WriterOptions) (driver.Writer, error) {
	if f.openWriterErr != nil {
		return nil, f.openWriterErr
	}
	return &fakeWriter{writeErr: f.writeErr, commitErr: f.commitErr}, nil
}

func (f *fakeDriver) Delete(context.Context, string) error { return f.deleteErr }

type fakeReader struct{ err error }

func (r *fakeReader) Read([]byte) (int, error) { return 0, r.err }
func (r *fakeReader) Close() error             { return nil }
func (r *fakeReader) Attributes() *driver.ReaderAttributes {
	return &driver.ReaderAttributes{Size: 4, ContentType: "text/plain"}
}
func (r *fakeReader) As(any) bool { return false }

type fakeWriter struct{ writeErr, commitErr error }

func (w *fakeWriter) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return len(p), nil
}
func (w *fakeWriter) Close() error { return w.commitErr }

var errNotFound = errors.New("fake: not found")

func fakeClient(t *testing.T, d *fakeDriver) *gocloudStorageClient {
	t.Helper()
	bucket := blob.NewBucket(d)
	t.Cleanup(func() { _ = bucket.Close() })
	return &gocloudStorageClient{bucket: bucket}
}

func TestGocloudStorageClient_FetchReadFailure_IsClassified(t *testing.T) {
	t.Parallel()
	client := fakeClient(t, &fakeDriver{readErr: errors.New("connection reset")})

	_, _, err := client.Fetch(context.Background(), "", "k", 1<<20)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `gocloud fetch "k": read`)
}

func TestGocloudStorageClient_UploadFailures_AreUpstream(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		driver *fakeDriver
		want   string
	}{
		"open writer": {&fakeDriver{openWriterErr: errors.New("refused")}, `gocloud upload "k"`},
		"write":       {&fakeDriver{writeErr: errors.New("broken pipe")}, `gocloud upload "k": write`},
		"commit":      {&fakeDriver{commitErr: errors.New("precondition")}, `gocloud upload "k": commit`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := fakeClient(t, tc.driver).Upload(context.Background(), "", "k", []byte("data"), "text/plain")
			require.Error(t, err)
			assert.ErrorIs(t, err, shared.ErrUpstream)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// Deleting an object that does not exist succeeds: delete is idempotent.
func TestGocloudStorageClient_Delete_NotFoundIsSuccess(t *testing.T) {
	t.Parallel()
	assert.NoError(t, fakeClient(t, &fakeDriver{deleteErr: errNotFound}).Delete(context.Background(), "", "k"))
}

// Any other delete failure is an upstream error with its class.
func TestGocloudStorageClient_DeleteFailure_IsUpstream(t *testing.T) {
	t.Parallel()
	err := fakeClient(t, &fakeDriver{deleteErr: errors.New("refused")}).Delete(context.Background(), "", "k")
	require.ErrorIs(t, err, shared.ErrUpstream)
	assert.Contains(t, err.Error(), `gocloud delete "k"`)
}

func TestClassifyCode(t *testing.T) {
	t.Parallel()
	for code, want := range map[gcerrors.ErrorCode]shared.Class{
		gcerrors.NotFound:          shared.ClassPermanent,
		gcerrors.Unimplemented:     shared.ClassPermanent,
		gcerrors.ResourceExhausted: shared.ClassTransient,
		gcerrors.DeadlineExceeded:  shared.ClassTransient,
		gcerrors.Internal:          shared.ClassTransient,
		gcerrors.Unknown:           shared.ClassUnknown,
		gcerrors.Canceled:          shared.ClassUnknown,
	} {
		class, reason := classifyCode(code)
		assert.Equal(t, want, class, code.String())
		assert.Equal(t, "gocloud "+code.String(), reason)
	}
}

var azureKey = base64.StdEncoding.EncodeToString([]byte("not-a-real-azure-account-key-123"))

func serviceAccountJSON(t *testing.T, privateKeyPEM string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "p",
		"private_key_id": "kid",
		"private_key":    privateKeyPEM,
		"client_email":   "sa@p.iam.gserviceaccount.com",
		"token_uri":      "https://oauth2.googleapis.com/token",
	})
	require.NoError(t, err)
	return string(raw)
}

func TestNewProvider_OpensEachProvider(t *testing.T) {
	t.Parallel()
	for name, params := range map[string]map[string]any{
		"default is aws-s3": {"bucket": "b", "accessKey": "a", "secretKey": "s", "region": "us-east-1"},
		"azure-blob":        {"provider": "azure-blob", "bucket": "docs", "azureAccountName": "acct01", "azureAccountKey": azureKey},
		"gcp-gcs":           {"provider": "gcp-gcs", "bucket": "b", "gcpServiceAccountKey": serviceAccountJSON(t, "unused")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client, err := NewProvider(context.Background(), params)
			require.NoError(t, err)
			require.NotNil(t, client)
			assert.NoError(t, client.(io.Closer).Close())
		})
	}
}

func TestNewProvider_UnknownProvider_IsValidationError(t *testing.T) {
	t.Parallel()
	_, err := NewProvider(context.Background(), map[string]any{"provider": "ftp"})
	assert.ErrorIs(t, err, shared.ErrValidation)
}

func TestNewProvider_MissingBucket_IsUpstream(t *testing.T) {
	t.Parallel()
	for name, params := range map[string]map[string]any{
		"aws-s3":  {"accessKey": "a", "secretKey": "s", "region": "us-east-1"},
		"gcp-gcs": {"provider": "gcp-gcs", "gcpServiceAccountKey": serviceAccountJSON(t, "unused")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := NewProvider(context.Background(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, shared.ErrUpstream)
			class, _ := shared.ClassOf(err)
			assert.Equal(t, shared.ClassUnknown, class, "a plain error has no portable code")
		})
	}
}

func TestOpenAzureBucket_Validation(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		container string
		params    map[string]any
	}{
		"key not base64":  {"docs", map[string]any{"azureAccountName": "acct01", "azureAccountKey": "%%%"}},
		"account invalid": {"docs", map[string]any{"azureAccountName": "evil.host:8443/x?", "azureAccountKey": azureKey}},
		"container bad":   {"Docs--x", map[string]any{"azureAccountName": "acct01", "azureAccountKey": azureKey}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := openAzureBucket(context.Background(), tc.container, tc.params)
			assert.ErrorIs(t, err, shared.ErrValidation)
		})
	}
}

func TestOpenGCSBucket_MalformedServiceAccount_IsValidationError(t *testing.T) {
	t.Parallel()
	// Passes the type/token_uri check but cannot be parsed as a credential.
	key := `{"type":"service_account","client_email":5}`
	_, err := openGCSBucket(context.Background(), "b", map[string]any{"gcpServiceAccountKey": key})
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrValidation)
	assert.Contains(t, err.Error(), "gcp-gcs credentials")
}

// redirectingTransport stands in for the network: it answers Google's token
// endpoint with a token, redirects every storage request to another host,
// and records whether that host was ever contacted.
type redirectingTransport struct {
	mu              sync.Mutex
	storageRequests int
	redirectTarget  []*http.Request
}

func (rt *redirectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if req.Body != nil {
		_ = req.Body.Close()
	}
	header := http.Header{}
	switch req.URL.Host {
	case "oauth2.googleapis.com":
		header.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: http.StatusOK, Header: header, Request: req,
			Body: io.NopCloser(strings.NewReader(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))}, nil
	case "attacker.invalid":
		rt.redirectTarget = append(rt.redirectTarget, req)
		return &http.Response{StatusCode: http.StatusOK, Header: header, Request: req, Body: io.NopCloser(strings.NewReader("x"))}, nil
	default:
		rt.storageRequests++
		header.Set("Location", "https://attacker.invalid/steal")
		return &http.Response{StatusCode: http.StatusFound, Header: header, Request: req, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
}

// The GCS client never follows a redirect: the bearer token would go with it.
// Not parallel: it swaps http.DefaultTransport, which the GCS transport and
// the token client both use.
func TestOpenGCSBucket_NeverFollowsRedirects(t *testing.T) {
	t.Setenv("STORAGE_EMULATOR_HOST", "")
	rt := &redirectingTransport{}
	orig := http.DefaultTransport
	http.DefaultTransport = rt
	t.Cleanup(func() { http.DefaultTransport = orig })

	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(pk)
	require.NoError(t, err)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	client, err := openGCSBucket(context.Background(), "b", map[string]any{"gcpServiceAccountKey": serviceAccountJSON(t, keyPEM)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })

	_, _, err = client.Fetch(context.Background(), "", "k", 1<<20)
	require.Error(t, err, "a redirect is returned, not followed")

	rt.mu.Lock()
	defer rt.mu.Unlock()
	assert.Positive(t, rt.storageRequests, "the request reached storage")
	assert.Empty(t, rt.redirectTarget, "the redirect target was never contacted")
}

// The SDK constructors do not fail with the arguments passed today, but their
// errors are handled so an SDK change surfaces as an error, not a nil client.
// Not parallel: it swaps package-level constructors (parallel tests start only
// after every sequential test has returned).
func TestOpenBucket_SDKConstructorFailures_AreUpstream(t *testing.T) {
	sdkErr := errors.New("sdk refused")
	azureParams := map[string]any{"azureAccountName": "acct01", "azureAccountKey": azureKey}

	t.Run("azure container client", func(t *testing.T) {
		orig := newAzureContainerClient
		t.Cleanup(func() { newAzureContainerClient = orig })
		newAzureContainerClient = func(string, *container.SharedKeyCredential, *container.ClientOptions) (*container.Client, error) {
			return nil, sdkErr
		}
		_, err := openAzureBucket(context.Background(), "docs", azureParams)
		assert.ErrorIs(t, err, shared.ErrUpstream)
		assert.ErrorIs(t, err, sdkErr)
	})

	t.Run("azure open bucket", func(t *testing.T) {
		orig := openAzureBlobBucket
		t.Cleanup(func() { openAzureBlobBucket = orig })
		openAzureBlobBucket = func(context.Context, *container.Client, *azureblob.Options) (*blob.Bucket, error) {
			return nil, sdkErr
		}
		_, err := openAzureBucket(context.Background(), "docs", azureParams)
		assert.ErrorIs(t, err, shared.ErrUpstream)
		assert.Contains(t, err.Error(), `azure-blob open container "docs"`)
	})

	t.Run("gcs http client", func(t *testing.T) {
		orig := newGCSHTTPClient
		t.Cleanup(func() { newGCSHTTPClient = orig })
		newGCSHTTPClient = func(http.RoundTripper, gcp.TokenSource) (*gcp.HTTPClient, error) {
			return nil, sdkErr
		}
		_, err := openGCSBucket(context.Background(), "b", map[string]any{"gcpServiceAccountKey": serviceAccountJSON(t, "unused")})
		assert.ErrorIs(t, err, shared.ErrUpstream)
		assert.Contains(t, err.Error(), "gcp-gcs http client")
	})
}
