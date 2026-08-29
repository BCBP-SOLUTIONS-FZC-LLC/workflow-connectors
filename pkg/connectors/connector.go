package connectors

import (
	"context"
	"fmt"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/chatnotify"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/restcall"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/storage"
)

type Connector interface {
	Type() string
	Execute(ctx context.Context, input map[string]any) (map[string]any, error)
}

// New builds the four active registered connectors against cfg. It is
// fallible: InternalToken is required for rest-call to attach to every
// outbound request, so an unconfigured Config fails immediately rather than
// at first dispatch.
//
// sql-query and document-extract are implemented in their own subpackages
// but are deliberately not wired in here: sql-query has no evidence of
// production use, and document-extract needs a real redesign (from its
// current Textract-shaped multi-mode design to a simple LLM API call)
// before it's worth activating. Both subpackages are left intact so either
// can be re-added by restoring their entries here.
func New(cfg Config) (map[string]Connector, error) {
	if cfg.InternalToken == "" {
		return nil, fmt.Errorf("connectors: InternalToken is required (rest-call attaches it to every outbound request)")
	}

	docRefs := shared.NewDocRefStore()
	built := []Connector{
		storage.New(cfg.StorageProviders, docRefs),
		sendemail.New(cfg.SendEmailProviders, docRefs),
		chatnotify.New(cfg.ChatNotifyClient),
		restcall.New(cfg.Aliases, cfg.HTTPClient, cfg.InternalToken),
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
