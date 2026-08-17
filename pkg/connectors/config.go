package connectors

import (
	"net/http"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/aliasconfig"
)

// Config wires runtime dependencies into the six connectors New builds.
// Provider-client fields are nil-defaultable: a nil override falls back to
// an in-memory mock, which is what every connector without a real SDK
// integration runs against until one is wired in (see storage.go,
// sendemail.go, documentextract.go, chatnotify.go).
type Config struct {
	// Aliases resolves rest-call's endpointAlias and sql-query's queryAlias
	// against cmd/connector-worker's own statically loaded registry
	// (aliasconfig.Load).
	Aliases aliasconfig.Config

	// HTTPClient backs rest-call and sql-query's HTTP dispatch. A nil value
	// defaults to a plain client — each call's own deadline comes from its
	// resolved alias's Timeout, not a client-wide timeout.
	HTTPClient *http.Client

	// InternalToken is attached as x-internal-token on every rest-call/
	// sql-query request, alongside the caller-supplied x-departments header
	// (see internalauth.go) — both mandatory per the LLD.
	InternalToken string

	StorageClient         StorageProviderClient
	SendEmailClient       SendEmailProviderClient
	DocumentExtractClient DocumentExtractProviderClient
	ChatNotifyClient      ChatNotifyProviderClient
}

func (c Config) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{}
}
