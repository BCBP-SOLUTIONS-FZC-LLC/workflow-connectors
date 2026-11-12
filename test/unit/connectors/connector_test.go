package connectors_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
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

func TestNew_InternalTokenIsOptional(t *testing.T) {
	t.Parallel()

	all, err := connectors.New(connectors.Config{})
	require.NoError(t, err)
	require.Len(t, all, 4)
}

func TestNew_RestCallThroughTheFacade_TokenHeaderOnlyWhenConfigured(t *testing.T) {
	t.Parallel()

	for name, token := range map[string]string{"no token": "", "token": "secret-token"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var got http.Header
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Clone()
			}))
			defer srv.Close()

			all, err := connectors.New(connectors.Config{
				InternalToken: token,
				Aliases: aliasconfig.Config{RestCall: []aliasconfig.Endpoint{
					{Alias: "ping", Method: "GET", BaseURL: srv.URL, PathTemplate: "/ping"},
				}},
			})
			require.NoError(t, err)

			ctx := connectors.WithActor(connectors.WithTenant(context.Background(), "tenant-1"), "principal-sub")
			_, err = all[registry.TypeRestCall].Execute(ctx, map[string]any{"endpointAlias": "ping"})
			require.NoError(t, err)

			assert.Equal(t, "tenant-1", got.Get("x-tenant-id"))
			assert.Equal(t, "principal-sub", got.Get("x-user-id"))
			if token == "" {
				assert.Empty(t, got.Values("x-internal-token"))
			} else {
				assert.Equal(t, token, got.Get("x-internal-token"))
			}
		})
	}
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
