package connectors

import (
	"net/http"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/aliasconfig"
)

type Config struct {
	Aliases       aliasconfig.Config
	HTTPClient    *http.Client
	InternalToken string

	StorageProviders   map[string]StorageProviderConstructor
	SendEmailProviders map[string]SendEmailProviderConstructor

	DocumentExtractClient DocumentExtractProviderClient
	ChatNotifyClient      ChatNotifyProviderClient
}

func (c Config) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{}
}
