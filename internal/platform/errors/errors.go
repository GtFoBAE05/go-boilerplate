// Package errors provides the domain error type and the errors shared by every
// resource. A resource's own failures are declared with NewError in that
// resource's service package, never added here.
//
// A domain error carries a transport-neutral Kind. The HTTP layer maps Kind to
// a status code, which keeps this package importable by a future gRPC or CLI
// consumer without dragging net/http along.
package errors

// Kind classifies a domain error. It is the only field the transport layer
// needs to choose a status code; the Code string is what clients branch on.
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
	// KindInternal means an unexpected server-side failure. Its detail must
	// never reach the client.
	KindInternal Kind = "internal"
)

// Error is a domain error. It knows what went wrong and how to classify it,
// but not which HTTP status a particular transport should use.
type Error struct {
	// Code is the stable, machine-readable identifier clients branch on.
	Code string
	// Kind classifies the failure for transport mapping.
	Kind Kind
	// Message is the client-safe text; it must never carry internal detail.
	Message string
	// Details carries optional structured context the client acts on (e.g.
	// the rules blocking a member removal, under "rules"). Package-level
	// sentinels are shared pointers, so details are attached via WithDetails,
	// which returns a copy - never by mutating a sentinel.
	Details any
}

func (e *Error) Error() string { return e.Message }

// WithDetails returns a copy of e carrying details. The shared sentinel the
// method is called on is never mutated, so concurrent requests stay safe:
//
//	return ErrOrderLocked.WithDetails(map[string]any{"rules": rules})
func (e *Error) WithDetails(details any) *Error {
	c := *e
	c.Details = details
	return &c
}

// NewError builds a domain error for a resource-specific failure. Declare the
// result as a package-level var so callers can compare it with errors.Is:
//
//	var ErrOrderLocked = service.NewError("ORDER_LOCKED", service.KindConflict, "order is locked")
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
