package connectors

import (
	"context"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
)

// WithDepartments and DepartmentsFromContext forward to shared so that a
// context built via the long-standing connectors.WithDepartments (as
// execution_service's cmd/connector-worker does) and one read back via
// shared.DepartmentsFromContext (as every connector-type subpackage does)
// resolve through the exact same context key.
func WithDepartments(ctx context.Context, departments []string) context.Context {
	return shared.WithDepartments(ctx, departments)
}

func DepartmentsFromContext(ctx context.Context) ([]string, bool) {
	return shared.DepartmentsFromContext(ctx)
}
