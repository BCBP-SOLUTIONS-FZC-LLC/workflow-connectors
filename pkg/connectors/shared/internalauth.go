package shared

import (
	"context"
	"fmt"
	"strings"
)

type (
	departmentsCtxKey    struct{}
	actorCtxKey          struct{}
	laneDepartmentCtxKey struct{}
)

func WithDepartments(ctx context.Context, departments []string) context.Context {
	return context.WithValue(ctx, departmentsCtxKey{}, departments)
}

func DepartmentsFromContext(ctx context.Context) ([]string, bool) {
	v, _ := ctx.Value(departmentsCtxKey{}).([]string)
	return v, len(v) > 0 && validateDepartments(v) == nil
}

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

func WithActor(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, actorCtxKey{}, userID)
}

func ActorFromContext(ctx context.Context) (string, bool) {
	v, _ := ctx.Value(actorCtxKey{}).(string)
	return v, v != ""
}

func WithLaneDepartment(ctx context.Context, departmentID string) context.Context {
	return context.WithValue(ctx, laneDepartmentCtxKey{}, departmentID)
}

func LaneDepartmentFromContext(ctx context.Context) (string, bool) {
	v, _ := ctx.Value(laneDepartmentCtxKey{}).(string)
	return v, v != ""
}

type CallerHeaders struct {
	TenantID         string
	UserID           string
	LaneDepartmentID string
}

func CallerHeadersFromContext(ctx context.Context) (CallerHeaders, error) {
	tenant, _ := TenantFromContext(ctx)
	actor, _ := ActorFromContext(ctx)
	lane, _ := LaneDepartmentFromContext(ctx)
	if tenant == "" || actor == "" {
		return CallerHeaders{}, ErrMissingInternalAuth
	}
	for name, v := range map[string]string{"tenant": tenant, "actor": actor, "lane department": lane} {
		if strings.ContainsAny(v, "\r\n") {
			return CallerHeaders{}, fmt.Errorf("%w: the %s contains CR or LF", ErrMissingInternalAuth, name)
		}
	}
	return CallerHeaders{TenantID: tenant, UserID: actor, LaneDepartmentID: lane}, nil
}

func (h CallerHeaders) Apply(header interface{ Set(key, value string) }) {
	header.Set(TenantIDHeader, h.TenantID)
	header.Set(UserIDHeader, h.UserID)
	if h.LaneDepartmentID != "" {
		header.Set(LaneDepartmentHeader, h.LaneDepartmentID)
	}
}
