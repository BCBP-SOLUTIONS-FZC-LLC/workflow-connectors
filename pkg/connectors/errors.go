package connectors

import "github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"

// These re-export shared's sentinel errors under their long-standing root
// names: execution_service's cmd/connector-worker checks errors.Is against
// connectors.ErrValidation/ErrUpstream/ErrMissingInternalAuth directly, and
// every connector-type subpackage needs the same values without importing
// back into this package (that would recreate the cycle shared exists to
// avoid), so shared is the single source of truth and root just points at it.
var (
	ErrValidation          = shared.ErrValidation
	ErrMissingInternalAuth = shared.ErrMissingInternalAuth
	ErrUpstream            = shared.ErrUpstream
)
