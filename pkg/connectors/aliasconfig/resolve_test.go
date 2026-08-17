package aliasconfig_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/aliasconfig"
)

func testConfig() aliasconfig.Config {
	return aliasconfig.Config{
		RestCall: []aliasconfig.Endpoint{
			{Alias: "known-endpoint", Method: "GET", BaseURL: "http://x", PathTemplate: "/a"},
		},
		SQLQuery: []aliasconfig.Query{
			{Alias: "known-query", BaseURL: "http://x", Path: "/q", QueryID: "q1"},
		},
	}
}

func TestResolveEndpoint(t *testing.T) {
	t.Parallel()

	cfg := testConfig()

	ep, err := aliasconfig.ResolveEndpoint(cfg, "known-endpoint")
	require.NoError(t, err)
	assert.Equal(t, "GET", ep.Method)

	_, err = aliasconfig.ResolveEndpoint(cfg, "missing")
	require.Error(t, err)
	assert.True(t, errors.Is(err, aliasconfig.ErrUnknownAlias))
}

func TestResolveQuery(t *testing.T) {
	t.Parallel()

	cfg := testConfig()

	q, err := aliasconfig.ResolveQuery(cfg, "known-query")
	require.NoError(t, err)
	assert.Equal(t, "q1", q.QueryID)

	_, err = aliasconfig.ResolveQuery(cfg, "missing")
	require.Error(t, err)
	assert.True(t, errors.Is(err, aliasconfig.ErrUnknownAlias))
}
