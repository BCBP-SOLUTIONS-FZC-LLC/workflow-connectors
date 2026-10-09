package aliasconfig

import (
	"fmt"
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

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("aliasconfig: parsing %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("aliasconfig: %s: %w", path, err)
	}
	return cfg, nil
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
// userinfo. Which hosts are allowed is enforced where aliases are written
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
	return nil
}
