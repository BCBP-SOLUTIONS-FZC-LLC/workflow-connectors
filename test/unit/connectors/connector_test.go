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

func TestWithDepartments_RoundTripsThroughDepartmentsFromContext(t *testing.T) {
	t.Parallel()

	ctx := connectors.WithDepartments(context.Background(), []string{"dept-1:reviewer"})
	departments, ok := connectors.DepartmentsFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, []string{"dept-1:reviewer"}, departments)
}

func TestCallerIdentity_RoundTripsThroughTheRootPackage(t *testing.T) {
	t.Parallel()

	ctx := connectors.WithLaneDepartment(connectors.WithActor(context.Background(), "principal-sub"), "lane")
	actor, ok := connectors.ActorFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "principal-sub", actor)
	lane, ok := connectors.LaneDepartmentFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "lane", lane)
}

func TestDepartmentsFromContext_NotSet_NotOK(t *testing.T) {
	t.Parallel()

	_, ok := connectors.DepartmentsFromContext(context.Background())
	assert.False(t, ok)
}
