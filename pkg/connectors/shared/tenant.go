package shared

import (
	"context"
	"errors"
)

// ErrMissingTenant means a call that needs the tenant (a registry-backed
// upload) ran without WithTenant: a worker wiring bug, never retryable.
var ErrMissingTenant = errors.New("connectors: missing tenant context (WithTenant not called)")

type tenantCtxKey struct{}

// WithTenant carries the job's tenant ID to connectors that scope state by
// tenant (the Drive document registry).
func WithTenant(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantCtxKey{}, tenantID)
}

func TenantFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(tenantCtxKey{}).(string)
	return v, ok && v != ""
}
