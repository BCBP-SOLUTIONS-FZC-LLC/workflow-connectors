package connectors

import (
	"context"
	"fmt"
)

// Connector executes one connector-task invocation. Its own retry/backoff
// behavior is never internal — cmd/connector-worker's dispatcher owns that,
// keyed off registry.Definition.Retry. input carries author-supplied config
// with every registry.Field.IsSecretRef() value already resolved to its real
// credential by the caller — Connector implementations never see an OpenBao
// path, only the value.
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

	docRefs := newDocRefStore()
	built := []Connector{
		newStorage(cfg, docRefs),
		newSendEmail(cfg, docRefs),
		newDocumentExtract(cfg),
		newChatNotify(cfg),
		newRestCall(cfg),
		newSQLQuery(cfg),
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
