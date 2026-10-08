package registry

func storageDefinition() Definition {
	return Definition{
		Type:        TypeStorage,
		DisplayName: "Storage (fetch / upload / delete)",
		Description: "Fetch, upload, or delete a document against an S3-compatible provider.",
		Inputs: []Field{
			{Name: "provider", Kind: FieldKindEnum, EnumValues: []string{"aws-s3", "azure-blob", "gcp-gcs", "google-drive"}, Description: "Backend provider for this call. Required — no default."},
			{Name: "accessKey", Kind: FieldKindSecretRef, Condition: "required for aws-s3", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker."},
			{Name: "secretKey", Kind: FieldKindSecretRef, Condition: "required for aws-s3", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker."},
			{Name: "region", Kind: FieldKindString, Condition: "optional, aws-s3"},
			{Name: "azureAccountName", Kind: FieldKindSecretRef, Condition: "required for azure-blob", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker."},
			{Name: "azureAccountKey", Kind: FieldKindSecretRef, Condition: "required for azure-blob", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker."},
			{Name: "gcpServiceAccountKey", Kind: FieldKindSecretRef, Condition: "required for gcp-gcs", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker."},
			{Name: "projectId", Kind: FieldKindString, Condition: "required for gcp-gcs"},
			{Name: "driveServiceAccountKey", Kind: FieldKindSecretRef, Condition: "required for google-drive", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker. Domain-wide delegation or direct folder sharing with the service account, per the target folder's own sharing settings."},
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
			{Name: "fetchedAt", Kind: FieldKindTimestamp},
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
			{Name: "provider", Kind: FieldKindEnum, EnumValues: []string{"sendgrid", "aws-ses", "microsoft-365", "google-workspace"}, Description: "Backend provider for this call. Required — no default."},
			{Name: "apiKey", Kind: FieldKindSecretRef, Condition: "required for sendgrid", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker."},
			{Name: "accessKey", Kind: FieldKindSecretRef, Condition: "required for aws-ses", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker."},
			{Name: "secretKey", Kind: FieldKindSecretRef, Condition: "required for aws-ses", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker."},
			{Name: "region", Kind: FieldKindString, Condition: "required for aws-ses"},
			{Name: "tenantId", Kind: FieldKindString, Condition: "required for microsoft-365"},
			{Name: "clientId", Kind: FieldKindString, Condition: "required for microsoft-365"},
			{Name: "clientSecret", Kind: FieldKindSecretRef, Condition: "required for microsoft-365", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker."},
			{Name: "serviceAccountKey", Kind: FieldKindSecretRef, Condition: "required for google-workspace", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker. Domain-wide delegation impersonates senderEmail; the service account needs the gmail.send scope granted for that."},
			{Name: "senderName", Kind: FieldKindString},
			{Name: "senderEmail", Kind: FieldKindString, Required: true},
			{Name: "receiverName", Kind: FieldKindString},
			{Name: "receiverEmail", Kind: FieldKindString, Required: true, Description: "Typically sourced from workflow-instance data."},
			{Name: "subject", Kind: FieldKindString},
			{Name: "contentType", Kind: FieldKindEnum, EnumValues: []string{"text/plain", "text/html"}},
			{Name: "body", Kind: FieldKindString, Condition: "required for microsoft-365/google-workspace"},
			{Name: "attachments", Kind: FieldKindList, Description: "List of document refs."},
			{Name: "templateId", Kind: FieldKindString, Description: "If set, subject/body are ignored in favor of the provider's own template rendering (sendgrid/aws-ses only). microsoft-365/google-workspace have no server-side template mechanism — ignored for those two, so body is required instead."},
		},
		Outputs: []Field{
			{Name: "sent", Kind: FieldKindBool},
			{Name: "messageId", Kind: FieldKindString},
			{Name: "sentAt", Kind: FieldKindTimestamp},
		},
		Retry: RetryPolicyUnsafe,
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

func chatNotifyDefinition() Definition {
	return Definition{
		Type:        TypeChatNotify,
		DisplayName: "Chat Notify (Slack/Teams)",
		Description: "Create a channel, invite users, or post a message.",
		Inputs: []Field{
			{Name: "authToken", Kind: FieldKindSecretRef, Required: true, Description: "Tenant-stored provider credential, read from its OpenBao path by the worker."},
			{Name: "method", Kind: FieldKindEnum, Required: true, EnumValues: []string{"create-channel", "invite-to-channel", "post-message"}},
			{Name: "channelName", Kind: FieldKindString, Condition: "only for create-channel"},
			{Name: "visibility", Kind: FieldKindEnum, Condition: "only for create-channel", EnumValues: []string{"public", "private"}},
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
