// Package errors provides the domain error type and the errors shared by every
// resource. A resource's own failures are declared with NewError in that
// resource's service package, never added here.
package errors

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

// Error is a domain error that knows how it surfaces over HTTP.
//
// Status and Code live here rather than in a switch inside the handler layer
// because the handler layer is shared by every resource: a per-resource switch
// would make each new vertical edit a common file. Code strings are duplicated
// from handler's generic codes instead of imported because handler imports
// service - importing back would be a cycle.
type Error struct {
	// Code is the stable, machine-readable identifier clients branch on.
	Code string
	// Status is the HTTP status WriteServiceError responds with.
	Status int
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
//	var ErrOrderLocked = service.NewError("ORDER_LOCKED", http.StatusConflict, "order is locked")
func NewError(code string, status int, message string) *Error {
	return &Error{Code: code, Status: status, Message: message}
}

// Errors shared by more than one resource. Anything whose meaning is specific
// to one resource belongs in that resource's package instead.
var (
	ErrUnauthenticated = NewError("UNAUTHENTICATED", http.StatusUnauthorized, "invalid credentials")
	ErrForbidden       = NewError("FORBIDDEN", http.StatusForbidden, "forbidden")
	ErrNotFound        = NewError("NOT_FOUND", http.StatusNotFound, "resource not found")
	ErrConflict        = NewError("CONFLICT", http.StatusConflict, "conflicts with existing data")
	ErrInternal        = NewError("INTERNAL", http.StatusInternalServerError, "internal server error")
)

const pgUniqueViolation = "23505"

// IsUniqueViolation reports whether err is a Postgres unique-constraint
// violation (duplicate key).
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
