package connectors

import "context"

type departmentsCtxKey struct{}

func WithDepartments(ctx context.Context, departments []string) context.Context {
	return context.WithValue(ctx, departmentsCtxKey{}, departments)
}

func DepartmentsFromContext(ctx context.Context) ([]string, bool) {
	v, ok := ctx.Value(departmentsCtxKey{}).([]string)
	return v, ok
}
