package aliasconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("aliasconfig: reading %s: %w", path, err)
	}

	cfg, err := decodeStrict(raw)
	if err != nil {
		return Config{}, fmt.Errorf("aliasconfig: parsing %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("aliasconfig: %s: %w", path, err)
	}
	return cfg, nil
}

// decodeStrict decodes the one YAML document with content, rejecting any key
// Config does not define: a misspelt key ("pathTemplte", "baseUrl") is an
// error, not a silently empty field. Empty documents (a bare "---", one with
// only comments, or an explicit null such as "--- ~" or "--- null") are
// ignored wherever they appear, so a leading or trailing "---" is harmless;
// a second document with content is an error. A file with no content is an
// empty Config.
func decodeStrict(raw []byte) (Config, error) {
	// First pass: find the document with content and make sure it is the
	// only one. yaml.Node decoding cannot apply KnownFields, so the content
	// document is decoded again, strictly, in the second pass.
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	content := -1
	for i := 0; ; i++ {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Config{}, err
		}
		if isEmptyDocument(&doc) {
			continue
		}
		if content >= 0 {
			return Config{}, fmt.Errorf("expected a single YAML document with content, found another in document %d", i+1)
		}
		content = i
	}
	if content < 0 {
		return Config{}, nil
	}

	var cfg Config
	dec = yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	for i := 0; i < content; i++ {
		// An empty document the first pass already decoded without error.
		var skip yaml.Node
		_ = dec.Decode(&skip)
	}
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// isEmptyDocument reports whether n is a document with no content or only a
// null scalar ("---" with nothing or only comments after it, "--- ~",
// "--- null").
func isEmptyDocument(n *yaml.Node) bool {
	return n.Kind == yaml.DocumentNode && (len(n.Content) == 0 ||
		len(n.Content) == 1 && n.Content[0].Kind == yaml.ScalarNode && n.Content[0].Tag == "!!null")
}

// Validate rejects a config that can never be valid. A restCall
// pathTemplate and a sqlQuery path must start with "/", so appended to the
// baseURL they can only extend its path, never change its host (a path
// "@evil.example" would turn "http://svc" into a URL with userinfo "svc" and
// host evil.example). rest-call and sql-query also check every request URL
// against the baseURL at call time (shared.NewInternalRequest), since a Config
// built in code need not have been validated.
func (c Config) Validate() error {
	seen := make(map[string]bool, len(c.RestCall))
	for _, e := range c.RestCall {
		if e.Alias == "" {
			return fmt.Errorf("restCall entry missing alias")
		}
		if seen[e.Alias] {
			return fmt.Errorf("restCall: duplicate alias %q", e.Alias)
		}
		seen[e.Alias] = true
		if !IsValidMethod(e.Method) {
			return fmt.Errorf("restCall %q: invalid method %q", e.Alias, e.Method)
		}
		if err := validateBaseURL(e.BaseURL); err != nil {
			return fmt.Errorf("restCall %q: %w", e.Alias, err)
		}
		if !strings.HasPrefix(e.PathTemplate, "/") {
			return fmt.Errorf("restCall %q: pathTemplate must start with \"/\"", e.Alias)
		}
		if err := validatePathTemplate(e.PathTemplate); err != nil {
			return fmt.Errorf("restCall %q: %w", e.Alias, err)
		}
	}

	seenQ := make(map[string]bool, len(c.SQLQuery))
	for _, q := range c.SQLQuery {
		if q.Alias == "" {
			return fmt.Errorf("sqlQuery entry missing alias")
		}
		if seenQ[q.Alias] {
			return fmt.Errorf("sqlQuery: duplicate alias %q", q.Alias)
		}
		seenQ[q.Alias] = true
		if err := validateBaseURL(q.BaseURL); err != nil {
			return fmt.Errorf("sqlQuery %q: %w", q.Alias, err)
		}
		if !strings.HasPrefix(q.Path, "/") {
			return fmt.Errorf("sqlQuery %q: path must start with \"/\"", q.Alias)
		}
		if q.QueryID == "" {
			return fmt.Errorf("sqlQuery %q: queryId is required", q.Alias)
		}
		if q.ParamCount < 0 {
			return fmt.Errorf("sqlQuery %q: paramCount must be >= 0", q.Alias)
		}
	}
	return nil
}

// validatePathTemplate rejects a pathTemplate (already known to start with
// "/") that cannot render to the URL it appears to describe:
//
//   - a "#" anywhere: url.Parse would read everything after it as a fragment,
//     which is never sent, so "/x#?role=user" would fix no query key at all
//     and a caller's queryParams could set role (a literal "#" is %23);
//   - a character that is not valid in a URL outside a placeholder (a space,
//     a control character, '"', '<', '>', '\', '^', '`' or '|');
//   - an unterminated "{" placeholder;
//   - a "%" in the path part not followed by two hex digits: "/x/%{id}" with
//     id "2F" would render an encoded slash;
//   - a query part with ";" or that url.ParseQuery rejects (see below).
//
// Placeholders are checked with a stand-in value, as
// shared.RenderPathTemplate escapes the real ones.
func validatePathTemplate(tmpl string) error {
	if strings.Contains(tmpl, "#") {
		return fmt.Errorf("pathTemplate must not contain \"#\" (a fragment is never sent; escape a literal as %%23)")
	}
	var b strings.Builder
	for i := 0; i < len(tmpl); i++ {
		c := tmpl[i]
		if c == '{' {
			end := strings.IndexByte(tmpl[i:], '}')
			if end == -1 {
				return fmt.Errorf("pathTemplate has an unterminated \"{\" placeholder")
			}
			b.WriteByte('x') // not a hex digit, so "%{id}" stays a bad escape
			i += end
			continue
		}
		if c <= ' ' || c == 0x7f || strings.IndexByte("\"<>\\^`|", c) >= 0 {
			return fmt.Errorf("pathTemplate contains %q, which is not valid in a URL (escape it)", c)
		}
		b.WriteByte(c)
	}
	path, query, hasQuery := strings.Cut(b.String(), "?")
	if _, err := url.PathUnescape(path); err != nil {
		return fmt.Errorf("pathTemplate path has an invalid escape (a literal %% is %%25): %w", err)
	}
	if !hasQuery {
		return nil
	}
	// Go's query parser drops a pair containing ";", so a key fixed after
	// one would not be seen as fixed and a caller could send it again.
	if strings.Contains(query, ";") {
		return fmt.Errorf("pathTemplate query must not contain \";\" (separate parameters with \"&\", escape a literal as %%3B)")
	}
	// The same strict parse shared.ApplyQueryParams applies at call time, so
	// a malformed query (a bad escape such as %zz) fails at load instead of
	// only on calls that pass queryParams.
	if _, err := url.ParseQuery(query); err != nil {
		return fmt.Errorf("pathTemplate query is not a valid query string: %w", err)
	}
	return nil
}

func IsValidMethod(m string) bool {
	switch strings.ToUpper(m) {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// validateBaseURL requires an absolute http or https URL with a host and no
// userinfo, query or fragment (the path template is appended to it, so a
// query or fragment in the baseURL would swallow the path). Which hosts are allowed is enforced where aliases are written
// (definition_service); this only rejects shapes that can never be valid.
func validateBaseURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("baseURL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("baseURL %q: %w", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return fmt.Errorf("baseURL %q must be an http or https URL with a host and no userinfo", raw)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("baseURL %q must not have a query or fragment", raw)
	}
	return nil
}
