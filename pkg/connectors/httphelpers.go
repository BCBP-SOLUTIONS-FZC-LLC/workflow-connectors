package connectors

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// asMap safely type-asserts an `any` from a connector's input map into
// map[string]any, treating anything else (nil, wrong type) as empty rather
// than panicking — author-supplied input is untrusted shape, not a Go type.
func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func boolField(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func stringSliceField(m map[string]any, key string) []string {
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// renderPathTemplate substitutes {name} placeholders in tmpl from params,
// URL-path-escaping every substituted value. An unresolved placeholder is a
// validation error, not a literal "{name}" left in the URL.
func renderPathTemplate(tmpl string, params map[string]any) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(tmpl) {
		if tmpl[i] == '{' {
			end := strings.IndexByte(tmpl[i:], '}')
			if end == -1 {
				return "", fmt.Errorf("unterminated path placeholder in %q", tmpl)
			}
			name := tmpl[i+1 : i+end]
			val, ok := params[name]
			if !ok {
				return "", fmt.Errorf("missing path param %q", name)
			}
			b.WriteString(url.PathEscape(fmt.Sprint(val)))
			i += end + 1
			continue
		}
		b.WriteByte(tmpl[i])
		i++
	}
	return b.String(), nil
}

func applyQueryParams(req *http.Request, params map[string]any) {
	if len(params) == 0 {
		return
	}
	q := req.URL.Query()
	for k, v := range params {
		q.Set(k, fmt.Sprint(v))
	}
	req.URL.RawQuery = q.Encode()
}

func flattenHeaders(h http.Header) map[string]any {
	out := make(map[string]any, len(h))
	for k, v := range h {
		out[k] = strings.Join(v, ", ")
	}
	return out
}

// decodeResponseBody returns a JSON-decoded body when the response says
// it's JSON, else the raw text — never silently dropped either way.
func decodeResponseBody(resp *http.Response) (any, error) {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "json") {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			// Mislabeled Content-Type shouldn't fail the whole call — fall
			// back to raw text.
			return string(raw), nil
		}
		return v, nil
	}
	return string(raw), nil
}
