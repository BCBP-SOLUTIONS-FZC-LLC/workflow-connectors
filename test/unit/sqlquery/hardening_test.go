package sqlquery_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sqlquery"
)

func queryAlias(baseURL string) aliasconfig.Config {
	return aliasconfig.Config{SQLQuery: []aliasconfig.Query{{Alias: "q", BaseURL: baseURL, Path: "/query", QueryID: "q1"}}}
}

func TestSQLQuery_ErrorStatusReportedBeforeDecode(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("<html>down</html>"))
	}))
	defer srv.Close()

	conn := sqlquery.New(queryAlias(srv.URL), nil, "t")
	_, err := conn.Execute(shared.WithDepartments(context.Background(), []string{"d:r"}), map[string]any{"queryAlias": "q"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrUpstream))
	assert.Contains(t, err.Error(), "status 503")
}

func TestSQLQuery_Redirect_NotFollowed(t *testing.T) {
	t.Parallel()

	reachedExternal := false
	external := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reachedExternal = true }))
	defer external.Close()
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, external.URL, http.StatusTemporaryRedirect)
	}))
	defer internal.Close()

	conn := sqlquery.New(queryAlias(internal.URL), nil, "t")
	_, err := conn.Execute(shared.WithDepartments(context.Background(), []string{"d:r"}), map[string]any{"queryAlias": "q"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrUpstream))
	assert.False(t, reachedExternal)
}

func TestSQLQuery_UnknownAlias_IsValidationError(t *testing.T) {
	t.Parallel()

	conn := sqlquery.New(aliasconfig.Config{}, nil, "t")
	_, err := conn.Execute(shared.WithDepartments(context.Background(), []string{"d:r"}), map[string]any{"queryAlias": "nope"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
	assert.True(t, errors.Is(err, aliasconfig.ErrUnknownAlias))
}

// An unvalidated alias whose path does not start with "/" could move the
// call to another host: refused before anything is sent.
func TestSQLQuery_PathCannotChangeHost(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer evil.Close()

	cfg := aliasconfig.Config{SQLQuery: []aliasconfig.Query{{
		Alias: "q", BaseURL: "http://svc.internal", Path: "@" + strings.TrimPrefix(evil.URL, "http://") + "/steal", QueryID: "q1",
	}}}
	_, err := sqlquery.New(cfg, nil, "secret").Execute(shared.WithDepartments(context.Background(), []string{"d:r"}), map[string]any{"queryAlias": "q"})
	require.ErrorIs(t, err, shared.ErrValidation)
	assert.Zero(t, hits.Load())
}

// Integers beyond 2^53 in a result set keep every digit.
func TestSQLQuery_LargeIntegers_StayExact(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"resultSet":[{"id":9007199254740993,"amount":12345678901234567890}]}`))
	}))
	defer srv.Close()

	out, err := sqlquery.New(queryAlias(srv.URL), nil, "t").Execute(shared.WithDepartments(context.Background(), []string{"d:r"}), map[string]any{"queryAlias": "q"})
	require.NoError(t, err)
	rows := out["resultSet"].([]map[string]any)
	assert.Equal(t, json.Number("9007199254740993"), rows[0]["id"])
	assert.Equal(t, json.Number("12345678901234567890"), rows[0]["amount"])
}

func TestSQLQuery_UnusableDepartments_FailClosed(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	for _, depts := range [][]string{{}, {"d1:r,d2:admin"}, {"d1:r\n"}} {
		_, err := sqlquery.New(queryAlias(srv.URL), nil, "t").Execute(shared.WithDepartments(context.Background(), depts), map[string]any{"queryAlias": "q"})
		require.ErrorIs(t, err, shared.ErrMissingInternalAuth)
	}
	assert.Zero(t, hits.Load())
}
