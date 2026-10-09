package aliasconfig_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aliases.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoad_Valid(t *testing.T) {
	t.Parallel()

	path := writeTemp(t, `
version: 1
restCall:
  - alias: tender-get-application
    method: GET
    baseURL: http://tender-service.internal
    pathTemplate: /api/v1/applications/{applicationId}
    timeout: 5s
sqlQuery:
  - alias: get-active-tenants
    baseURL: http://tender-service.internal
    path: /internal/queries/execute
    queryId: get-active-tenants
    paramCount: 1
    timeout: 5s
`)

	cfg, err := aliasconfig.Load(path)
	require.NoError(t, err)
	assert.Len(t, cfg.RestCall, 1)
	assert.Len(t, cfg.SQLQuery, 1)
	assert.Equal(t, "tender-get-application", cfg.RestCall[0].Alias)
}

func TestLoad_MissingFile(t *testing.T) {
	t.Parallel()

	_, err := aliasconfig.Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
}

func TestLoad_MalformedYAML(t *testing.T) {
	t.Parallel()

	path := writeTemp(t, "restCall: [not: valid: yaml")
	_, err := aliasconfig.Load(path)
	require.Error(t, err)
}

func TestLoad_ValidationRules(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"duplicate rest-call alias": `
restCall:
  - alias: dup
    method: GET
    baseURL: http://x
    pathTemplate: /a
  - alias: dup
    method: GET
    baseURL: http://x
    pathTemplate: /b
`,
		"invalid method": `
restCall:
  - alias: a
    method: FROBNICATE
    baseURL: http://x
    pathTemplate: /a
`,
		"missing baseURL": `
restCall:
  - alias: a
    method: GET
    pathTemplate: /a
`,
		"missing pathTemplate": `
restCall:
  - alias: a
    method: GET
    baseURL: http://x
`,
		"duplicate sql-query alias": `
sqlQuery:
  - alias: dup
    baseURL: http://x
    path: /q
    queryId: q1
  - alias: dup
    baseURL: http://x
    path: /q
    queryId: q2
`,
		"missing queryId": `
sqlQuery:
  - alias: a
    baseURL: http://x
    path: /q
`,
		"negative paramCount": `
sqlQuery:
  - alias: a
    baseURL: http://x
    path: /q
    queryId: q1
    paramCount: -1
`,
	}

	for name, yamlContent := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := writeTemp(t, yamlContent)
			_, err := aliasconfig.Load(path)
			assert.Error(t, err)
		})
	}
}
