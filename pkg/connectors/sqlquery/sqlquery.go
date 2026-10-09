package sqlquery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

type Connector struct {
	aliases       aliasconfig.Config
	httpClient    *http.Client
	internalToken string
}

// New builds the connector. httpClient is copied and never follows redirects
// (shared.InternalHTTPClient); nil means a default client. Each call is
// bounded by its alias's timeout, or shared.DefaultHTTPTimeout.
func New(aliases aliasconfig.Config, httpClient *http.Client, internalToken string) Connector {
	return Connector{aliases: aliases, httpClient: shared.InternalHTTPClient(httpClient), internalToken: internalToken}
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
		return nil, fmt.Errorf("%w: %w", shared.ErrValidation, err)
	}

	params, _ := input["params"].([]any)
	if q.ParamCount > 0 && len(params) != q.ParamCount {
		return nil, fmt.Errorf("%w: queryAlias %q expects %d params, got %d", shared.ErrValidation, alias, q.ParamCount, len(params))
	}

	departments, err := shared.DepartmentsHeaderValue(ctx)
	if err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(sqlQueryRequest{QueryID: q.QueryID, Params: params})
	if err != nil {
		return nil, fmt.Errorf("%w: encoding request: %s", shared.ErrValidation, err)
	}

	// Every call has a deadline: the alias's own timeout, else the default.
	reqCtx, cancel := context.WithTimeout(ctx, shared.CallTimeout(q.Timeout))
	defer cancel()

	req, err := shared.NewInternalRequest(reqCtx, http.MethodPost, q.BaseURL, q.Path, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("sql-query %q: %w", alias, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(shared.InternalTokenHeader, s.internalToken)
	req.Header.Set(shared.DepartmentsHeader, departments)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, shared.Classify(fmt.Sprintf("sql-query %q", alias), err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Status first: an error page is rarely the JSON shape below, and its
	// decode error would hide the status. 3xx included: redirects are not followed.
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: %w", shared.ErrUpstream, shared.HTTPStatusError(resp.StatusCode, fmt.Errorf("sql-query %q: status %d", alias, resp.StatusCode)))
	}
	raw, err := shared.ReadAllLimited(resp.Body, shared.MaxResponseBytes, "response body")
	if err != nil {
		return nil, shared.Classify(fmt.Sprintf("sql-query %q: reading response", alias), err)
	}
	var parsed sqlQueryResponse
	if err := shared.DecodeJSON(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: %w", shared.ErrUpstream, shared.Permanent("malformed response", fmt.Errorf("sql-query %q: decoding response: %w", alias, err)))
	}

	return map[string]any{"resultSet": parsed.ResultSet}, nil
}
