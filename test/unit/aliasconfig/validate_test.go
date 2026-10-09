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
