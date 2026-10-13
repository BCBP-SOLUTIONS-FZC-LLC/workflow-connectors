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

// decodeStrict decodes exactly one YAML document (empty extra documents,
// such as a trailing "---", are ignored), rejecting any key Config
// does not define: a misspelt key ("pathTemplte", "baseUrl") is an error, not
// a silently empty field. An empty file is an empty Config.
func decodeStrict(raw []byte) (Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, nil
		}
		return Config{}, err
	}
	// Further documents are accepted only when empty: a file ending in "---"
	// (or "---" followed by comments) is one document, not two.
	for {
		var extra yaml.Node
		err := dec.Decode(&extra)
		if errors.Is(err, io.EOF) {
			return cfg, nil
		}
		if err != nil || !isEmptyDocument(&extra) {
			return Config{}, fmt.Errorf("expected a single YAML document")
		}
	}
}

// isEmptyDocument reports whether n is a document with no content or only a
// null scalar (what "---" with nothing, or only comments, after it decodes to).
func isEmptyDocument(n *yaml.Node) bool {
	return n.Kind == yaml.DocumentNode && (len(n.Content) == 0 ||
		len(n.Content) == 1 && n.Content[0].Kind == yaml.ScalarNode &&
			n.Content[0].Tag == "!!null" && n.Content[0].Value == "")
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
		// Go's query parser drops a pair containing ";", so a key fixed after
		// one would not be seen as fixed and a caller could send it again.
		if _, query, ok := strings.Cut(e.PathTemplate, "?"); ok {
			if strings.Contains(query, ";") {
				return fmt.Errorf("restCall %q: pathTemplate query must not contain \";\" (separate parameters with \"&\", escape a literal as %%3B)", e.Alias)
			}
			// The same strict parse shared.ApplyQueryParams applies at call
			// time, so a malformed query (a bad escape such as %zz) fails at
			// load instead of only on calls that pass queryParams.
			if _, err := url.ParseQuery(query); err != nil {
				return fmt.Errorf("restCall %q: pathTemplate query is not a valid query string: %w", e.Alias, err)
			}
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
