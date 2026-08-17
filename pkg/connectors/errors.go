package connectors

import "errors"

var (
	// ErrValidation is returned when input fails a connector's own required-
	// field/shape checks, before any upstream call is attempted.
	ErrValidation = errors.New("connectors: invalid input")

	// ErrMissingInternalAuth is returned by rest-call/sql-query when the
	// caller didn't attach departments via WithDepartments — x-departments is
	// mandatory per the LLD, so this fails closed rather than sending the
	// request without it.
	ErrMissingInternalAuth = errors.New("connectors: missing internal auth context (WithDepartments not called)")

	// ErrUpstream wraps a failure that reached the target service (a non-2xx
	// response, or a response that couldn't be decoded) — distinct from a
	// bare transport error, so a caller can classify/log differently without
	// this package making a retry decision on its own behalf.
	ErrUpstream = errors.New("connectors: upstream call failed")
)
