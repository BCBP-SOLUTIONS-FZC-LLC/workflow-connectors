package connectors

import (
	"context"
	"fmt"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/chatnotify"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/restcall"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
)

type Connector interface {
	Type() string
	Execute(ctx context.Context, input map[string]any) (map[string]any, error)
}

// New builds the connectors keyed by type: storage, send-email, chat-notify
// and rest-call. It fails when cfg.Aliases does not pass
// aliasconfig.Config.Validate; cfg.InternalToken is optional. chat-notify is built (a nil
// ChatNotifyClient fails every call with ErrValidation) although
// registry.All() does not list it. The storage and send-email connectors
// implement interface{ ResetClients() } to drop their cached provider
// clients (e.g. after a credential rotation).
func New(cfg Config) (map[string]Connector, error) {
	// Aliases built in code need not have come through aliasconfig.Load:
	// validate them here so a malformed alias fails at startup, not per call.
	// The zero Config (no aliases) is valid.
	if err := cfg.Aliases.Validate(); err != nil {
		return nil, fmt.Errorf("connectors: Aliases: %w", err)
	}

	// Valkey is the single authoritative document-ref store: storage and
	// send-email use it directly, with no cache or second store in front.
	docRefs := cfg.DocRefs
	built := []Connector{
		storage.New(cfg.StorageProviders, docRefs),
		sendemail.New(cfg.SendEmailProviders, docRefs, sendemail.WithSendIntents(cfg.SendIntents)),
		chatnotify.New(cfg.ChatNotifyClient),
		restcall.New(cfg.Aliases, cfg.HTTPClient, cfg.InternalToken),
	}

	return indexByType(built), nil
}

// indexByType keys connectors by Type, panicking on a duplicate: two
// connectors claiming one type is a programming error in New.
func indexByType(built []Connector) map[string]Connector {
	byType := make(map[string]Connector, len(built))
	for _, c := range built {
		if _, exists := byType[c.Type()]; exists {
			panic(fmt.Sprintf("connectors: duplicate connector Type %q", c.Type()))
		}
		byType[c.Type()] = c
	}
	return byType
}
