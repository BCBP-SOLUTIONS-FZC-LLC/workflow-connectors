package aliasconfig_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
)

func TestValidate_MissingFields(t *testing.T) {
	t.Parallel()

	validQuery := aliasconfig.Query{Alias: "q", BaseURL: "https://svc.internal", Path: "/q", QueryID: "q"}
	cases := map[string]struct {
		cfg  aliasconfig.Config
		want string
	}{
		"restCall alias": {
			cfg:  aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{Method: "GET", BaseURL: "https://svc.internal", PathTemplate: "/x"}}},
			want: "restCall entry missing alias",
		},
		"sqlQuery alias": {
			cfg:  aliasconfig.Config{SQLQuery: []aliasconfig.Query{{BaseURL: "https://svc.internal", Path: "/q", QueryID: "q"}}},
			want: "sqlQuery entry missing alias",
		},
		"sqlQuery path": {
			cfg: aliasconfig.Config{SQLQuery: []aliasconfig.Query{func() aliasconfig.Query {
				q := validQuery
				q.Path = ""
				return q
			}()}},
			want: "path must start with",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := tc.cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	require.NoError(t, aliasconfig.Config{SQLQuery: []aliasconfig.Query{validQuery}}.Validate())
}

// A ";" in a pathTemplate's query would make Go's query parser drop the pair,
// so a key fixed after it could be sent again by the caller: rejected at load.
func TestValidate_SemicolonInPathTemplateQuery(t *testing.T) {
	t.Parallel()

	endpoint := func(path string) aliasconfig.Config {
		return aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{
			Alias: "a", Method: "GET", BaseURL: "https://svc.internal", PathTemplate: path,
		}}}
	}
	for _, path := range []string{"/x?a=1;b=2", "/x?a={v};page=1", "/x?;"} {
		err := endpoint(path).Validate()
		require.Error(t, err, path)
		assert.Contains(t, err.Error(), `must not contain ";"`, path)
	}
	for _, path := range []string{"/x;matrix=1", "/x;m=1?a=1&b=%3B", "/x?a=1&b=2"} {
		require.NoError(t, endpoint(path).Validate(), path)
	}
}

func TestValidate_MalformedPathTemplateQuery(t *testing.T) {
	t.Parallel()

	endpoint := func(path string) aliasconfig.Config {
		return aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{
			Alias: "a", Method: "GET", BaseURL: "https://svc.internal", PathTemplate: path,
		}}}
	}
	// Refused at load: before, these failed only on a call passing queryParams.
	for _, path := range []string{"/x?a=%zz", "/x?a=1&b=%", "/x?%g1=v"} {
		err := endpoint(path).Validate()
		require.Error(t, err, path)
		assert.Contains(t, err.Error(), "not a valid query string", path)
	}
	for _, path := range []string{"/x?a={v}&page=1", "/x?flag", "/x?a=%20b", "/x?"} {
		require.NoError(t, endpoint(path).Validate(), path)
	}
}
