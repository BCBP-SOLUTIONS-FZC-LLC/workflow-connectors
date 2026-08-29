package registry

import (
	"fmt"
	"net/http"
	"strings"
)

const (
	TypeStorage    = "storage"
	TypeSendEmail  = "send-email"
	TypeRestCall   = "rest-call"
	TypeChatNotify = "chat-notify"

	// TypeDocumentExtract and TypeSQLQuery are disabled: neither has a
	// definition in All() below, so neither is part of the active connector
	// set. The consts stay because pkg/connectors/documentextract and
	// pkg/connectors/sqlquery — kept intact but no longer wired into
	// connectors.New() — still reference them for their own Type() methods.
	TypeDocumentExtract = "document-extract"
	TypeSQLQuery        = "sql-query"
)

type FieldKind string

const (
	FieldKindString    FieldKind = "string"
	FieldKindBool      FieldKind = "bool"
	FieldKindInt       FieldKind = "int"
	FieldKindFloat     FieldKind = "float"
	FieldKindEnum      FieldKind = "enum"
	FieldKindList      FieldKind = "list"
	FieldKindMap       FieldKind = "map"
	FieldKindAny       FieldKind = "any"
	FieldKindDocRef    FieldKind = "document_ref"
	FieldKindSecretRef FieldKind = "secret_ref"
	FieldKindTimestamp FieldKind = "timestamp"
)

type RetryPolicy string

const (
	RetryPolicySafe        RetryPolicy = "safe"
	RetryPolicyUnsafe      RetryPolicy = "unsafe"
	RetryPolicyConditional RetryPolicy = "conditional"
)

func IsIdempotentMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

type Field struct {
	Name        string
	Kind        FieldKind
	Required    bool
	EnumValues  []string
	Condition   string
	Description string
}

// IsSecretRef reports whether f's value must be resolved from OpenBao
func (f Field) IsSecretRef() bool {
	return f.Kind == FieldKindSecretRef
}

type Definition struct {
	Type        string
	DisplayName string
	Description string
	Inputs      []Field
	Outputs     []Field
	Retry       RetryPolicy
}

func All() map[string]Definition {
	defs := []Definition{
		storageDefinition(),
		sendEmailDefinition(),
		restCallDefinition(),
		chatNotifyDefinition(),
	}

	byType := make(map[string]Definition, len(defs))
	for _, d := range defs {
		if _, exists := byType[d.Type]; exists {
			panic(fmt.Sprintf("registry: duplicate connector Type %q", d.Type))
		}
		byType[d.Type] = d
	}
	return byType
}
