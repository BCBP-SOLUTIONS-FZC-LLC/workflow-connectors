package connectors

import (
	"context"
	"fmt"
)

type Connector interface {
	Type() string
	Execute(ctx context.Context, input map[string]any) (map[string]any, error)
}

func New() map[string]Connector {
	connectors := []Connector{
		newStorage(),
		newSendEmail(),
		newDocumentExtract(),
		newRestCall(),
		newSQLQuery(),
		newChatNotify(),
	}

	byType := make(map[string]Connector, len(connectors))
	for _, c := range connectors {
		if _, exists := byType[c.Type()]; exists {
			panic(fmt.Sprintf("connectors: duplicate connector Type %q", c.Type()))
		}
		byType[c.Type()] = c
	}
	return byType
}
