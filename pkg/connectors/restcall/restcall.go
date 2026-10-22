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

	caller, err := shared.CallerHeadersFromContext(ctx)
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

	method := strings.ToUpper(ep.Method)
	req, err := shared.NewInternalRequest(reqCtx, method, ep.BaseURL, path, body)
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
	caller.Apply(req.Header)

	// From here on every result, failures included, carries the method sent,
	// so the worker's DecideRetry needs no second alias lookup.
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return map[string]any{"method": method}, shared.Classify(fmt.Sprintf("rest-call %q", alias), err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		// 3xx included: redirects are not followed, so one is a failed call.
		// The status alone classifies it: the body is read without failing
		// (shared.DecodeErrorResponseBody), so an oversized or cut-off error
		// page can never turn a transient 503 into a permanent error.
		body, truncated := shared.DecodeErrorResponseBody(resp)
		out := output(resp, method, body)
		if truncated {
			out["bodyTruncated"] = true
		}
		return out, fmt.Errorf("%w: %w", shared.ErrUpstream, shared.HTTPStatusError(resp.StatusCode, fmt.Errorf("rest-call %q: status %d", alias, resp.StatusCode)))
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, shared.MaxResponseBytes+1))
	if err != nil {
		return output(resp, method, nil), shared.Classify(fmt.Sprintf("rest-call %q: reading response", alias), err)
	}
	if int64(len(raw)) > shared.MaxResponseBytes {
		// The service processed the call: this is not invalid input, and a
		// retry would get the same oversized body. Report what is known.
		out := output(resp, method, strings.ToValidUTF8(string(raw[:shared.MaxResponseBytes]), ""))
		out["bodyTruncated"] = true
		return out, fmt.Errorf("%w: %w", shared.ErrUpstream, shared.Permanent(ReasonResponseTooLarge,
			fmt.Errorf("%w: rest-call %q: status %d response body exceeds the %d-byte limit", shared.ErrTooLarge, alias, resp.StatusCode, shared.MaxResponseBytes)))
	}
	return output(resp, method, shared.DecodeBody(raw, resp.Header.Get("Content-Type"))), nil
}

// ReasonResponseTooLarge is the ClassOf reason of a successful (2xx) call
// whose response body exceeded shared.MaxResponseBytes: a permanent failure,
// but not invalid input — the service did process the request. It equals
// shared.ReasonResponseTooLarge, which sql-query uses too.
const ReasonResponseTooLarge = shared.ReasonResponseTooLarge

func output(resp *http.Response, method string, body any) map[string]any {
	return map[string]any{
		"status":  resp.StatusCode,
		"method":  method,
		"headers": shared.FlattenHeaders(resp.Header),
		"body":    body,
	}
}
