package sqlquery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

// Connector is deliberately structured like restcall.Connector, not a direct
// database connection: an owning service's own internal query-execution
// endpoint resolves queryId against its own pre-registered, read-only
// statement and performs the actual parameter binding.
type Connector struct {
	aliases       aliasconfig.Config
	httpClient    *http.Client
	internalToken string
}

func New(aliases aliasconfig.Config, httpClient *http.Client, internalToken string) Connector {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return Connector{aliases: aliases, httpClient: httpClient, internalToken: internalToken}
}

func (Connector) Type() string { return registry.TypeSQLQuery }

type sqlQueryRequest struct {
	QueryID string `json:"queryId"`
	Params  []any  `json:"params"`
}

type sqlQueryResponse struct {
	ResultSet []map[string]any `json:"resultSet"`
}

func (s Connector) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	alias, _ := input["queryAlias"].(string)
	if alias == "" {
		return nil, fmt.Errorf("%w: queryAlias is required", shared.ErrValidation)
	}
	q, err := aliasconfig.ResolveQuery(s.aliases, alias)
	if err != nil {
		return nil, err
	}

	params, _ := input["params"].([]any)
	if q.ParamCount > 0 && len(params) != q.ParamCount {
		return nil, fmt.Errorf("%w: queryAlias %q expects %d params, got %d", shared.ErrValidation, alias, q.ParamCount, len(params))
	}

	departments, ok := shared.DepartmentsFromContext(ctx)
	if !ok {
		return nil, shared.ErrMissingInternalAuth
	}

	encoded, err := json.Marshal(sqlQueryRequest{QueryID: q.QueryID, Params: params})
	if err != nil {
		return nil, fmt.Errorf("%w: encoding request: %s", shared.ErrValidation, err)
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
		return nil, fmt.Errorf("%w: building request: %s", shared.ErrValidation, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(shared.InternalTokenHeader, s.internalToken)
	req.Header.Set(shared.DepartmentsHeader, strings.Join(departments, ","))

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connectors: sql-query %q: %w", alias, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var parsed sqlQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("%w: sql-query %q: decoding response: %s", shared.ErrUpstream, alias, err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w: sql-query %q: status %d", shared.ErrUpstream, alias, resp.StatusCode)
	}

	return map[string]any{"resultSet": parsed.ResultSet}, nil
}
