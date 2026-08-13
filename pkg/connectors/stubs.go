package connectors

import (
	"context"
	"errors"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

var ErrNotImplemented = errors.New("connectors: not implemented")

type stub struct {
	typ string
}

func (s stub) Type() string { return s.typ }

func (s stub) Execute(context.Context, map[string]any) (map[string]any, error) {
	return nil, ErrNotImplemented
}

func newStorage() Connector         { return stub{typ: registry.TypeStorage} }
func newSendEmail() Connector       { return stub{typ: registry.TypeSendEmail} }
func newDocumentExtract() Connector { return stub{typ: registry.TypeDocumentExtract} }
func newRestCall() Connector        { return stub{typ: registry.TypeRestCall} }
func newSQLQuery() Connector        { return stub{typ: registry.TypeSQLQuery} }
func newChatNotify() Connector      { return stub{typ: registry.TypeChatNotify} }
