package connectors

import (
	"context"
	"fmt"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/chatnotify"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/documentextract"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/restcall"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/sqlquery"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/storage"
)

type Connector interface {
	Type() string
	Execute(ctx context.Context, input map[string]any) (map[string]any, error)
}

// New builds all six registered connectors against cfg. It is fallible:
// InternalToken is required for rest-call/sql-query to attach to every
// outbound request, so an unconfigured Config fails immediately rather than
// at first dispatch.
func New(cfg Config) (map[string]Connector, error) {
	if cfg.InternalToken == "" {
		return nil, fmt.Errorf("connectors: InternalToken is required (rest-call/sql-query attach it to every outbound request)")
	}

	docRefs := shared.NewDocRefStore()
	built := []Connector{
		storage.New(cfg.StorageProviders, docRefs),
		sendemail.New(cfg.SendEmailProviders, docRefs),
		documentextract.New(cfg.DocumentExtractClient),
		chatnotify.New(cfg.ChatNotifyClient),
		restcall.New(cfg.Aliases, cfg.HTTPClient, cfg.InternalToken),
		sqlquery.New(cfg.Aliases, cfg.HTTPClient, cfg.InternalToken),
	}

	byType := make(map[string]Connector, len(built))
	for _, c := range built {
		if _, exists := byType[c.Type()]; exists {
			panic(fmt.Sprintf("connectors: duplicate connector Type %q", c.Type()))
		}
		byType[c.Type()] = c
	}
	return byType, nil
}
