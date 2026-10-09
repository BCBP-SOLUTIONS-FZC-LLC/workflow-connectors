package shared

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func AsMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// StringField returns m[key] as a string. A json.Number (a JSON number
// decoded by rest-call or sql-query and mapped into this input) is accepted
// as its exact decimal text.
func StringField(m map[string]any, key string) string {
	return asString(m[key])
}

func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case json.Number:
		return s.String()
	default:
		return ""
	}
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
		switch v.(type) {
		case string, json.Number:
			out = append(out, asString(v))
		}
	}
	return out
}

// RenderPathTemplate fills each {name} placeholder of tmpl from params.
// Before the first '?' a value is one path segment: it is url.PathEscape'd,
// and a value that renders as "", "." or ".." is ErrValidation — an empty
// segment ("//") or a dot segment would let a parameter change which
// resource the path names. After the first '?' a value is url.QueryEscape'd,
// so it can never add a query parameter. Every error wraps ErrValidation.
func RenderPathTemplate(tmpl string, params map[string]any) (string, error) {
	var b strings.Builder
	inQuery := false
	i := 0
	for i < len(tmpl) {
		switch tmpl[i] {
		case '{':
			end := strings.IndexByte(tmpl[i:], '}')
			if end == -1 {
				return "", fmt.Errorf("%w: unterminated path placeholder in %q", ErrValidation, tmpl)
			}
			name := tmpl[i+1 : i+end]
			val, ok := params[name]
			if !ok {
				return "", fmt.Errorf("%w: missing path param %q", ErrValidation, name)
			}
			formatted := FormatParam(val)
			if inQuery {
				b.WriteString(url.QueryEscape(formatted))
			} else {
				if formatted == "" || formatted == "." || formatted == ".." {
					return "", fmt.Errorf("%w: path param %q must not be empty, \".\" or \"..\"", ErrValidation, name)
				}
				b.WriteString(url.PathEscape(formatted))
			}
			i += end + 1
			continue
		case '?':
			inQuery = true
		}
		b.WriteByte(tmpl[i])
		i++
	}
	return b.String(), nil
}

// NewInternalRequest builds a rest-call or sql-query request to baseURL +
// path (an alias's rendered path, which starts with "/"). It refuses, with
// ErrValidation, any URL whose scheme or host differs from baseURL's or that
// carries userinfo: whatever an alias's path or a parameter holds, the
// request goes only to the alias's own service.
func NewInternalRequest(ctx context.Context, method, baseURL, path string, body io.Reader) (*http.Request, error) {
	full, err := joinInternalURL(baseURL, path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, full, body)
	if err != nil {
		return nil, fmt.Errorf("%w: building request: %w", ErrValidation, err)
	}
	return req, nil
}

func joinInternalURL(baseURL, path string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("%w: baseURL %q: %w", ErrValidation, baseURL, err)
	}
	full := strings.TrimRight(baseURL, "/") + path
	u, err := url.Parse(full)
	if err != nil {
		return "", fmt.Errorf("%w: request URL: %w", ErrValidation, err)
	}
	if !strings.HasPrefix(path, "/") || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil {
		return "", fmt.Errorf("%w: request URL must stay on the alias's scheme and host, with no userinfo", ErrValidation)
	}
	return full, nil
}

// ApplyQueryParams adds params to req's query. A key the alias's path
// template already sets is ErrValidation: a caller's parameter never
// overrides one the alias fixed.
func ApplyQueryParams(req *http.Request, params map[string]any) error {
	if len(params) == 0 {
		return nil
	}
	q := req.URL.Query()
	for k, v := range params {
		if q.Has(k) {
			return fmt.Errorf("%w: query param %q is fixed by the alias's path template", ErrValidation, k)
		}
		q.Set(k, FormatParam(v))
	}
	req.URL.RawQuery = q.Encode()
	return nil
}

// FlattenHeaders joins multi-value headers with ", ". Set-Cookie is dropped:
// a session cookie has no use in a workflow variable and must not land in one.
func FlattenHeaders(h http.Header) map[string]any {
	out := make(map[string]any, len(h))
	for k, v := range h {
		if http.CanonicalHeaderKey(k) == "Set-Cookie" {
			continue
		}
		out[k] = strings.Join(v, ", ")
	}
	return out
}

// DecodeResponseBody reads a successful response's body (at most
// MaxResponseBytes, else TooLarge) and decodes it with DecodeBody.
func DecodeResponseBody(resp *http.Response) (any, error) {
	raw, err := ReadAllLimited(resp.Body, MaxResponseBytes, "response body")
	if err != nil {
		return nil, err
	}
	return DecodeBody(raw, resp.Header.Get("Content-Type")), nil
}

// DecodeErrorResponseBody reads an error response's body and never fails, so
// the response's status alone classifies the call. A body over
// MaxResponseBytes is cut to its first MaxResponseBytes bytes, and a body
// whose read fails keeps what arrived; either is returned as a string (any
// incomplete UTF-8 sequence dropped), never JSON-decoded, with truncated
// true.
func DecodeErrorResponseBody(resp *http.Response) (body any, truncated bool) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err == nil && int64(len(raw)) <= MaxResponseBytes {
		return DecodeBody(raw, resp.Header.Get("Content-Type")), false
	}
	if int64(len(raw)) > MaxResponseBytes {
		raw = raw[:MaxResponseBytes]
	}
	return strings.ToValidUTF8(string(raw), ""), true
}

// DecodeBody returns nil for an empty body; the decoded JSON value when
// contentType contains "json" and raw is exactly one JSON value; else raw as
// a string. JSON numbers decode as json.Number, so an integer beyond 2^53
// (an ID, an amount in minor units) keeps every digit.
func DecodeBody(raw []byte, contentType string) any {
	if len(raw) == 0 {
		return nil
	}
	if strings.Contains(contentType, "json") {
		var v any
		if err := DecodeJSON(raw, &v); err == nil {
			return v
		}
	}
	return string(raw)
}

// DecodeJSON unmarshals raw, which must hold exactly one JSON value, into v
// with numbers as json.Number rather than float64.
func DecodeJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

// FormatParam renders a path or query parameter. A json.Number (how
// rest-call and sql-query decode JSON numbers) is its exact text. A float64
// (how encoding/json decodes numbers elsewhere) would print 1234567 as
// "1.234567e+06" with fmt.Sprint; whole numbers are printed as integers and
// others without an exponent.
func FormatParam(v any) string {
	switch n := v.(type) {
	case float64:
		return formatFloat(n)
	case float32:
		return formatFloat(float64(n))
	case json.Number:
		return n.String()
	default:
		return fmt.Sprint(v)
	}
}

func formatFloat(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1<<53 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
