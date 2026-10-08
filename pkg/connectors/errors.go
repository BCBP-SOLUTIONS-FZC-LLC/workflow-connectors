package connectors

import "github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"

// Sentinels re-exported from shared; execution_service checks them with errors.Is.
var (
	ErrValidation          = shared.ErrValidation
	ErrMissingInternalAuth = shared.ErrMissingInternalAuth
	ErrUpstream            = shared.ErrUpstream
)
