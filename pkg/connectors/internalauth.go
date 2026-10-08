package connectors

import (
	"context"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
)

// WithDepartments and DepartmentsFromContext share shared's context key.
func WithDepartments(ctx context.Context, departments []string) context.Context {
	return shared.WithDepartments(ctx, departments)
}

func DepartmentsFromContext(ctx context.Context) ([]string, bool) {
	return shared.DepartmentsFromContext(ctx)
}
