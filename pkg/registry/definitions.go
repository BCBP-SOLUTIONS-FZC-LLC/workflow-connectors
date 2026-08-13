package registry

func storageDefinition() Definition {
	return Definition{
		Type:        TypeStorage,
		DisplayName: "Storage (fetch / upload / delete)",
		Description: "Fetch, upload, or delete a document against an S3-compatible provider.",
		Inputs: []Field{
			{Name: "accessKey", Kind: FieldKindSecretRef, Required: true, Description: "Author-supplied provider credential, resolved from its OpenBao secret path."},
			{Name: "secretKey", Kind: FieldKindSecretRef, Required: true, Description: "Author-supplied provider credential, resolved from its OpenBao secret path."},
			{Name: "region", Kind: FieldKindString},
			{Name: "operation", Kind: FieldKindEnum, Required: true, EnumValues: []string{"fetch", "upload", "delete"}},
			{Name: "bucket", Kind: FieldKindString, Required: true},
			{Name: "key", Kind: FieldKindString, Required: true, Description: "The document path/identifier; may be sourced from a workflow variable."},
			{Name: "content", Kind: FieldKindDocRef, Condition: "only for upload"},
			{Name: "createDocument", Kind: FieldKindBool, Condition: "only for fetch", Description: "true creates a document reference, false returns content inline."},
		},
		Outputs: []Field{
			{Name: "contentRef", Kind: FieldKindDocRef, Condition: "large objects"},
			{Name: "content", Kind: FieldKindString, Condition: "small objects, inline"},
			{Name: "contentType", Kind: FieldKindString},
			{Name: "sizeBytes", Kind: FieldKindInt},
			{Name: "fetchedAt", Kind: FieldKindString},
		},
		Retry: RetryPolicySafe,
	}
}

func sendEmailDefinition() Definition {
	return Definition{
		Type:        TypeSendEmail,
		DisplayName: "Send Email",
		Description: "Send an email via the configured provider.",
		Inputs: []Field{
			{Name: "apiKey", Kind: FieldKindSecretRef, Required: true, Description: "Author-supplied provider credential, resolved from its OpenBao secret path."},
			{Name: "senderName", Kind: FieldKindString},
			{Name: "senderEmail", Kind: FieldKindString, Required: true},
			{Name: "receiverName", Kind: FieldKindString},
			{Name: "receiverEmail", Kind: FieldKindString, Required: true, Description: "Typically sourced from workflow-instance data."},
			{Name: "subject", Kind: FieldKindString},
			{Name: "contentType", Kind: FieldKindEnum, EnumValues: []string{"text/plain", "text/html"}},
			{Name: "body", Kind: FieldKindString},
			{Name: "attachments", Kind: FieldKindList, Description: "List of document refs."},
			{Name: "templateId", Kind: FieldKindString, Description: "If set, subject/body are ignored in favor of the provider's own template rendering."},
		},
		Outputs: []Field{
			{Name: "sent", Kind: FieldKindBool},
			{Name: "messageId", Kind: FieldKindString},
			{Name: "sentAt", Kind: FieldKindString},
		},
		Retry: RetryPolicyUnsafe,
	}
}

func documentExtractDefinition() Definition {
	return Definition{
		Type:        TypeDocumentExtract,
		DisplayName: "Document Extract (OCR / structured extraction)",
		Description: "Real-time-only document analysis: form fields, signatures, layout, or targeted queries.",
		Inputs: []Field{
			{Name: "accessKey", Kind: FieldKindSecretRef, Required: true, Description: "Author-supplied provider credential, resolved from its OpenBao secret path."},
			{Name: "secretKey", Kind: FieldKindSecretRef, Required: true, Description: "Author-supplied provider credential, resolved from its OpenBao secret path."},
			{Name: "region", Kind: FieldKindString},
			{Name: "documentLocation", Kind: FieldKindEnum, Required: true, EnumValues: []string{"s3", "inline"}},
			{Name: "documentBucket", Kind: FieldKindString, Condition: "only if documentLocation=s3"},
			{Name: "documentName", Kind: FieldKindString, Condition: "only if documentLocation=s3"},
			{Name: "documentVersion", Kind: FieldKindString, Condition: "only if documentLocation=s3"},
			{Name: "documentRef", Kind: FieldKindDocRef, Condition: "only if documentLocation=inline"},
			{Name: "analyzeForm", Kind: FieldKindBool},
			{Name: "analyzeSignatures", Kind: FieldKindBool},
			{Name: "analyzeLayout", Kind: FieldKindBool},
			{Name: "analyzeQueries", Kind: FieldKindBool},
			{Name: "query", Kind: FieldKindString, Condition: "only if analyzeQueries"},
			{Name: "clientRequestToken", Kind: FieldKindString},
			{Name: "jobTag", Kind: FieldKindString},
			{Name: "kmsKeyId", Kind: FieldKindString},
		},
		Outputs: []Field{
			{Name: "fields", Kind: FieldKindMap, Condition: "for analyzeForm"},
			{Name: "rawText", Kind: FieldKindString, Description: "Full-text extraction."},
			{Name: "signaturesDetected", Kind: FieldKindList, Condition: "for analyzeSignatures"},
			{Name: "answers", Kind: FieldKindList, Condition: "for analyzeQueries"},
			{Name: "confidence", Kind: FieldKindFloat, Description: "Per-field confidence."},
		},
		Retry: RetryPolicySafe,
	}
}

func restCallDefinition() Definition {
	return Definition{
		Type:        TypeRestCall,
		DisplayName: "REST Call (internal platform APIs only)",
		Description: "Alias-based, never a raw URL — the target is always another service on this platform, never an external domain.",
		Inputs: []Field{
			{Name: "endpointAlias", Kind: FieldKindString, Required: true, Description: "Resolved against cmd/connector-worker's own static internal-service registry — never a raw URL."},
			{Name: "pathParams", Kind: FieldKindMap},
			{Name: "queryParams", Kind: FieldKindMap},
			{Name: "body", Kind: FieldKindAny},
		},
		Outputs: []Field{
			{Name: "status", Kind: FieldKindInt},
			{Name: "headers", Kind: FieldKindMap},
			{Name: "body", Kind: FieldKindAny},
		},
		Retry: RetryPolicyConditional,
	}
}

func sqlQueryDefinition() Definition {
	return Definition{
		Type:        TypeSQLQuery,
		DisplayName: "SQL Query (internal platform databases only, read-only)",
		Description: "Alias-based, read-only, never raw SQL — pre-registered named queries against this platform's own databases only.",
		Inputs: []Field{
			{Name: "queryAlias", Kind: FieldKindString, Required: true, Description: "A pre-registered named read-only query, resolved against cmd/connector-worker's own internal registry — never raw SQL."},
			{Name: "params", Kind: FieldKindList, Description: "Bound as query parameters, never string-interpolated."},
		},
		Outputs: []Field{
			{Name: "resultSet", Kind: FieldKindList, Description: "Row count bounded (a fixed cap) to keep context_json small."},
		},
		Retry: RetryPolicySafe,
	}
}

func chatNotifyDefinition() Definition {
	return Definition{
		Type:        TypeChatNotify,
		DisplayName: "Chat Notify (Slack/Teams)",
		Description: "Create a channel, invite users, or post a message.",
		Inputs: []Field{
			{Name: "authToken", Kind: FieldKindSecretRef, Required: true, Description: "Author-supplied provider credential, resolved from its OpenBao secret path."},
			{Name: "method", Kind: FieldKindEnum, Required: true, EnumValues: []string{"create-channel", "invite-to-channel", "post-message"}},
			{Name: "channelName", Kind: FieldKindString, Condition: "only for create-channel"},
			{Name: "visibility", Kind: FieldKindEnum, Condition: "only for create-channel"},
			{Name: "inviteBy", Kind: FieldKindString, Condition: "only for invite-to-channel"},
			{Name: "channelNameOrId", Kind: FieldKindString, Condition: "only for invite-to-channel"},
			{Name: "users", Kind: FieldKindList, Condition: "only for invite-to-channel"},
			{Name: "channelOrUser", Kind: FieldKindString, Condition: "only for post-message"},
			{Name: "thread", Kind: FieldKindString, Condition: "only for post-message"},
			{Name: "messageType", Kind: FieldKindString, Condition: "only for post-message"},
			{Name: "message", Kind: FieldKindString, Condition: "only for post-message, or messageBlock"},
			{Name: "messageBlock", Kind: FieldKindAny, Condition: "only for post-message, or message"},
			{Name: "attachments", Kind: FieldKindList, Condition: "only for post-message"},
		},
		Outputs: []Field{
			{Name: "sent", Kind: FieldKindBool},
			{Name: "messageId", Kind: FieldKindString},
		},
		Retry: RetryPolicyUnsafe,
	}
}
