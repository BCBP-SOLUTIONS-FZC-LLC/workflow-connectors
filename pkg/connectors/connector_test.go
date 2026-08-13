package connectors_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

func TestNew_SixTypes(t *testing.T) {
	t.Parallel()

	all := connectors.New()
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

		_, err := c.Execute(context.Background(), map[string]any{})
		assert.True(t, errors.Is(err, connectors.ErrNotImplemented))
	}
}
