package restcall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

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

func (Connector) Type() string { return registry.TypeRestCall }

func (r Connector) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	alias, _ := input["endpointAlias"].(string)
	if alias == "" {
		return nil, fmt.Errorf("%w: endpointAlias is required", shared.ErrValidation)
	}
	ep, err := aliasconfig.ResolveEndpoint(r.aliases, alias)
	if err != nil {
		return nil, err
	}

	departments, ok := shared.DepartmentsFromContext(ctx)
	if !ok {
		return nil, shared.ErrMissingInternalAuth
	}

	path, err := shared.RenderPathTemplate(ep.PathTemplate, shared.AsMap(input["pathParams"]))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", shared.ErrValidation, err)
	}
	fullURL := strings.TrimRight(ep.BaseURL, "/") + path

	var body io.Reader
	if b, ok := input["body"]; ok && b != nil {
		encoded, err := json.Marshal(b)
		if err != nil {
			return nil, fmt.Errorf("%w: encoding body: %s", shared.ErrValidation, err)
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
		return nil, fmt.Errorf("%w: building request: %s", shared.ErrValidation, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	shared.ApplyQueryParams(req, shared.AsMap(input["queryParams"]))
	req.Header.Set(shared.InternalTokenHeader, r.internalToken)
	req.Header.Set(shared.DepartmentsHeader, strings.Join(departments, ","))

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connectors: rest-call %q: %w", alias, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := shared.DecodeResponseBody(resp)
	if err != nil {
		return nil, fmt.Errorf("%w: rest-call %q: decoding response: %s", shared.ErrUpstream, alias, err)
	}

	out := map[string]any{
		"status":  resp.StatusCode,
		"headers": shared.FlattenHeaders(resp.Header),
		"body":    respBody,
	}
	if resp.StatusCode >= 400 {
		return out, fmt.Errorf("%w: rest-call %q: status %d", shared.ErrUpstream, alias, resp.StatusCode)
	}
	return out, nil
}
