// Package aliasconfig defines the endpointAlias/queryAlias registry schema
// (LLD workflow_connectors.md §6.4.4/§6.4.5). definition_service owns and
// serves it; execution_service's cmd/connector-worker fetches and caches it.
// rest-call/sql-query's Execute() implementations resolve against the same
// Config shape everywhere, so the services that need it never drift.
package aliasconfig

import "time"

type Config struct {
	Version  int        `yaml:"version"`
	RestCall []Endpoint `yaml:"restCall"`
	SQLQuery []Query    `yaml:"sqlQuery"`
}

type Endpoint struct {
	Alias        string        `yaml:"alias"`
	Method       string        `yaml:"method"`
	BaseURL      string        `yaml:"baseURL"`
	PathTemplate string        `yaml:"pathTemplate"`
	Timeout      time.Duration `yaml:"timeout"`
}

type Query struct {
	Alias      string        `yaml:"alias"`
	BaseURL    string        `yaml:"baseURL"`
	Path       string        `yaml:"path"`
	QueryID    string        `yaml:"queryId"`
	ParamCount int           `yaml:"paramCount"`
	Timeout    time.Duration `yaml:"timeout"`
}
