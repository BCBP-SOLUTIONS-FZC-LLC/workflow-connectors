package connectors

import "github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"

var (
	ErrValidation          = shared.ErrValidation
	ErrMissingInternalAuth = shared.ErrMissingInternalAuth
	ErrUpstream            = shared.ErrUpstream
	ErrMissingTenant       = shared.ErrMissingTenant

	// ErrNotDelivered and ErrDeliveryUnknown give every failed send-email
	// call its delivery outcome. Only a transient ErrNotDelivered is ever
	// retried automatically (see DecideRetry).
	ErrNotDelivered    = shared.ErrNotDelivered
	ErrDeliveryUnknown = shared.ErrDeliveryUnknown
)

// ErrorClass is an error's retry classification (see shared.ClassOf).
type ErrorClass = shared.Class

const (
	ClassUnknown   = shared.ClassUnknown
	ClassTransient = shared.ClassTransient
	ClassPermanent = shared.ClassPermanent
)

// ClassOf returns err's class and the classifier's short reason. It is the
// only way to read a class: there is no errors.Is sentinel per class.
func ClassOf(err error) (ErrorClass, string) { return shared.ClassOf(err) }

// IsTransient reports whether err is classified transient (may recover).
func IsTransient(err error) bool { return shared.IsTransient(err) }

// IsPermanent reports whether err is classified permanent (needs a fix).
func IsPermanent(err error) bool { return shared.IsPermanent(err) }

// IsRetryable reports whether err's class allows an automatic retry: only a
// transient error. Prefer DecideRetry, which also applies the connector
// type's retry policy.
func IsRetryable(err error) bool { return shared.IsRetryable(err) }
