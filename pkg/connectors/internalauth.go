package connectors

import "context"

type departmentsCtxKey struct{}

// WithDepartments attaches the caller's forwarded x-departments value (a
// list of dept_uuid:role pairs) to ctx — rest-call and sql-query read it
// back via DepartmentsFromContext and fail closed if it's absent, since the
// LLD makes it mandatory, not optional, alongside x-internal-token.
func WithDepartments(ctx context.Context, departments []string) context.Context {
	return context.WithValue(ctx, departmentsCtxKey{}, departments)
}

// DepartmentsFromContext returns the departments attached by WithDepartments.
// ok is false if the caller never attached any — a connector that requires
// this must treat that as a hard failure, not an empty-but-valid value.
func DepartmentsFromContext(ctx context.Context) ([]string, bool) {
	v, ok := ctx.Value(departmentsCtxKey{}).([]string)
	return v, ok
}
