package registry

func storageDefinition() Definition {
	return Definition{
		Type:        TypeStorage,
		DisplayName: "Storage (fetch / upload / delete)",
		Description: "Fetch, upload, or delete a document in the tenant's object store or Google Drive folder.",
		Inputs: []Field{
			{Name: "provider", Kind: FieldKindEnum, EnumValues: []string{"aws-s3", "azure-blob", "gcp-gcs", "google-drive"}, Description: "Backend provider for this call. Required — no default."},
			{Name: "accessKey", Kind: FieldKindSecretRef, Condition: "required for aws-s3", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker."},
			{Name: "secretKey", Kind: FieldKindSecretRef, Condition: "required for aws-s3", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker."},
			{Name: "region", Kind: FieldKindString, Condition: "required for aws-s3", Description: "The bucket's AWS region. Never taken from the worker's own environment."},
			{Name: "azureAccountName", Kind: FieldKindSecretRef, Condition: "required for azure-blob", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker."},
			{Name: "azureAccountKey", Kind: FieldKindSecretRef, Condition: "required for azure-blob", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker."},
			{Name: "gcpServiceAccountKey", Kind: FieldKindSecretRef, Condition: "required for gcp-gcs", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker."},
			{Name: "projectId", Kind: FieldKindString, Condition: "optional, gcp-gcs", Description: "Not needed for object operations; the service-account key identifies the project."},
			{Name: "driveServiceAccountKey", Kind: FieldKindSecretRef, Condition: "required for google-drive", Description: "Tenant-stored provider credential, read from its OpenBao path by the worker. Domain-wide delegation or direct folder sharing with the service account, per the target folder's own sharing settings."},
			{Name: "operation", Kind: FieldKindEnum, Required: true, EnumValues: []string{"fetch", "upload", "delete"}},
			{Name: "bucket", Kind: FieldKindString, Required: true},
			{Name: "key", Kind: FieldKindString, Required: true, Description: "The document path/identifier; may be sourced from a workflow variable."},
			{Name: "content", Kind: FieldKindDocRef, Condition: "only for upload", Description: "A document ref (docref:…), or literal content encoded as contentEncoding says. A ref resolves on any worker replica for 24 h, for the same tenant only."},
			{Name: "contentEncoding", Kind: FieldKindEnum, Condition: "only for upload, literal content", EnumValues: []string{"utf-8", "base64"}, Description: "How literal content is encoded. Defaults to utf-8; use base64 for binary content."},
			{Name: "contentType", Kind: FieldKindString, Condition: "only for upload", Description: "MIME type stored with the uploaded object. For ref content it defaults to the ref's own content type."},
			{Name: "createDocument", Kind: FieldKindBool, Condition: "fetch or upload", Description: "true stores the bytes as a document (S3, with its reference in Valkey) and returns a contentRef any worker replica can resolve for 24 h. On fetch, false returns content inline, allowed up to 1 MiB; on upload, false returns no contentRef."},
		},
		Outputs: []Field{
			{Name: "contentRef", Kind: FieldKindDocRef, Condition: "with createDocument, or an upload whose content was a ref", Description: "A document ref (docref:…), stored durably before it is returned."},
			{Name: "content", Kind: FieldKindString, Condition: "fetch without createDocument (inline, up to 1 MiB)"},
			{Name: "contentEncoding", Kind: FieldKindEnum, Condition: "with inline content", EnumValues: []string{"utf-8", "base64"}, Description: "base64 when the bytes are not valid UTF-8."},
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
		Description: "Send an email via the configured provider. Not idempotent: at-most-once when not retried, effectively at-least-once when retried after an uncertain outcome. Exactly-once delivery is not provided. A failed send is retried automatically only when the provider provably never accepted it and the failure is transient (DNS, connection refused, 429); a send whose outcome is unknown is never retried automatically. Attachment limits (total raw bytes): sendgrid 20 MiB, aws-ses 25 MiB, microsoft-365 3 MiB (inline sendMail), google-workspace 25 MiB.",
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
			{Name: "attachments", Kind: FieldKindList, Description: "List of document refs (from storage); each must belong to the same tenant and be unexpired. Total raw size at most 25 MiB, and at most the provider's own limit: sendgrid 20 MiB, microsoft-365 3 MiB; larger is a permanent validation failure (not_delivered)."},
			{Name: "messageKey", Kind: FieldKindString, Description: "Optional business-level message identifier. With a send-intent store configured, a second request with the same key is rejected before anything is sent. Duplicate-request protection only — not idempotency: it cannot prevent a provider-level duplicate after an uncertain outcome."},
			{Name: "resend", Kind: FieldKindBool, Condition: "only with messageKey; requires resendAttempt", Description: "Explicit opt-in to send again for a messageKey that already has an intent. If the earlier outcome was accepted or unknown, the recipient may get the email twice."},
			{Name: "resendAttempt", Kind: FieldKindInt, Condition: "required with resend", Description: "The attempt being resent: the intent's current attempts, as shown in the duplicate output (attempts) and error. The resend proceeds only while that attempt is current and finished (not pending, unless pending unchanged for 15 minutes: the worker stopped mid-send), so a redelivered resend request is a duplicate and never sends again."},
			{Name: "templateId", Kind: FieldKindString, Description: "If set, subject/body are ignored in favor of the provider's own template rendering (sendgrid/aws-ses only). microsoft-365/google-workspace have no server-side template mechanism — ignored for those two, so body is required instead."},
		},
		Outputs: []Field{
			{Name: "sent", Kind: FieldKindBool},
			{Name: "messageId", Kind: FieldKindString},
			{Name: "sentAt", Kind: FieldKindTimestamp},
			{Name: "deliveryOutcome", Kind: FieldKindEnum, EnumValues: []string{"accepted", "not_delivered", "unknown"}, Description: "accepted: the provider accepted the message. not_delivered: it definitively did not. unknown: it may have — verify delivery before any resend, which may duplicate the email. Returned with every result, failures included (a failure before sending is not_delivered); on a duplicate it is the existing intent's outcome (pending reports unknown)."},
			{Name: "sendIntentId", Kind: FieldKindString, Condition: "with messageKey", Description: "The send intent recorded for this request."},
			{Name: "sendIntentWarning", Kind: FieldKindString, Condition: "with messageKey, when the outcome could not be recorded", Description: "The send intent stays pending: treat the delivery as unknown and verify it before any resend."},
			{Name: "duplicate", Kind: FieldKindBool, Condition: "with messageKey, when the key already had an intent", Description: "Nothing was sent by this request. A duplicate of an accepted intent succeeds (no error), so a redelivered job is idempotent; any other duplicate fails with DuplicateRequestError (permanent)."},
			{Name: "status", Kind: FieldKindEnum, Condition: "with duplicate", EnumValues: []string{"pending", "accepted", "not_delivered", "unknown"}, Description: "The existing intent's status."},
			{Name: "attempts", Kind: FieldKindInt, Condition: "with duplicate", Description: "The existing intent's current attempt: the resendAttempt an explicit resend must name."},
			{Name: "providerMessageId", Kind: FieldKindString, Condition: "with duplicate of an accepted intent", Description: "The provider message ID recorded when it was accepted."},
		},
		Retry: RetryPolicyNotDelivered,
	}
}

func restCallDefinition() Definition {
	return Definition{
		Type:        TypeRestCall,
		DisplayName: "REST Call (internal platform APIs only)",
		Description: "Alias-based, never a raw URL — the target is always another service on this platform, never an external domain.",
		Inputs: []Field{
			{Name: "endpointAlias", Kind: FieldKindString, Required: true, Description: "Resolved against cmd/connector-worker's own static internal-service registry — never a raw URL."},
			{Name: "pathParams", Kind: FieldKindMap, Description: "Fills the alias's {name} placeholders. A path segment value is path-escaped and must not be empty, \".\" or \"..\"."},
			{Name: "queryParams", Kind: FieldKindMap, Description: "Added to the query string; a key the alias's path template already sets is rejected, never overridden."},
			{Name: "body", Kind: FieldKindAny},
		},
		Outputs: []Field{
			{Name: "status", Kind: FieldKindInt},
			{Name: "headers", Kind: FieldKindMap},
			{Name: "body", Kind: FieldKindAny, Description: "JSON-decoded when the response Content-Type contains \"json\", with numbers kept as exact decimal text (json.Number, so integers beyond 2^53 lose no digits); otherwise a string."},
			{Name: "bodyTruncated", Kind: FieldKindBool, Description: "Set (true) only on a status >= 300 whose body was over the 10 MiB response cap or cut off: body is then its first 10 MiB, or what arrived, as a string."},
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
