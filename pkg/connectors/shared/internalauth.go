package shared

import (
	"context"
	"fmt"
	"strings"
)

type departmentsCtxKey struct{}

// WithDepartments carries the acting user's "dept_uuid:role" pairs to
// rest-call and sql-query, which send them comma-joined as x-departments.
func WithDepartments(ctx context.Context, departments []string) context.Context {
	return context.WithValue(ctx, departmentsCtxKey{}, departments)
}

// DepartmentsFromContext returns the departments set by WithDepartments. ok
// is false unless there is at least one and every one is usable in the
// comma-joined x-departments header: non-empty, with no ',', '\r' or '\n'
// (which would add or forge a department, or break the header).
func DepartmentsFromContext(ctx context.Context) ([]string, bool) {
	v, _ := ctx.Value(departmentsCtxKey{}).([]string)
	return v, len(v) > 0 && validateDepartments(v) == nil
}

// DepartmentsHeaderValue returns the x-departments value for ctx, or
// ErrMissingInternalAuth (permanent: a worker wiring bug) when the context
// has no departments or one is empty or contains ',', '\r' or '\n'.
func DepartmentsHeaderValue(ctx context.Context) (string, error) {
	v, _ := ctx.Value(departmentsCtxKey{}).([]string)
	if len(v) == 0 {
		return "", ErrMissingInternalAuth
	}
	if err := validateDepartments(v); err != nil {
		return "", err
	}
	return strings.Join(v, ","), nil
}

func validateDepartments(departments []string) error {
	for i, d := range departments {
		if d == "" || strings.ContainsAny(d, ",\r\n") {
			return fmt.Errorf("%w: department %d is empty or contains ',', CR or LF", ErrMissingInternalAuth, i)
		}
	}
	return nil
}
