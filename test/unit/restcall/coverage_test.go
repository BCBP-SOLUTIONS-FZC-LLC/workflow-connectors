package restcall_test

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/restcall"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func TestRestCall_MissingAlias_IsValidationError(t *testing.T) {
	t.Parallel()
	_, err := restcall.New(aliasconfig.Config{}, nil, testInternalToken).Execute(deptCtx(), map[string]any{})
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrValidation)
}

func TestRestCall_JSONBody_IsSentWithContentType(t *testing.T) {
	t.Parallel()

	var gotContentType string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	conn := restCallConnector(t, srv, aliasconfig.Endpoint{Alias: "create", Method: "post", PathTemplate: "/items"})
	out, err := conn.Execute(deptCtx(), map[string]any{
		"endpointAlias": "create",
		"body":          map[string]any{"name": "x"},
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, out["status"])
	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, map[string]any{"name": "x"}, gotBody)
}

func TestRestCall_UnencodableBody_IsValidationError(t *testing.T) {
	t.Parallel()

	conn := restcall.New(aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{
		Alias: "create", Method: "POST", BaseURL: "http://127.0.0.1:1", PathTemplate: "/items",
	}}}, nil, testInternalToken)
	_, err := conn.Execute(deptCtx(), map[string]any{"endpointAlias": "create", "body": math.Inf(1)})
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrValidation)
	assert.Contains(t, err.Error(), "encoding body")
}

func TestRestCall_UnbuildableRequest_IsValidationError(t *testing.T) {
	t.Parallel()

	// The alias config is not validated here, so a method with a space
	// reaches http.NewRequest and is rejected there.
	conn := restcall.New(aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{
		Alias: "bad", Method: "BAD METHOD", BaseURL: "http://127.0.0.1:1", PathTemplate: "/items",
	}}}, nil, testInternalToken)
	_, err := conn.Execute(deptCtx(), map[string]any{"endpointAlias": "bad"})
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrValidation)
	assert.Contains(t, err.Error(), "building request")
}
