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

// A misspelt or unknown key is an error, never a silently empty field.
func TestLoad_UnknownKeyIsRejected(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"misspelt entry key": `
restCall:
  - alias: a
    method: GET
    baseURL: http://svc.internal
    pathTemplte: /a
`,
		"unknown top-level key": `
version: 1
restCalls: []
`,
	} {
		_, err := aliasconfig.Load(writeTemp(t, content))
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "not found in type", name)
	}
}

func TestLoad_SingleDocumentOnly(t *testing.T) {
	t.Parallel()

	_, err := aliasconfig.Load(writeTemp(t, "version: 1\n---\nversion: 2\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "single YAML document")
}

// A leading or trailing "---" (an empty document, optionally with comments,
// or an explicit null) is common in generated or concatenated YAML; it must
// not stop the worker from starting.
func TestLoad_EmptyDocumentsAreIgnored(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"trailing ---":              "version: 1\n---\n",
		"trailing --- no newline":   "version: 1\n---",
		"trailing --- with comment": "version: 1\n--- # end\n# nothing more\n",
		"several empty documents":   "version: 1\n---\n---\n...\n",
		"trailing explicit ~":       "version: 1\n--- ~\n",
		"trailing explicit null":    "version: 1\n--- null\n",
		"trailing null document":    "version: 1\n---\nnull\n",
		"leading empty document":    "---\n---\nversion: 1\n",
		"leading null document":     "--- ~\n---\nversion: 1\n",
		"empty documents around":    "---\n# header\n---\nversion: 1\n---\nNULL\n...\n",
	} {
		cfg, err := aliasconfig.Load(writeTemp(t, content))
		require.NoError(t, err, name)
		assert.Equal(t, 1, cfg.Version, name)
	}
}

func TestLoad_SecondDocumentWithContentIsRejected(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"mapping":         "version: 1\n---\nrestCall: []\n",
		"scalar":          "version: 1\n---\nhello\n",
		"string null":     "version: 1\n---\n'null'\n",
		"after empty doc": "version: 1\n---\n---\nversion: 2\n",
		"leading content": "hello\n---\nversion: 1\n",
	} {
		_, err := aliasconfig.Load(writeTemp(t, content))
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "single YAML document", name)
	}
}

// A malformed document fails the load wherever it is, with the parser's
// error, even after a valid one.
func TestLoad_MalformedLaterDocumentIsRejected(t *testing.T) {
	t.Parallel()

	_, err := aliasconfig.Load(writeTemp(t, "version: 1\n---\n[unclosed\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aliasconfig: parsing")
}

// An unknown key is still rejected when the content document comes after
// empty ones (it is decoded strictly).
func TestLoad_UnknownKeyAfterEmptyDocumentIsRejected(t *testing.T) {
	t.Parallel()

	_, err := aliasconfig.Load(writeTemp(t, "---\n---\nversoin: 1\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "versoin")
}

func TestLoad_EmptyFileIsEmptyConfig(t *testing.T) {
	t.Parallel()

	for _, content := range []string{"", "# only a comment\n", "---\n", "--- ~\n---\n", "null\n"} {
		cfg, err := aliasconfig.Load(writeTemp(t, content))
		require.NoError(t, err)
		assert.Equal(t, aliasconfig.Config{}, cfg)
	}
}
