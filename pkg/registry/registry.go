package registry

import (
	"fmt"
	"net/http"
	"strings"
)

const (
	TypeStorage         = "storage"
	TypeSendEmail       = "send-email"
	TypeDocumentExtract = "document-extract"
	TypeRestCall        = "rest-call"
	TypeSQLQuery        = "sql-query"
	TypeChatNotify      = "chat-notify"
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

// IsIdempotentMethod resolves RetryPolicyConditional's per-call condition for
// rest-call: retryable only when the resolved HTTP method is itself
// idempotent. A dispatcher has no other signal to key off, since
// Definition.Retry is one static value per connector type.
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

// IsSecretRef reports whether f's value must be resolved from OpenBao rather
// than carried inline — the one distinction every consumer that special-cases
// credential fields needs, kept here so that check isn't hand-rolled per
// caller.
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
		documentExtractDefinition(),
		restCallDefinition(),
		sqlQueryDefinition(),
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
