package aliasconfig

import (
	"errors"
	"fmt"
)

// ErrUnknownAlias is returned by ResolveEndpoint/ResolveQuery when alias
// isn't present in cfg — a dangling alias is a real runtime failure, never a
// silent no-op.
var ErrUnknownAlias = errors.New("aliasconfig: unknown alias")

// ResolveEndpoint looks up a rest-call endpointAlias. Exported separately
// from the rest-call connector itself so cmd/connector-worker's dispatcher
// can resolve an alias's Method up front, independent of Execute() doing the
// same lookup internally to make the call — the dispatcher needs Method
// before dispatch to decide registry.IsIdempotentMethod-gated retryability.
func ResolveEndpoint(cfg Config, alias string) (Endpoint, error) {
	for _, e := range cfg.RestCall {
		if e.Alias == alias {
			return e, nil
		}
	}
	return Endpoint{}, fmt.Errorf("%w: %q", ErrUnknownAlias, alias)
}

// ResolveQuery looks up a sql-query queryAlias.
func ResolveQuery(cfg Config, alias string) (Query, error) {
	for _, q := range cfg.SQLQuery {
		if q.Alias == alias {
			return q, nil
		}
	}
	return Query{}, fmt.Errorf("%w: %q", ErrUnknownAlias, alias)
}
