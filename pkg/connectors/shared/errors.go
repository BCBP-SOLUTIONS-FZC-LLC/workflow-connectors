// Package shared holds what more than one connector uses, with no import back into the connector packages.
package shared

import "errors"

var (
	ErrValidation = errors.New("connectors: invalid input")

	ErrMissingInternalAuth = errors.New("connectors: missing internal auth context (WithDepartments not called)")

	ErrUpstream = errors.New("connectors: upstream call failed")
)
