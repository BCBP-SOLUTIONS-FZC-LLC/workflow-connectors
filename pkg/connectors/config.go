package connectors

import (
	"net/http"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/chatnotify"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
)

type Config struct {
	Aliases       aliasconfig.Config
	HTTPClient    *http.Client
	InternalToken string

	StorageProviders   map[string]storage.ProviderConstructor
	SendEmailProviders map[string]sendemail.ProviderConstructor

	ChatNotifyClient chatnotify.ProviderClient

	// SendIntents enables send-email's duplicate-request protection for calls
	// that carry a messageKey (nil: a messageKey is a validation error). It
	// is not idempotency — see sendemail's delivery semantics.
	SendIntents sendintent.Store

	// DocRefs creates and resolves document refs. Production:
	// docref.NewService(valkeystore.New(...), s3content.New(...)) — content
	// in the platform's S3 document bucket, metadata in the shared Valkey —
	// so every replica resolves every ref. nil disables document refs:
	// storage createDocument, ref content and email attachments are then
	// validation errors. docref.NewMemoryService is for unit tests only.
	DocRefs *docref.Service
}
