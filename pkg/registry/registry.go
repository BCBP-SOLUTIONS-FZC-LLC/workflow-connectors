package registry

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
)

type RetryPolicy string

const (
	RetryPolicySafe        RetryPolicy = "safe"
	RetryPolicyUnsafe      RetryPolicy = "unsafe"
	RetryPolicyConditional RetryPolicy = "conditional"
)

type Field struct {
	Name        string
	Kind        FieldKind
	Required    bool
	EnumValues  []string
	Condition   string
	Description string
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
		byType[d.Type] = d
	}
	return byType
}
