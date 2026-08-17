package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

// sqlQuery is deliberately structured like restCall, not a direct database
// connection: an owning service's own internal query-execution endpoint
// resolves queryId against its own pre-registered, read-only statement and
// performs the actual parameter binding — this connector never sees or
// constructs SQL text (LLD §6.4.5), and x-internal-token/x-departments are
// real HTTP headers rather than a meaningless attempt to attach them to a
// raw DB connection.
type sqlQuery struct {
	cfg Config
}

func newSQLQuery(cfg Config) Connector { return sqlQuery{cfg: cfg} }

func (sqlQuery) Type() string { return registry.TypeSQLQuery }

type sqlQueryRequest struct {
	QueryID string `json:"queryId"`
	Params  []any  `json:"params"`
}

type sqlQueryResponse struct {
	ResultSet []map[string]any `json:"resultSet"`
}

func (s sqlQuery) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	alias, _ := input["queryAlias"].(string)
	if alias == "" {
		return nil, fmt.Errorf("%w: queryAlias is required", ErrValidation)
	}
	q, err := aliasconfig.ResolveQuery(s.cfg.Aliases, alias)
	if err != nil {
		return nil, err
	}

	params, _ := input["params"].([]any)
	if q.ParamCount > 0 && len(params) != q.ParamCount {
		return nil, fmt.Errorf("%w: queryAlias %q expects %d params, got %d", ErrValidation, alias, q.ParamCount, len(params))
	}

	departments, ok := DepartmentsFromContext(ctx)
	if !ok {
		return nil, ErrMissingInternalAuth
	}

	encoded, err := json.Marshal(sqlQueryRequest{QueryID: q.QueryID, Params: params})
	if err != nil {
		return nil, fmt.Errorf("%w: encoding request: %s", ErrValidation, err)
	}

	reqCtx := ctx
	if q.Timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, q.Timeout)
		defer cancel()
	}

	fullURL := strings.TrimRight(q.BaseURL, "/") + q.Path
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, fullURL, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("%w: building request: %s", ErrValidation, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(internalTokenHeader, s.cfg.InternalToken)
	req.Header.Set(departmentsHeader, strings.Join(departments, ","))

	resp, err := s.cfg.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("connectors: sql-query %q: %w", alias, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var parsed sqlQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("%w: sql-query %q: decoding response: %s", ErrUpstream, alias, err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w: sql-query %q: status %d", ErrUpstream, alias, resp.StatusCode)
	}

	return map[string]any{"resultSet": parsed.ResultSet}, nil
}
