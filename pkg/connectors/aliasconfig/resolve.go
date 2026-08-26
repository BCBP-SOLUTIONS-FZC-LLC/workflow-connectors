package aliasconfig

import (
	"errors"
	"fmt"
)

var ErrUnknownAlias = errors.New("aliasconfig: unknown alias")

func ResolveEndpoint(cfg Config, alias string) (Endpoint, error) {
	for _, e := range cfg.RestCall {
		if e.Alias == alias {
			return e, nil
		}
	}
	return Endpoint{}, fmt.Errorf("%w: %q", ErrUnknownAlias, alias)
}

func ResolveQuery(cfg Config, alias string) (Query, error) {
	for _, q := range cfg.SQLQuery {
		if q.Alias == alias {
			return q, nil
		}
	}
	return Query{}, fmt.Errorf("%w: %q", ErrUnknownAlias, alias)
}
