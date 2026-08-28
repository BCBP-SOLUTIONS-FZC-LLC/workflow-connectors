// Package shared holds state genuinely used across more than one connector
// type: sentinel errors, input-decoding helpers, the document-ref store
// (storage and send-email both resolve refs through it), internal-auth
// context plumbing, and the internal-call header names. It has zero
// dependency back on root pkg/connectors or any connector-type subpackage,
// the same way pkg/connectors/aliasconfig does — that's what lets each
// connector-type subpackage import it without an import cycle.
package shared

import "errors"

var (
	ErrValidation = errors.New("connectors: invalid input")

	ErrMissingInternalAuth = errors.New("connectors: missing internal auth context (WithDepartments not called)")

	ErrUpstream = errors.New("connectors: upstream call failed")
)
