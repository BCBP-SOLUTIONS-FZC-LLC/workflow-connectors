package connectors

import (
	"net/http"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/chatnotify"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/documentextract"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/storage"
)

type Config struct {
	Aliases       aliasconfig.Config
	HTTPClient    *http.Client
	InternalToken string

	StorageProviders   map[string]storage.ProviderConstructor
	SendEmailProviders map[string]sendemail.ProviderConstructor

	DocumentExtractClient documentextract.ProviderClient
	ChatNotifyClient      chatnotify.ProviderClient
}
