package restcall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

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

func (Connector) Type() string { return registry.TypeRestCall }

func (r Connector) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	alias, _ := input["endpointAlias"].(string)
	if alias == "" {
		return nil, fmt.Errorf("%w: endpointAlias is required", shared.ErrValidation)
	}
	ep, err := aliasconfig.ResolveEndpoint(r.aliases, alias)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", shared.ErrValidation, err)
	}

	departments, err := shared.DepartmentsHeaderValue(ctx)
	if err != nil {
		return nil, err
	}

	path, err := shared.RenderPathTemplate(ep.PathTemplate, shared.AsMap(input["pathParams"]))
	if err != nil {
		return nil, err
	}

	var body io.Reader
	if b, ok := input["body"]; ok && b != nil {
		encoded, err := json.Marshal(b)
		if err != nil {
			return nil, fmt.Errorf("%w: encoding body: %s", shared.ErrValidation, err)
		}
		body = bytes.NewReader(encoded)
	}

	// Every call has a deadline: the alias's own timeout, else the default.
	reqCtx, cancel := context.WithTimeout(ctx, shared.CallTimeout(ep.Timeout))
	defer cancel()

	req, err := shared.NewInternalRequest(reqCtx, strings.ToUpper(ep.Method), ep.BaseURL, path, body)
	if err != nil {
		return nil, fmt.Errorf("rest-call %q: %w", alias, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := shared.ApplyQueryParams(req, shared.AsMap(input["queryParams"])); err != nil {
		return nil, err
	}
	req.Header.Set(shared.InternalTokenHeader, r.internalToken)
	req.Header.Set(shared.DepartmentsHeader, departments)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, shared.Classify(fmt.Sprintf("rest-call %q", alias), err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		// 3xx included: redirects are not followed, so one is a failed call.
		// The status alone classifies it: the body is read without failing
		// (shared.DecodeErrorResponseBody), so an oversized or cut-off error
		// page can never turn a transient 503 into a permanent error.
		body, truncated := shared.DecodeErrorResponseBody(resp)
		out := output(resp, body)
		if truncated {
			out["bodyTruncated"] = true
		}
		return out, fmt.Errorf("%w: %w", shared.ErrUpstream, shared.HTTPStatusError(resp.StatusCode, fmt.Errorf("rest-call %q: status %d", alias, resp.StatusCode)))
	}

	respBody, err := shared.DecodeResponseBody(resp)
	if err != nil {
		return nil, shared.Classify(fmt.Sprintf("rest-call %q: reading response", alias), err)
	}
	return output(resp, respBody), nil
}

func output(resp *http.Response, body any) map[string]any {
	return map[string]any{
		"status":  resp.StatusCode,
		"headers": shared.FlattenHeaders(resp.Header),
		"body":    body,
	}
}
