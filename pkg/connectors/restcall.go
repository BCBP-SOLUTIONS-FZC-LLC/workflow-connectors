package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

const (
	internalTokenHeader = "x-internal-token"
	departmentsHeader   = "x-departments"
)

// restCall dispatches to another platform service's own internal HTTP API,
// resolved via endpointAlias — never a raw URL (LLD §6.4.4). It never
// retries internally: registry.RetryPolicyConditional's decision (is the
// resolved method idempotent?) is the caller's to make, using
// aliasconfig.ResolveEndpoint + registry.IsIdempotentMethod before dispatch.
type restCall struct {
	cfg Config
}

func newRestCall(cfg Config) Connector { return restCall{cfg: cfg} }

func (restCall) Type() string { return registry.TypeRestCall }

func (r restCall) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	alias, _ := input["endpointAlias"].(string)
	if alias == "" {
		return nil, fmt.Errorf("%w: endpointAlias is required", ErrValidation)
	}
	ep, err := aliasconfig.ResolveEndpoint(r.cfg.Aliases, alias)
	if err != nil {
		return nil, err
	}

	departments, ok := DepartmentsFromContext(ctx)
	if !ok {
		return nil, ErrMissingInternalAuth
	}

	path, err := renderPathTemplate(ep.PathTemplate, asMap(input["pathParams"]))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrValidation, err)
	}
	fullURL := strings.TrimRight(ep.BaseURL, "/") + path

	var body io.Reader
	if b, ok := input["body"]; ok && b != nil {
		encoded, err := json.Marshal(b)
		if err != nil {
			return nil, fmt.Errorf("%w: encoding body: %s", ErrValidation, err)
		}
		body = bytes.NewReader(encoded)
	}

	reqCtx := ctx
	if ep.Timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, ep.Timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(reqCtx, strings.ToUpper(ep.Method), fullURL, body)
	if err != nil {
		return nil, fmt.Errorf("%w: building request: %s", ErrValidation, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	applyQueryParams(req, asMap(input["queryParams"]))
	req.Header.Set(internalTokenHeader, r.cfg.InternalToken)
	req.Header.Set(departmentsHeader, strings.Join(departments, ","))

	resp, err := r.cfg.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("connectors: rest-call %q: %w", alias, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := decodeResponseBody(resp)
	if err != nil {
		return nil, fmt.Errorf("%w: rest-call %q: decoding response: %s", ErrUpstream, alias, err)
	}

	out := map[string]any{
		"status":  resp.StatusCode,
		"headers": flattenHeaders(resp.Header),
		"body":    respBody,
	}
	if resp.StatusCode >= 400 {
		return out, fmt.Errorf("%w: rest-call %q: status %d", ErrUpstream, alias, resp.StatusCode)
	}
	return out, nil
}
