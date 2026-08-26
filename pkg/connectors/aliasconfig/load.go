package aliasconfig

import (
	"fmt"
	"net/http"
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
		if e.BaseURL == "" {
			return fmt.Errorf("restCall %q: baseURL is required", e.Alias)
		}
		if e.PathTemplate == "" {
			return fmt.Errorf("restCall %q: pathTemplate is required", e.Alias)
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
		if q.BaseURL == "" {
			return fmt.Errorf("sqlQuery %q: baseURL is required", q.Alias)
		}
		if q.Path == "" {
			return fmt.Errorf("sqlQuery %q: path is required", q.Alias)
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
