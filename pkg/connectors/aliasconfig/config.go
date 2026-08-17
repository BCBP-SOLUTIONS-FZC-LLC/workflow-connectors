// Package aliasconfig defines and loads the static endpointAlias/queryAlias
// registry cmd/connector-worker reads at startup (LLD workflow_connectors.md
// §6.4.4/§6.4.5, Decision #12) — never a DB table, no admin CRUD API in v1.
// It lives here rather than in execution_service so the schema has exactly
// one owner: rest-call and sql-query's own Execute() implementations resolve
// against the same Config a caller loads via Load, so the two repos that
// need this shape never drift.
package aliasconfig

import "time"

// Config is the full alias registry: every rest-call endpointAlias and
// sql-query queryAlias this platform has pre-registered.
type Config struct {
	Version  int        `yaml:"version"`
	RestCall []Endpoint `yaml:"restCall"`
	SQLQuery []Query    `yaml:"sqlQuery"`
}

// Endpoint describes one rest-call endpointAlias: another service's own
// internal HTTP API, never a raw external URL (LLD §6.4.4). Method is fixed
// per alias — it feeds registry.IsIdempotentMethod for the RetryPolicyConditional
// decision, so it can't be left for the caller to supply at dispatch time.
type Endpoint struct {
	Alias        string        `yaml:"alias"`
	Method       string        `yaml:"method"`
	BaseURL      string        `yaml:"baseURL"`
	PathTemplate string        `yaml:"pathTemplate"`
	Timeout      time.Duration `yaml:"timeout"`
}

// Query describes one sql-query queryAlias: an owning service's own internal
// query-execution endpoint (an HTTP proxy, not a direct DB connection — see
// this feature's design-doc follow-up for why) plus the pre-registered,
// read-only query identifier that service runs on our behalf.
type Query struct {
	Alias      string        `yaml:"alias"`
	BaseURL    string        `yaml:"baseURL"`
	Path       string        `yaml:"path"`
	QueryID    string        `yaml:"queryId"`
	ParamCount int           `yaml:"paramCount"`
	Timeout    time.Duration `yaml:"timeout"`
}
