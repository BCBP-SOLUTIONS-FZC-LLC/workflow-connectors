package shared

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
)

// Error classification
//
// Every error a connector returns has exactly one class:
//
//   - ClassTransient: the same call may succeed later (throttling, timeouts,
//     5xx, connection failures, a document upload in progress). Safe to
//     retry, subject to the connector type's retry policy.
//   - ClassPermanent: retrying the same input cannot succeed (invalid input,
//     a missing object, access denied, a checksum mismatch). Never retried.
//   - ClassUnknown: the library cannot tell (an unrecognised error, a
//     cancelled call, an email whose delivery outcome is unknown). Never
//     retried automatically: an unknown failure is treated like a permanent
//     one until a person decides.
//
// A class is attached where the provider's error is understood — the adapter
// or core that called the provider — and read anywhere with ClassOf. The
// original error always stays in the chain for errors.As.
//
// There is deliberately no errors.Is sentinel per class: errors.Is matches
// any classified error anywhere in the chain, while the class is decided by
// ClassOf's precedence (input errors first, then the outermost Classifier),
// so the two could disagree. Read the class only with ClassOf, IsTransient,
// IsPermanent or connectors.DecideRetry.

// Class is an error's retry classification.
type Class int

const (
	ClassUnknown Class = iota
	ClassTransient
	ClassPermanent
)

func (c Class) String() string {
	switch c {
	case ClassTransient:
		return "transient"
	case ClassPermanent:
		return "permanent"
	default:
		return "unknown"
	}
}

// Classifier is implemented by errors that carry their own class (for
// example *ClassifiedError and send-email's *SendError).
type Classifier interface {
	ErrorClass() (Class, string)
}

// ClassifiedError attaches a class and a short reason (for logs and metrics,
// e.g. "http 503", "s3 SlowDown", "timeout") to an error.
type ClassifiedError struct {
	Class  Class
	Reason string
	Err    error
}

func (e *ClassifiedError) Error() string { return e.Err.Error() }
func (e *ClassifiedError) Unwrap() error { return e.Err }

func (e *ClassifiedError) ErrorClass() (Class, string) { return e.Class, e.Reason }

// WithClass attaches class and reason to err.
func WithClass(class Class, reason string, err error) error {
	if err == nil {
		return nil
	}
	return &ClassifiedError{Class: class, Reason: reason, Err: err}
}

// Transient marks err as safe to retry.
func Transient(reason string, err error) error { return WithClass(ClassTransient, reason, err) }

// Permanent marks err as never retryable.
func Permanent(reason string, err error) error { return WithClass(ClassPermanent, reason, err) }

// ClassOf returns err's class and the reason for it. Input and wiring errors
// (ErrValidation, ErrMissingInternalAuth, ErrMissingTenant) are permanent;
// otherwise the outermost Classifier in the chain decides; anything else is
// unknown.
func ClassOf(err error) (Class, string) {
	switch {
	case err == nil:
		return ClassUnknown, ""
	case errors.Is(err, ErrValidation):
		return ClassPermanent, "invalid input"
	case errors.Is(err, ErrMissingInternalAuth), errors.Is(err, ErrMissingTenant):
		return ClassPermanent, "missing call context"
	}
	var c Classifier
	if errors.As(err, &c) {
		return c.ErrorClass()
	}
	return ClassUnknown, "unclassified"
}

// IsTransient reports whether err is classified transient.
func IsTransient(err error) bool { c, _ := ClassOf(err); return c == ClassTransient }

// IsPermanent reports whether err is classified permanent.
func IsPermanent(err error) bool { c, _ := ClassOf(err); return c == ClassPermanent }

// ClassifyHTTPStatus classifies a non-2xx HTTP status from a provider or an
// internal service. 408, 425, 429, 500, 502, 503 and 504 are transient; 3xx
// (redirects are never followed) and every other 4xx are permanent; other
// 5xx (501, 505, 507, ...) are unknown.
func ClassifyHTTPStatus(status int) Class {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return ClassTransient
	}
	switch {
	case status >= 300 && status < 500:
		return ClassPermanent
	default:
		return ClassUnknown
	}
}

// HTTPStatusError attaches the class of an HTTP status to err.
func HTTPStatusError(status int, err error) error {
	return WithClass(ClassifyHTTPStatus(status), fmt.Sprintf("http %d", status), err)
}

// apiErrorCodes classifies the error codes AWS services (S3 in particular)
// return; they are read from any error exposing ErrorCode() string.
var apiErrorCodes = map[string]Class{
	"NoSuchKey":                ClassPermanent,
	"NoSuchBucket":             ClassPermanent,
	"NotFound":                 ClassPermanent,
	"AccessDenied":             ClassPermanent,
	"InvalidBucketName":        ClassPermanent,
	"InvalidAccessKeyId":       ClassPermanent,
	"SignatureDoesNotMatch":    ClassPermanent,
	"InvalidArgument":          ClassPermanent,
	"InvalidRequest":           ClassPermanent,
	"EntityTooLarge":           ClassPermanent,
	"KeyTooLongError":          ClassPermanent,
	"MessageRejected":          ClassPermanent,
	"RequestTimeout":           ClassTransient,
	"RequestTimeTooSkewed":     ClassTransient,
	"SlowDown":                 ClassTransient,
	"Throttling":               ClassTransient,
	"ThrottlingException":      ClassTransient,
	"TooManyRequestsException": ClassTransient,
	"InternalError":            ClassTransient,
	"ServiceUnavailable":       ClassTransient,
}

// ClassifyCause derives a class from an error with no class attached, using
// what any provider error exposes: an AWS-style ErrorCode(), an
// HTTPStatusCode(), or a network failure. It returns ClassUnknown when none
// applies.
func ClassifyCause(err error) (Class, string) {
	if err == nil {
		return ClassUnknown, ""
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		if c, known := apiErrorCodes[coded.ErrorCode()]; known {
			return c, "api " + coded.ErrorCode()
		}
	}
	var resp interface{ HTTPStatusCode() int }
	if errors.As(err, &resp) && resp.HTTPStatusCode() >= 300 {
		return ClassifyHTTPStatus(resp.HTTPStatusCode()), fmt.Sprintf("http %d", resp.HTTPStatusCode())
	}
	return classifyNetwork(err)
}

func classifyNetwork(err error) (Class, string) {
	if errors.Is(err, context.Canceled) {
		return ClassUnknown, "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ClassTransient, "timeout"
	}
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return ClassTransient, "dns"
	}
	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		return ClassTransient, "timeout"
	}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return ClassTransient, "connection refused"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE),
		errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return ClassTransient, "connection reset"
	}
	if opErr, ok := errors.AsType[*net.OpError](err); ok && opErr.Op == "dial" {
		return ClassTransient, "connection failed"
	}
	return ClassUnknown, "unclassified"
}

// ClassifyByCause attaches ClassifyCause's class to err, unless err already
// has one (or is an input error).
func ClassifyByCause(err error) error {
	if err == nil {
		return nil
	}
	if c, _ := ClassOf(err); c != ClassUnknown {
		return err
	}
	var c Classifier
	if errors.As(err, &c) {
		return err // explicitly unknown: keep it
	}
	class, reason := ClassifyCause(err)
	return WithClass(class, reason, err)
}
