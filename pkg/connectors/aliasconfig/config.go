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
