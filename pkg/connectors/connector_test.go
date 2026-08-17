package connectors_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

func validConfig() connectors.Config {
	return connectors.Config{InternalToken: "test-token"}
}

func TestNew_SixTypes(t *testing.T) {
	t.Parallel()

	all, err := connectors.New(validConfig())
	require.NoError(t, err)
	require.Len(t, all, 6)

	for _, typ := range []string{
		registry.TypeStorage,
		registry.TypeSendEmail,
		registry.TypeDocumentExtract,
		registry.TypeRestCall,
		registry.TypeSQLQuery,
		registry.TypeChatNotify,
	} {
		c, ok := all[typ]
		require.True(t, ok, "missing connector for %q", typ)
		assert.Equal(t, typ, c.Type())
	}
}

func TestNew_RequiresInternalToken(t *testing.T) {
	t.Parallel()

	_, err := connectors.New(connectors.Config{})
	require.Error(t, err)
}
