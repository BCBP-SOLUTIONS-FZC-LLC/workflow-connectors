package shared

import (
	"errors"
	"fmt"
)

var (
	ErrValidation = errors.New("connectors: invalid input")

	ErrMissingInternalAuth = errors.New("connectors: missing internal auth context (WithDepartments not called)")

	ErrUpstream = errors.New("connectors: upstream call failed")
)

// Classify wraps an error returned by a provider client, a provider
// constructor or an internal service call made for op. An error that already
// carries ErrValidation keeps that class (permanent), so a missing credential
// is never mistaken for an upstream failure; anything else becomes
// ErrUpstream with its transient/permanent/unknown class — the class an
// adapter attached, or else one derived from the cause (ClassifyCause).
// ErrUpstream alone does not mean retryable: see IsTransient. The original
// error stays in the chain for errors.As.
func Classify(op string, err error) error {
	if errors.Is(err, ErrValidation) {
		return fmt.Errorf("%s: %w", op, err)
	}
	return fmt.Errorf("%w: %s: %w", ErrUpstream, op, ClassifyByCause(err))
}
