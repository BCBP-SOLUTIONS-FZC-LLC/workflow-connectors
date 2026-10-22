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

func WithActor(ctx context.Context, userID string) context.Context {
	return shared.WithActor(ctx, userID)
}

func ActorFromContext(ctx context.Context) (string, bool) {
	return shared.ActorFromContext(ctx)
}

func WithLaneDepartment(ctx context.Context, departmentID string) context.Context {
	return shared.WithLaneDepartment(ctx, departmentID)
}

func LaneDepartmentFromContext(ctx context.Context) (string, bool) {
	return shared.LaneDepartmentFromContext(ctx)
}
