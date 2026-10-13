package gocloud

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func requireClass(t *testing.T, err error, want shared.Class) {
	t.Helper()
	require.Error(t, err)
	class, reason := shared.ClassOf(err)
	assert.Equal(t, want, class, "reason %q: %v", reason, err)
}

// A delete error is success only when the provider check says the object
// alone is missing; the check's own failure replaces the delete's error.
func TestGocloudStorageClient_Delete_UsesTheProviderCheck(t *testing.T) {
	t.Parallel()
	checkErr := apiErr{"NotFound"}
	for name, tc := range map[string]struct {
		check   func(context.Context, error) (bool, error)
		wantErr string
	}{
		"object missing": {check: func(context.Context, error) (bool, error) { return true, nil }},
		"not missing": {
			check:   func(context.Context, error) (bool, error) { return false, nil },
			wantErr: `gocloud delete "k": `,
		},
		"check failed": {
			check:   func(context.Context, error) (bool, error) { return false, checkErr },
			wantErr: `gocloud delete "k": api error NotFound`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := fakeClient(t, &fakeDriver{deleteErr: errNotFound})
			c.objectMissing = tc.check
			err := c.Delete(context.Background(), "", "k")
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, shared.ErrUpstream)
			assert.Contains(t, err.Error(), tc.wantErr)
			requireClass(t, err, shared.ClassPermanent)
		})
	}
}

func TestS3ObjectMissing(t *testing.T) {
	t.Parallel()
	bucketOK := func(context.Context) error { return nil }
	bucketGone := func(context.Context) error { return apiErr{"NotFound"} }
	ctx := context.Background()

	missing, err := s3ObjectMissing(bucketGone)(ctx, fmt.Errorf("blob: %w", apiErr{"NoSuchKey"}))
	assert.True(t, missing, "NoSuchKey is only ever about the key")
	assert.NoError(t, err)

	missing, err = s3ObjectMissing(bucketOK)(ctx, apiErr{"NotFound"})
	assert.True(t, missing, "a bare 404 with the bucket present is the object")
	assert.NoError(t, err)

	missing, err = s3ObjectMissing(bucketGone)(ctx, apiErr{"NotFound"})
	assert.False(t, missing)
	assert.Error(t, err, "a bare 404 with the bucket gone is the bucket")

	for _, other := range []error{apiErr{"NoSuchBucket"}, apiErr{"AccessDenied"}, errors.New("301 moved"), statusErr{404}} {
		missing, err = s3ObjectMissing(bucketOK)(ctx, other)
		assert.False(t, missing, other.Error())
		assert.NoError(t, err)
	}

	// A 403, from the delete or the bucket check, names the permissions a
	// delete needs and keeps the original error.
	missing, err = s3ObjectMissing(bucketOK)(ctx, fmt.Errorf("blob: %w", statusErr{403}))
	assert.False(t, missing)
	require.ErrorIs(t, err, statusErr{403})
	assert.Contains(t, err.Error(), "s3:ListBucket")

	missing, err = s3ObjectMissing(func(context.Context) error { return statusErr{403} })(ctx, apiErr{"NotFound"})
	assert.False(t, missing)
	require.ErrorIs(t, err, statusErr{403})
	assert.Contains(t, err.Error(), "aws-s3 bucket check (HeadBucket): access denied")
	assert.Contains(t, err.Error(), "s3:ListBucket")

	missing, err = s3ObjectMissing(bucketGone)(ctx, apiErr{"NotFound"})
	assert.False(t, missing)
	assert.Contains(t, err.Error(), "aws-s3 bucket check (HeadBucket): api error NotFound")
	assert.NotContains(t, err.Error(), "s3:ListBucket")
}

// statusErr is an error carrying an HTTP status, as the AWS SDK's do.
type statusErr struct{ status int }

func (e statusErr) Error() string       { return fmt.Sprintf("http %d", e.status) }
func (e statusErr) HTTPStatusCode() int { return e.status }

func TestAzureAndGCSObjectMissing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for err, want := range map[error]bool{
		&azcore.ResponseError{ErrorCode: "BlobNotFound", StatusCode: 404}:      true,
		&azcore.ResponseError{ErrorCode: "ContainerNotFound", StatusCode: 404}: false,
		&net.DNSError{Name: "nope.blob.core.windows.net", IsNotFound: true}:    false,
	} {
		got, checkErr := azureObjectMissing(ctx, err)
		assert.Equal(t, want, got, err.Error())
		assert.NoError(t, checkErr)
	}
	bucketOK := func(context.Context) error { return nil }
	bucketGone := func(context.Context) error {
		return &googleapi.Error{Code: 404, Message: "The specified bucket does not exist."}
	}
	// A 404 is the object only once the bucket is confirmed, whatever its
	// message says; anything else is never "already deleted" and lists nothing.
	for err, want := range map[error]bool{
		&googleapi.Error{Code: 404, Message: "No such object: b/k"}:                  true,
		&googleapi.Error{Code: 404, Message: "reworded by a proxy"}:                  true,
		&googleapi.Error{Code: 404, Message: "The specified bucket does not exist."}: true,
		&googleapi.Error{Code: 403, Message: "forbidden"}:                            false,
		errors.New("plain"): false,
	} {
		got, checkErr := gcsObjectMissing(bucketOK)(ctx, err)
		assert.Equal(t, want, got, err.Error())
		assert.NoError(t, checkErr)

		got, checkErr = gcsObjectMissing(bucketGone)(ctx, err)
		assert.False(t, got, err.Error())
		if want {
			require.Error(t, checkErr, err.Error())
			assert.Contains(t, checkErr.Error(), "gcp-gcs bucket check (objects.list)")
			requireClass(t, upstream("op", checkErr), shared.ClassPermanent)
		} else {
			assert.NoError(t, checkErr, err.Error())
		}
	}
	// A bucket check that fails keeps its own class. (Not run against the
	// stub API: the GCS client retries a 503 until the context ends.)
	_, checkErr := gcsObjectMissing(func(context.Context) error { return &googleapi.Error{Code: 503} })(ctx, &googleapi.Error{Code: 404})
	requireClass(t, upstream("op", checkErr), shared.ClassTransient)
	missing, err := codeNotFound(ctx, errors.New("plain"))
	assert.False(t, missing)
	assert.NoError(t, err)
}

// Azure and GCS responses carry their HTTP status in fields the shared
// classifier does not read; the adapter classifies them by status.
func TestClassify_ProviderStatuses(t *testing.T) {
	t.Parallel()
	for err, want := range map[error]shared.Class{
		&azcore.ResponseError{StatusCode: 503, ErrorCode: "ServerBusy"}:           shared.ClassTransient,
		&azcore.ResponseError{StatusCode: 500}:                                    shared.ClassTransient,
		&azcore.ResponseError{StatusCode: 429}:                                    shared.ClassTransient,
		&azcore.ResponseError{StatusCode: 408}:                                    shared.ClassTransient,
		&azcore.ResponseError{StatusCode: 403, ErrorCode: "AuthorizationFailure"}: shared.ClassPermanent,
		&azcore.ResponseError{StatusCode: 404, ErrorCode: "ContainerNotFound"}:    shared.ClassPermanent,
		&googleapi.Error{Code: 503}:                                               shared.ClassTransient,
		&googleapi.Error{Code: 429}:                                               shared.ClassTransient,
		&googleapi.Error{Code: 403}:                                               shared.ClassPermanent,
		&net.DNSError{Name: "nope.blob.core.windows.net.", IsNotFound: true}:      shared.ClassUnknown,
		&net.DNSError{Name: "s3.us-east-1.amazonaws.com", IsNotFound: true}:       shared.ClassTransient,
		&net.DNSError{Name: "nope.blob.core.windows.net", IsTemporary: true}:      shared.ClassTransient,
	} {
		requireClass(t, upstream("op", fmt.Errorf("blob: %w", err)), want)
	}
}

// --- aws-s3 against a stub S3 endpoint ---

type s3Stub struct {
	headObject, headBucket int
}

func (s *s3Stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodHead && len(parts) == 1:
		w.WriteHeader(s.headBucket)
	case r.Method == http.MethodHead:
		w.WriteHeader(s.headObject)
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

// Not parallel: it swaps the package-level S3 client constructor.
func TestS3Delete_AgainstStubEndpoint(t *testing.T) {
	stub := &s3Stub{}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
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

	for name, tc := range map[string]struct {
		headObject, headBucket int
		want                   shared.Class // ClassUnknown: success
	}{
		"object deleted":                 {http.StatusOK, http.StatusOK, shared.ClassUnknown},
		"object missing, bucket present": {http.StatusNotFound, http.StatusOK, shared.ClassUnknown},
		"bucket missing":                 {http.StatusNotFound, http.StatusNotFound, shared.ClassPermanent},
		"wrong region":                   {http.StatusMovedPermanently, http.StatusMovedPermanently, shared.ClassPermanent},
		"forbidden":                      {http.StatusForbidden, http.StatusOK, shared.ClassPermanent},
		"bucket check fails transiently": {http.StatusNotFound, http.StatusServiceUnavailable, shared.ClassTransient},
		"bucket check forbidden":         {http.StatusNotFound, http.StatusForbidden, shared.ClassPermanent},
	} {
		stub.headObject, stub.headBucket = tc.headObject, tc.headBucket
		err := client.Delete(context.Background(), "bucket", "k")
		if tc.want == shared.ClassUnknown {
			assert.NoError(t, err, name)
			continue
		}
		require.ErrorIs(t, err, shared.ErrUpstream, name)
		class, reason := shared.ClassOf(err)
		assert.Equal(t, tc.want, class, "%s: %s: %v", name, reason, err)
		if tc.headObject == http.StatusForbidden || tc.headBucket == http.StatusForbidden {
			assert.Contains(t, err.Error(), "s3:ListBucket", name)
		}
	}
}

// --- azure-blob against a stub Blob endpoint ---

// Not parallel: it swaps the package-level Azure client constructor.
func TestAzureDelete_AgainstStubEndpoint(t *testing.T) {
	var (
		mu     sync.Mutex
		status int
		code   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if code != "" {
			w.Header().Set("x-ms-error-code", code)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	orig := newAzureContainerClient
	t.Cleanup(func() { newAzureContainerClient = orig })
	newAzureContainerClient = func(_ string, cred *container.SharedKeyCredential, opts *container.ClientOptions) (*container.Client, error) {
		return orig(srv.URL+"/acct01/docs", cred, opts) // the production options: no retries
	}
	client, err := openAzureBucket(context.Background(), "docs", map[string]any{"azureAccountName": "acct01", "azureAccountKey": azureKey})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })

	for name, tc := range map[string]struct {
		status int
		code   string
		want   shared.Class
	}{
		"deleted":           {http.StatusAccepted, "", shared.ClassUnknown},
		"blob missing":      {http.StatusNotFound, "BlobNotFound", shared.ClassUnknown},
		"container missing": {http.StatusNotFound, "ContainerNotFound", shared.ClassPermanent},
		"forbidden":         {http.StatusForbidden, "AuthorizationFailure", shared.ClassPermanent},
		"server busy":       {http.StatusServiceUnavailable, "ServerBusy", shared.ClassTransient},
	} {
		mu.Lock()
		status, code = tc.status, tc.code
		mu.Unlock()
		err := client.Delete(context.Background(), "docs", "k")
		if tc.want == shared.ClassUnknown {
			assert.NoError(t, err, name)
			continue
		}
		require.ErrorIs(t, err, shared.ErrUpstream, name)
		class, reason := shared.ClassOf(err)
		assert.Equal(t, tc.want, class, "%s: %s: %v", name, reason, err)
	}
}

// --- gcp-gcs against a stub JSON API ---

type gcsStubTransport struct {
	mu     sync.Mutex
	status int
	body   string
	// listStatus and listBody answer objects.list (the bucket check); a zero
	// listStatus answers it with status and body.
	listStatus int
	listBody   string
}

func (rt *gcsStubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if req.Body != nil {
		_ = req.Body.Close()
	}
	header := http.Header{"Content-Type": {"application/json"}}
	if req.URL.Host == "oauth2.googleapis.com" {
		return &http.Response{StatusCode: http.StatusOK, Header: header, Request: req,
			Body: io.NopCloser(strings.NewReader(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))}, nil
	}
	status, body := rt.status, rt.body
	if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/o") && rt.listStatus != 0 {
		status, body = rt.listStatus, rt.listBody
	}
	return &http.Response{StatusCode: status, Header: header, Request: req, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// useGCSTransport sends this test's GCS traffic to rt: the storage requests
// (the adapter's own transport) and the token requests (tokenContext's
// client, on http.DefaultTransport).
func useGCSTransport(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	origDefault, origGCS := http.DefaultTransport, newGCSTransport
	http.DefaultTransport = rt
	newGCSTransport = func() http.RoundTripper { return rt }
	t.Cleanup(func() { http.DefaultTransport, newGCSTransport = origDefault, origGCS })
}

func gcsErrorBody(code int, message string) string {
	return fmt.Sprintf(`{"error":{"code":%d,"message":%q,"errors":[{"reason":"x","message":%q}]}}`, code, message, message)
}

// Not parallel: it swaps the GCS transport and http.DefaultTransport.
func TestGCSDelete_AgainstStubAPI(t *testing.T) {
	t.Setenv("STORAGE_EMULATOR_HOST", "")
	rt := &gcsStubTransport{}
	useGCSTransport(t, rt)

	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(pk)
	require.NoError(t, err)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	client, err := openGCSBucket(context.Background(), "b", map[string]any{"gcpServiceAccountKey": serviceAccountJSON(t, keyPEM)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })

	const emptyList = `{"kind":"storage#objects"}`
	notFound := gcsErrorBody(404, "Not Found") // wording the adapter does not recognise
	bucketMissing := gcsErrorBody(404, "The specified bucket does not exist.")
	for name, tc := range map[string]struct {
		status     int
		body       string
		listStatus int
		listBody   string
		want       shared.Class
		wantErr    string
	}{
		"deleted":                              {status: http.StatusNoContent, want: shared.ClassUnknown},
		"object missing":                       {status: http.StatusNotFound, body: gcsErrorBody(404, "No such object: b/k"), listStatus: http.StatusOK, listBody: emptyList, want: shared.ClassUnknown},
		"object missing, unrecognised message": {status: http.StatusNotFound, body: notFound, listStatus: http.StatusOK, listBody: emptyList, want: shared.ClassUnknown},
		"bucket missing":                       {status: http.StatusNotFound, body: bucketMissing, want: shared.ClassPermanent, wantErr: "gcp-gcs bucket check (objects.list)"},
		"bucket missing, unrecognised message": {
			status: http.StatusNotFound, body: notFound, listStatus: http.StatusNotFound, listBody: notFound,
			want: shared.ClassPermanent, wantErr: "gcp-gcs bucket check (objects.list)",
		},
		"forbidden": {status: http.StatusForbidden, body: gcsErrorBody(403, "sa does not have storage.objects.delete access"), want: shared.ClassPermanent},
	} {
		rt.mu.Lock()
		rt.status, rt.body, rt.listStatus, rt.listBody = tc.status, tc.body, tc.listStatus, tc.listBody
		rt.mu.Unlock()
		err := client.Delete(context.Background(), "b", "k")
		if tc.want == shared.ClassUnknown {
			assert.NoError(t, err, name)
			continue
		}
		require.ErrorIs(t, err, shared.ErrUpstream, name)
		class, reason := shared.ClassOf(err)
		assert.Equal(t, tc.want, class, "%s: %s: %v", name, reason, err)
		assert.Contains(t, err.Error(), tc.wantErr, name)
	}
}
