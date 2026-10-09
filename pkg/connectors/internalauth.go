package connectors

import (
	"context"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func WithDepartments(ctx context.Context, departments []string) context.Context {
	return shared.WithDepartments(ctx, departments)
}

func DepartmentsFromContext(ctx context.Context) ([]string, bool) {
	return shared.DepartmentsFromContext(ctx)
}

func WithTenant(ctx context.Context, tenantID string) context.Context {
	return shared.WithTenant(ctx, tenantID)
}

func TenantFromContext(ctx context.Context) (string, bool) {
	return shared.TenantFromContext(ctx)
}
