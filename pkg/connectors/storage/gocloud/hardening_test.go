package gocloud

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gocloud.dev/gcp"
	"google.golang.org/api/option"

	gcs "cloud.google.com/go/storage"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
)

// countingServer answers every request with handler and counts them.
type countingServer struct {
	*httptest.Server
	requests atomic.Int32
}

func newCountingServer(t *testing.T, handler http.HandlerFunc) *countingServer {
	t.Helper()
	s := &countingServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

// openS3Stub opens the production aws-s3 client (its retryer, HTTP client
// and redirect policy) against srv. Not parallel-safe: it swaps newS3Client.
func openS3Stub(t *testing.T, srv *httptest.Server) storage.ProviderClient {
	t.Helper()
	orig := newS3Client
	t.Cleanup(func() { newS3Client = orig })
	newS3Client = func(cfg aws.Config) *s3.Client {
		return s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(srv.URL)
			o.UsePathStyle = true
		})
	}
	client, err := openS3Bucket(context.Background(), "bucket", map[string]any{"accessKey": "a", "secretKey": "s", "region": "us-east-1"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })
	return client
}

// openAzureStub opens the production azure-blob client (its retry and
// transport options) against srv. Not parallel-safe: it swaps
// newAzureContainerClient.
func openAzureStub(t *testing.T, srv *httptest.Server) storage.ProviderClient {
	t.Helper()
	orig := newAzureContainerClient
	t.Cleanup(func() { newAzureContainerClient = orig })
	newAzureContainerClient = func(_ string, cred *container.SharedKeyCredential, opts *container.ClientOptions) (*container.Client, error) {
		return orig(srv.URL+"/acct01/docs", cred, opts)
	}
	client, err := openAzureBucket(context.Background(), "docs", map[string]any{"azureAccountName": "acct01", "azureAccountKey": azureKey})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })
	return client
}

// gcsCountingTransport answers Google's token endpoint with a token and
// every storage request with status, counting the storage requests.
type gcsCountingTransport struct {
	status   int
	requests atomic.Int32
}

func (rt *gcsCountingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
	header := http.Header{"Content-Type": {"application/json"}}
	if req.URL.Host == "oauth2.googleapis.com" {
		return &http.Response{StatusCode: http.StatusOK, Header: header, Request: req,
			Body: io.NopCloser(strings.NewReader(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))}, nil
	}
	rt.requests.Add(1)
	return &http.Response{StatusCode: rt.status, Header: header, Request: req,
		Body: io.NopCloser(strings.NewReader(gcsErrorBody(rt.status, "backend unavailable")))}, nil
}

// openGCSStub opens the production gcp-gcs client with rt as its network.
// Not parallel-safe: it swaps the GCS transport and http.DefaultTransport.
func openGCSStub(t *testing.T, rt http.RoundTripper) storage.ProviderClient {
	t.Helper()
	t.Setenv("STORAGE_EMULATOR_HOST", "")
	useGCSTransport(t, rt)
	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(pk)
	require.NoError(t, err)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	client, err := openGCSBucket(context.Background(), "b", map[string]any{"gcpServiceAccountKey": serviceAccountJSON(t, keyPEM)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })
	return client
}

// The library never retries: a 503 on a fetch is one request, returned as a
// transient error for the worker to decide on. Against the production
// constructors of each provider. Not parallel: it swaps SDK constructors.
func TestGocloud_ServiceUnavailable_IsOneRequest(t *testing.T) {
	unavailable := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-ms-error-code", "ServerBusy")
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	t.Run("aws-s3", func(t *testing.T) {
		srv := newCountingServer(t, unavailable)
		client := openS3Stub(t, srv.Server)
		_, _, err := client.Fetch(context.Background(), "bucket", "k", 1<<20)
		requireClass(t, err, shared.ClassTransient)
		assert.Equal(t, int32(1), srv.requests.Load(), "the SDK did not retry")

		srv.requests.Store(0)
		requireClass(t, client.Upload(context.Background(), "bucket", "k", []byte("x"), "text/plain"), shared.ClassTransient)
		assert.Equal(t, int32(1), srv.requests.Load(), "the SDK did not retry the upload")
	})

	t.Run("azure-blob", func(t *testing.T) {
		srv := newCountingServer(t, unavailable)
		client := openAzureStub(t, srv.Server)
		_, _, err := client.Fetch(context.Background(), "docs", "k", 1<<20)
		requireClass(t, err, shared.ClassTransient)
		assert.Equal(t, int32(1), srv.requests.Load(), "the SDK did not retry")
	})

	t.Run("gcp-gcs", func(t *testing.T) {
		rt := &gcsCountingTransport{status: http.StatusServiceUnavailable}
		client := openGCSStub(t, rt)
		_, _, err := client.Fetch(context.Background(), "b", "k", 1<<20)
		requireClass(t, err, shared.ClassTransient)
		assert.Equal(t, int32(1), rt.requests.Load(), "the SDK did not retry")
	})
}

// A redirect is returned, never followed: the signed request would go to
// whichever host the response names. (gcp-gcs: TestOpenGCSBucket_NeverFollowsRedirects.)
// Not parallel: it swaps SDK constructors.
func TestGocloud_NeverFollowsRedirects(t *testing.T) {
	for name, open := range map[string]func(*testing.T, *httptest.Server) storage.ProviderClient{
		"aws-s3":     openS3Stub,
		"azure-blob": openAzureStub,
	} {
		t.Run(name, func(t *testing.T) {
			target := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "stolen") })
			srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", target.URL+"/steal")
				w.WriteHeader(http.StatusTemporaryRedirect)
			})
			client := open(t, srv.Server)
			_, _, err := client.Fetch(context.Background(), "docs", "k", 1<<20)
			require.Error(t, err, "a redirect is returned, not followed")
			assert.Equal(t, int32(1), srv.requests.Load())
			assert.Zero(t, target.requests.Load(), "the redirect target was never contacted")
		})
	}
}

// Every provider's HTTP client is bounded by ProviderHTTPTimeout and owns its
// transport. Not parallel: it swaps the GCS HTTP client constructor.
func TestGocloud_HTTPClientsAreBounded(t *testing.T) {
	s3Client, err := openS3Bucket(context.Background(), "b", map[string]any{"accessKey": "a", "secretKey": "s", "region": "us-east-1"})
	require.NoError(t, err)
	azClient, err := openAzureBucket(context.Background(), "docs", map[string]any{"azureAccountName": "acct01", "azureAccountKey": azureKey})
	require.NoError(t, err)
	for _, c := range []storage.ProviderClient{s3Client, azClient} {
		gc := c.(*gocloudStorageClient)
		require.NotNil(t, gc.httpClient)
		assert.Equal(t, storageHTTPTimeout, gc.httpClient.Timeout, gc.provider)
		tr, ok := gc.httpClient.Transport.(*http.Transport)
		require.True(t, ok, gc.provider)
		assert.Equal(t, shared.ProviderHTTPTimeout, tr.ResponseHeaderTimeout, gc.provider)
		assert.NotSame(t, http.DefaultTransport, gc.httpClient.Transport, gc.provider)
		assert.NoError(t, c.(io.Closer).Close())
	}

	var built *gcp.HTTPClient
	orig := newGCSHTTPClient
	t.Cleanup(func() { newGCSHTTPClient = orig })
	newGCSHTTPClient = func(rt http.RoundTripper, ts gcp.TokenSource) (*gcp.HTTPClient, error) {
		assert.NotSame(t, http.DefaultTransport, rt, "the GCS client has its own transport")
		built, err = orig(rt, ts)
		return built, err
	}
	gcsClient, err := openGCSBucket(context.Background(), "b", map[string]any{"gcpServiceAccountKey": serviceAccountJSON(t, "unused")})
	require.NoError(t, err)
	assert.Equal(t, storageHTTPTimeout, built.Timeout)
	require.NotNil(t, gcsClient.(*gocloudStorageClient).httpClient)
	assert.NoError(t, gcsClient.(io.Closer).Close())
}

// A GCS client that cannot be built is an upstream error. Not parallel: it
// swaps the GCS client constructor.
func TestOpenGCSBucket_ClientFailure_IsUpstream(t *testing.T) {
	sdkErr := errors.New("sdk refused")
	orig := newGCSClient
	t.Cleanup(func() { newGCSClient = orig })
	newGCSClient = func(context.Context, ...option.ClientOption) (*gcs.Client, error) { return nil, sdkErr }
	_, err := openGCSBucket(context.Background(), "b", map[string]any{"gcpServiceAccountKey": serviceAccountJSON(t, "unused")})
	require.ErrorIs(t, err, shared.ErrUpstream)
	assert.ErrorIs(t, err, sdkErr)
	assert.Contains(t, err.Error(), "gcp-gcs client")
}

// gocloud's drivers rewrite some characters of a key to "__0xNN__": such a
// key would read, write or delete another object (and a delete would
// "succeed" on a name that does not exist). Each is refused as invalid input
// before any request. Not parallel: it swaps SDK constructors.
func TestGocloud_KeysTheDriverWouldRewrite_AreRefusedBeforeAnyRequest(t *testing.T) {
	common := []string{"a/../b.pdf", "../b.pdf", "a/b../c", "a/.../b", "line\nbreak", "cr\rkey", "tab\tkey", "nul\x00key"}
	azureOnly := []string{`Invoice #123.pdf`, `100%.pdf`, `what?.pdf`, `say "hi".pdf`, `back\slash.pdf`, "del\x7f.pdf", "folder/"}
	ok := []string{"Invoice #123.pdf", "a/b/c.pdf", "..pdf", "a/..", "dir/..b", "über.pdf", "100%.pdf"}

	s3Srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	azSrv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	gcsRT := &gcsCountingTransport{status: http.StatusInternalServerError}
	clients := map[string]storage.ProviderClient{
		"aws-s3":     openS3Stub(t, s3Srv.Server),
		"azure-blob": openAzureStub(t, azSrv.Server),
		"gcp-gcs":    openGCSStub(t, gcsRT),
	}
	requests := func() int32 { return s3Srv.requests.Load() + azSrv.requests.Load() + gcsRT.requests.Load() }

	for provider, client := range clients {
		refused := common
		if provider == "azure-blob" {
			refused = append(append([]string(nil), common...), azureOnly...)
		}
		for _, key := range refused {
			ctx := context.Background()
			_, _, fetchErr := client.Fetch(ctx, "b", key, 1<<20)
			for op, err := range map[string]error{
				"fetch":  fetchErr,
				"upload": client.Upload(ctx, "b", key, []byte("x"), "text/plain"),
				"delete": client.Delete(ctx, "b", key),
			} {
				require.ErrorIs(t, err, shared.ErrValidation, "%s %s %q", provider, op, key)
				assert.NotErrorIs(t, err, shared.ErrUpstream)
				assert.Contains(t, err.Error(), "the gocloud "+provider+" driver cannot address", "%s %s %q", provider, op, key)
			}
		}
		assert.Zero(t, requests(), "%s: no refused key reached the provider", provider)
	}

	// Keys the drivers keep as they are reach the provider (here, a 500).
	for _, key := range ok {
		for provider, client := range clients {
			if provider == "azure-blob" && strings.ContainsAny(key, `#%`) {
				continue
			}
			before := requests()
			err := client.Delete(context.Background(), "b", key)
			require.ErrorIs(t, err, shared.ErrUpstream, "%s %q", provider, key)
			assert.Greater(t, requests(), before, "%s %q reached the provider", provider, key)
		}
	}
	// A trailing "/" is a valid S3 and GCS key.
	for _, provider := range []string{"aws-s3", "gcp-gcs"} {
		assert.Empty(t, unaddressableKey(provider, "folder/"), provider)
	}
}

func TestUnaddressableKey_NamesTheCharacter(t *testing.T) {
	t.Parallel()
	for key, want := range map[string]string{
		"a#b":   `contains '#'`,
		"a\nb":  `contains the control character '\n'`,
		"a/../": `contains "../"`,
		"dir/":  `ends with "/"`,
		"a\\b":  `contains '\\'`,
	} {
		assert.Equal(t, want, unaddressableKey("azure-blob", key), "%q", key)
	}
	assert.Empty(t, unaddressableKey("aws-s3", "a#b?c%d\"e\\f"))
	c := &gocloudStorageClient{}
	err := c.checkKey("x/../y")
	require.ErrorIs(t, err, shared.ErrValidation)
	assert.Contains(t, err.Error(), "the gocloud storage driver cannot address")
}

// A delete of an Azure blob that has snapshots (409 SnapshotsPresent) never
// deletes the snapshots: it fails permanently and says what to do. Not
// parallel: it swaps the Azure client constructor.
func TestAzureDelete_SnapshotsPresent_IsPermanentAndExplained(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method+" "+r.Header.Get("x-ms-delete-snapshots"))
		mu.Unlock()
		w.Header().Set("x-ms-error-code", "SnapshotsPresent")
		w.WriteHeader(http.StatusConflict)
	})
	client := openAzureStub(t, srv.Server)

	err := client.Delete(context.Background(), "docs", "k")
	require.ErrorIs(t, err, shared.ErrUpstream)
	requireClass(t, err, shared.ClassPermanent)
	assert.Contains(t, err.Error(), "the blob has snapshots")
	assert.Contains(t, err.Error(), "outside the connector")
	assert.Equal(t, []string{"DELETE "}, methods, "one delete, never asking to delete snapshots")
}

// A 50 MiB object must fit inside the whole-request bound at a slow but
// workable rate, while a silent server is still cut off after the provider
// timeout.
func TestStorageHTTPTimeout_FitsTheLargestObject(t *testing.T) {
	t.Parallel()
	const slowLink = 700_000 / 8 // bytes per second at 0.7 Mbit/s
	assert.GreaterOrEqual(t, storageHTTPTimeout.Seconds(), float64(shared.MaxObjectBytes)/slowLink)
	assert.Equal(t, shared.ProviderHTTPTimeout, storageTransport().ResponseHeaderTimeout)
}

// With STORAGE_EMULATOR_HOST set, the GCS SDK would send the tenant's
// requests, unauthenticated, to that host: no client is built and no request
// is made. Not parallel: it sets an environment variable.
func TestOpenGCSBucket_EmulatorHostSet_IsRefused(t *testing.T) {
	t.Setenv(storageEmulatorHostEnv, "evil.invalid:9000")
	called := false
	orig := newGCSClient
	t.Cleanup(func() { newGCSClient = orig })
	newGCSClient = func(ctx context.Context, opts ...option.ClientOption) (*gcs.Client, error) {
		called = true
		return orig(ctx, opts...)
	}

	_, err := openGCSBucket(context.Background(), "b", map[string]any{"gcpServiceAccountKey": serviceAccountJSON(t, "unused")})
	require.Error(t, err)
	assert.ErrorIs(t, err, errEmulatorHostSet)
	assert.ErrorIs(t, err, shared.ErrUpstream)
	assert.Contains(t, err.Error(), "evil.invalid:9000")
	assert.False(t, shared.IsTransient(err), "a worker misconfiguration is not retried")
	assert.False(t, called, "no GCS client is built")
}
