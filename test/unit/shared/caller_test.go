package shared_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func TestCallerHeaders_NeedTenantAndActorAndRefuseHeaderBreakingValues(t *testing.T) {
	t.Parallel()

	bad := map[string]context.Context{
		"no tenant":    shared.WithActor(context.Background(), "principal"),
		"no actor":     shared.WithTenant(context.Background(), "tenant"),
		"tenant crlf":  shared.WithActor(shared.WithTenant(context.Background(), "t\r\nx-internal-token: forged"), "principal"),
		"actor lf":     shared.WithActor(shared.WithTenant(context.Background(), "tenant"), "p\nx-other: 1"),
		"lane cr":      shared.WithLaneDepartment(shared.WithActor(shared.WithTenant(context.Background(), "tenant"), "principal"), "d\r"),
		"blank actor":  shared.WithActor(shared.WithTenant(context.Background(), "tenant"), ""),
		"blank tenant": shared.WithActor(shared.WithTenant(context.Background(), ""), "principal"),
	}
	for name, ctx := range bad {
		_, err := shared.CallerHeadersFromContext(ctx)
		require.ErrorIs(t, err, shared.ErrMissingInternalAuth, name)
		assert.True(t, shared.IsPermanent(err), name)
	}

	ctx := shared.WithLaneDepartment(shared.WithActor(shared.WithTenant(context.Background(), "tenant"), "principal"), "dept")
	got, err := shared.CallerHeadersFromContext(ctx)
	require.NoError(t, err)
	assert.Equal(t, shared.CallerHeaders{TenantID: "tenant", UserID: "principal", LaneDepartmentID: "dept"}, got)

	got, err = shared.CallerHeadersFromContext(shared.WithActor(shared.WithTenant(context.Background(), "tenant"), "principal"))
	require.NoError(t, err)
	assert.Empty(t, got.LaneDepartmentID, "the lane department is optional context")
}
