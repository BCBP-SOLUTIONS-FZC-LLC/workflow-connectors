package connectors

import "context"

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
		byType[c.Type()] = c
	}
	return byType
}
