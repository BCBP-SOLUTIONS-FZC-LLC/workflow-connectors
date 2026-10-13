package registry

import (
	"fmt"
	"net/http"
	"slices"
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

// All returns the definitions of the connector types a workflow may be
// authored with: storage, send-email and rest-call. chat-notify is
// deliberately absent (see ChatNotifyDefinition), as are sql-query and
// document-extract, which have no definition.
func All() map[string]Definition {
	defs := []Definition{
		storageDefinition(),
		sendEmailDefinition(),
		restCallDefinition(),
	}

	return indexByType(defs)
}

// Lookup returns the definition of connectorType: any type in All(), plus
// chat-notify (ChatNotifyDefinition), which connectors.New still builds.
// Runtime code deciding about a connector's result (retry policy) uses
// Lookup; authoring uses All. Lookup is called on every failed task, so the
// definitions are built once; each call returns a copy (its Inputs, Outputs
// and EnumValues slices included) that the caller may modify freely.
func Lookup(connectorType string) (Definition, bool) {
	def, ok := lookupDefs[connectorType]
	if !ok {
		return Definition{}, false
	}
	return cloneDefinition(def), true
}

// lookupDefs holds every definition Lookup serves, built once. It is never
// handed out: Lookup returns clones.
var lookupDefs = func() map[string]Definition {
	defs := All()
	chat := ChatNotifyDefinition()
	defs[chat.Type] = chat
	return defs
}()

func cloneDefinition(d Definition) Definition {
	d.Inputs = cloneFields(d.Inputs)
	d.Outputs = cloneFields(d.Outputs)
	return d
}

func cloneFields(fields []Field) []Field {
	out := slices.Clone(fields)
	for i := range out {
		out[i].EnumValues = slices.Clone(out[i].EnumValues)
	}
	return out
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
