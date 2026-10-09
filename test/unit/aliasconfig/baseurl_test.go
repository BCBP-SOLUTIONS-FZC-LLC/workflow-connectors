package aliasconfig_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
)

func TestValidate_BaseURLShape(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"tender-service.internal", "ftp://x", "file:///etc/passwd", "http://", "https://user:pw@x.internal", "://x"} {
		cfg := aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{Alias: "a", Method: "GET", BaseURL: bad, PathTemplate: "/a"}}}
		assert.Errorf(t, cfg.Validate(), "rest-call baseURL %q must be rejected", bad)

		q := aliasconfig.Config{SQLQuery: []aliasconfig.Query{{Alias: "q", BaseURL: bad, Path: "/q", QueryID: "q1"}}}
		assert.Errorf(t, q.Validate(), "sql-query baseURL %q must be rejected", bad)
	}

	ok := aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{Alias: "a", Method: "GET", BaseURL: "https://tender.internal:8443", PathTemplate: "/a"}}}
	assert.NoError(t, ok.Validate())
}
