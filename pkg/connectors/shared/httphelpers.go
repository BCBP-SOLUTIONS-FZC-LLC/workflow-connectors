package shared

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func AsMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func StringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func BoolField(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func StringSliceField(m map[string]any, key string) []string {
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

func RenderPathTemplate(tmpl string, params map[string]any) (string, error) {
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

func ApplyQueryParams(req *http.Request, params map[string]any) {
	if len(params) == 0 {
		return
	}
	q := req.URL.Query()
	for k, v := range params {
		q.Set(k, fmt.Sprint(v))
	}
	req.URL.RawQuery = q.Encode()
}

func FlattenHeaders(h http.Header) map[string]any {
	out := make(map[string]any, len(h))
	for k, v := range h {
		out[k] = strings.Join(v, ", ")
	}
	return out
}

func DecodeResponseBody(resp *http.Response) (any, error) {
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
			// Mislabeled Content-Type shouldn't fail the whole call
			return string(raw), nil
		}
		return v, nil
	}
	return string(raw), nil
}
