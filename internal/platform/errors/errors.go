// Package errors provides the domain error type and shared sentinels. A resource
// declares its own errors in its service package. Kind is transport-neutral, so
// a future gRPC or CLI consumer can map it without importing net/http.
package errors

// Kind classifies a domain error for transport mapping; clients branch on Code.
type Kind string

const (
	// KindInvalid is a client-supplied value that failed a domain invariant.
	KindInvalid Kind = "invalid"
	// KindUnauthenticated means credentials are missing or wrong.
	KindUnauthenticated Kind = "unauthenticated"
	// KindForbidden means the caller is known but not allowed.
	KindForbidden Kind = "forbidden"
	// KindNotFound means the resource does not exist.
	KindNotFound Kind = "not_found"
	// KindConflict means the write conflicts with existing state.
	KindConflict Kind = "conflict"
	// KindInternal means an unexpected server-side failure; its detail is private.
	KindInternal Kind = "internal"
)

// Error is a domain error; it classifies the failure but not its HTTP status.
type Error struct {
	// Code is the stable, machine-readable identifier clients branch on.
	Code string
	// Kind classifies the failure for transport mapping.
	Kind Kind
	// Message is the client-safe text; it must never carry internal detail.
	Message string
	// Details carries optional structured context the client acts on. Shared
	// sentinels are immutable; attach details via WithDetails, which copies..
	Details any
}

func (e *Error) Error() string { return e.Message }

// WithDetails returns a copy carrying details; the shared sentinel is never
// mutated, so concurrent requests stay safe.
func (e *Error) WithDetails(details any) *Error {
	c := *e
	c.Details = details
	return &c
}

// NewError builds a resource-specific domain error. Declare the result as a
// package-level var so callers can compare it with errors.Is.
func NewError(code string, kind Kind, message string) *Error {
	return &Error{Code: code, Kind: kind, Message: message}
}

// NewValidationError builds a KindInvalid error carrying per-field messages.
// The HTTP layer renders the map as the response's "fields" object.
func NewValidationError(fields map[string]string) *Error {
	return &Error{
		Code:    "VALIDATION_FAILED",
		Kind:    KindInvalid,
		Message: "validation failed",
		Details: fields,
	}
}

// Errors shared by more than one resource. Anything whose meaning is specific
// to one resource belongs in that resource's package instead.
var (
	ErrUnauthenticated = NewError("UNAUTHENTICATED", KindUnauthenticated, "invalid credentials")
	ErrForbidden       = NewError("FORBIDDEN", KindForbidden, "forbidden")
	ErrNotFound        = NewError("NOT_FOUND", KindNotFound, "resource not found")
	ErrConflict        = NewError("CONFLICT", KindConflict, "conflicts with existing data")
	ErrInternal        = NewError("INTERNAL", KindInternal, "internal server error")
)
