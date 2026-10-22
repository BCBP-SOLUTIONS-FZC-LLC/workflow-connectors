// Package sqlquery is the sql-query connector core: it runs a named query
// (an alias's queryId) on an internal platform service.
//
// EXPERIMENTAL — not for production. It is not built by connectors.New nor
// listed in registry.All(), and its API may change in any release. Known
// gaps: the service alone scopes the query to the x-tenant-id it receives.
package sqlquery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	// paramCount is exact: 0 means the query takes no params, so a stray
	// params list is rejected rather than silently sent or dropped.
	var params []any
	if raw, present := input["params"]; present && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("%w: params must be a list, got %T", shared.ErrValidation, raw)
		}
		params = list
	}
	if len(params) != q.ParamCount {
		return nil, fmt.Errorf("%w: queryAlias %q expects %d params, got %d", shared.ErrValidation, alias, q.ParamCount, len(params))
	}

	caller, err := shared.CallerHeadersFromContext(ctx)
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
	caller.Apply(req.Header)

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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, shared.MaxResponseBytes+1))
	if err != nil {
		return nil, shared.Classify(fmt.Sprintf("sql-query %q: reading response", alias), err)
	}
	if int64(len(raw)) > shared.MaxResponseBytes {
		// As in rest-call: the service processed the query, so this is not
		// invalid input, and a retry would get the same oversized result.
		return nil, fmt.Errorf("%w: %w", shared.ErrUpstream, shared.Permanent(shared.ReasonResponseTooLarge,
			fmt.Errorf("%w: sql-query %q: status %d response body exceeds the %d-byte limit", shared.ErrTooLarge, alias, resp.StatusCode, shared.MaxResponseBytes)))
	}
	var parsed sqlQueryResponse
	if err := shared.DecodeJSON(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: %w", shared.ErrUpstream, shared.Permanent("malformed response", fmt.Errorf("sql-query %q: decoding response: %w", alias, err)))
	}

	return map[string]any{"resultSet": parsed.ResultSet}, nil
}
