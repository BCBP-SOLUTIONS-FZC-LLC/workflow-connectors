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

// RetryPolicy says which transient failures of a connector type may be
// retried automatically. A permanent or unknown failure is never retried,
// whatever the policy.
type RetryPolicy string

const (
	// RetryPolicySafe: every transient failure (storage).
	RetryPolicySafe RetryPolicy = "safe"
	// RetryPolicyUnsafe: never (chat-notify).
	RetryPolicyUnsafe RetryPolicy = "unsafe"
	// RetryPolicyConditional: transient failures of an idempotent HTTP
	// method only (rest-call).
	RetryPolicyConditional RetryPolicy = "conditional"
	// RetryPolicyNotDelivered: transient failures the provider provably never
	// accepted (send-email: DNS, connection refused, 429), so a retry cannot
	// duplicate the message. A failure whose delivery is unknown is never
	// retried.
	RetryPolicyNotDelivered RetryPolicy = "not-delivered"
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

	return indexByType(defs)
}

// indexByType keys definitions by Type, panicking on a duplicate: two
// definitions claiming one type is a programming error in All.
func indexByType(defs []Definition) map[string]Definition {
	byType := make(map[string]Definition, len(defs))
	for _, d := range defs {
		if _, exists := byType[d.Type]; exists {
			panic(fmt.Sprintf("registry: duplicate connector Type %q", d.Type))
		}
		byType[d.Type] = d
	}
	return byType
}
