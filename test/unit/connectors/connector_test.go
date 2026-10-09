package connectors_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

func validConfig() connectors.Config {
	return connectors.Config{InternalToken: "test-token"}
}

func TestNew_FourTypes(t *testing.T) {
	t.Parallel()

	all, err := connectors.New(validConfig())
	require.NoError(t, err)
	require.Len(t, all, 4)

	for _, typ := range []string{
		registry.TypeStorage,
		registry.TypeSendEmail,
		registry.TypeRestCall,
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

// execution_service's cmd/connector-worker calls connectors.WithDepartments
// and reads it back via connectors.DepartmentsFromContext directly (not
// through pkg/connectors/shared, which it doesn't import) — root must keep
// forwarding to the same context key shared's own callers use.
func TestWithDepartments_RoundTripsThroughDepartmentsFromContext(t *testing.T) {
	t.Parallel()

	ctx := connectors.WithDepartments(context.Background(), []string{"dept-1:reviewer"})
	departments, ok := connectors.DepartmentsFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, []string{"dept-1:reviewer"}, departments)
}

func TestDepartmentsFromContext_NotSet_NotOK(t *testing.T) {
	t.Parallel()

	_, ok := connectors.DepartmentsFromContext(context.Background())
	assert.False(t, ok)
}
