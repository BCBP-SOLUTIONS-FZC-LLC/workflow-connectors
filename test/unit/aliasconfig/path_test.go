package aliasconfig_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
)

// A path not starting with "/" is appended straight after the host, so
// "@evil.example/x" would send the call to evil.example.
func TestValidate_PathMustStartWithSlash(t *testing.T) {
	t.Parallel()

	for _, p := range []string{"", "@evil.example/x", "x", ".evil.example/x", ":8080/x", "?a=1"} {
		rest := aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{Alias: "a", Method: "GET", BaseURL: "http://svc", PathTemplate: p}}}
		err := rest.Validate()
		require.Error(t, err, "pathTemplate %q", p)
		assert.Contains(t, err.Error(), `pathTemplate must start with "/"`)

		sql := aliasconfig.Config{SQLQuery: []aliasconfig.Query{{Alias: "q", BaseURL: "http://svc", Path: p, QueryID: "q"}}}
		err = sql.Validate()
		require.Error(t, err, "path %q", p)
		assert.Contains(t, err.Error(), `path must start with "/"`)
	}

	// A template with a fixed query string stays valid.
	ok := aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{Alias: "a", Method: "GET", BaseURL: "http://svc", PathTemplate: "/items/{id}?view=full"}}}
	require.NoError(t, ok.Validate())
}
