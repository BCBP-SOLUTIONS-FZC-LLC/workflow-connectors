package restcall_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/restcall"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func aliasFor(baseURL, pathTemplate string) aliasconfig.Config {
	return aliasconfig.Config{RestCall: []aliasconfig.Endpoint{
		{Alias: "a", Method: http.MethodGet, BaseURL: baseURL, PathTemplate: pathTemplate},
	}}
}

func deptCtx() context.Context {
	return shared.WithDepartments(context.Background(), []string{"d:approver"})
}

func TestRestCall_Redirect_NotFollowed_TokenNeverLeaves(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var leakedToken, leakedDepts string
	external := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		leakedToken = r.Header.Get(shared.InternalTokenHeader)
		leakedDepts = r.Header.Get(shared.DepartmentsHeader)
	}))
	defer external.Close()
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, external.URL+"/steal", http.StatusFound)
	}))
	defer internal.Close()

	conn := restcall.New(aliasFor(internal.URL, "/x"), &http.Client{}, "secret-token")
	out, err := conn.Execute(deptCtx(), map[string]any{"endpointAlias": "a"})

	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrUpstream))
	assert.Equal(t, http.StatusFound, out["status"])
	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, leakedToken, "x-internal-token must never reach another host")
	assert.Empty(t, leakedDepts)
}

func TestRestCall_NumericPathAndQueryParams(t *testing.T) {
	t.Parallel()

	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
	}))
	defer srv.Close()

	conn := restcall.New(aliasFor(srv.URL, "/applications/{id}"), nil, "t")
	_, err := conn.Execute(deptCtx(), map[string]any{
		"endpointAlias": "a",
		"pathParams":    map[string]any{"id": float64(1234567)},
		"queryParams":   map[string]any{"page": float64(3)},
	})
	require.NoError(t, err)
	assert.Equal(t, "/applications/1234567", gotPath)
	assert.Equal(t, "page=3", gotQuery)
}

func TestRestCall_UnknownAlias_IsValidationError(t *testing.T) {
	t.Parallel()

	conn := restcall.New(aliasconfig.Config{}, nil, "t")
	_, err := conn.Execute(deptCtx(), map[string]any{"endpointAlias": "missing"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
	assert.True(t, errors.Is(err, aliasconfig.ErrUnknownAlias))
}

func TestRestCall_TransportError_IsUpstream(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	conn := restcall.New(aliasFor(url, "/x"), nil, "t")
	_, err := conn.Execute(deptCtx(), map[string]any{"endpointAlias": "a"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrUpstream))
}

func TestRestCall_OversizedResponse_IsRejected(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, shared.MaxResponseBytes+1))
	}))
	defer srv.Close()

	conn := restcall.New(aliasFor(srv.URL, "/x"), nil, "t")
	_, err := conn.Execute(deptCtx(), map[string]any{"endpointAlias": "a"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestRestCall_SetCookieNotReturned(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc"})
	}))
	defer srv.Close()

	conn := restcall.New(aliasFor(srv.URL, "/x"), nil, "t")
	out, err := conn.Execute(deptCtx(), map[string]any{"endpointAlias": "a"})
	require.NoError(t, err)
	assert.NotContains(t, out["headers"], "Set-Cookie")
}

// An alias timeout longer than the 30-second default is honoured: the
// default client carries no client-wide timeout that would cut it short.
func TestRestCall_AliasTimeoutLongerThanDefault_IsHonoured(t *testing.T) {
	t.Parallel()
	client := shared.InternalHTTPClient(nil)
	assert.Zero(t, client.Timeout, "no client-wide cap")
	assert.Equal(t, 2*time.Minute, shared.CallTimeout(2*time.Minute))
	assert.Equal(t, shared.DefaultHTTPTimeout, shared.CallTimeout(0), "an alias without a timeout gets the default deadline")
}
