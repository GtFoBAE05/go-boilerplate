// Package service holds cross-resource service bits: the domain error type
// and storage-error helpers. Each resource's business logic lives in its own
// subpackage (service/user, ...), exposing an interface plus an unexported
// implementation so handlers can mock the contract in tests.
//
// Domain errors are self-describing: each carries the HTTP status and
// machine-readable code it maps to. handler.WriteServiceError reads those
// fields, so adding a resource never means editing the shared handler layer.
package service

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
// service — importing back would be a cycle.
type Error struct {
	// Code is the stable, machine-readable identifier clients branch on.
	Code string
	// Status is the HTTP status WriteServiceError responds with.
	Status int
	// Message is the client-safe text; it must never carry internal detail.
	Message string
}

func (e *Error) Error() string { return e.Message }

// NewError builds a domain error for a resource-specific failure. Declare the
// result as a package-level var so callers can compare it with errors.Is:
//
//	var ErrOrderLocked = service.NewError("ORDER_LOCKED", http.StatusConflict, "order is locked")
func NewError(code string, status int, message string) *Error {
	return &Error{Code: code, Status: status, Message: message}
}

// Domain errors shared across resources. Resource-specific ones belong in the
// resource's own service package.
var (
	// ErrNotFound means the requested resource does not exist (HTTP 404).
	ErrNotFound = NewError("NOT_FOUND", http.StatusNotFound, "resource not found")
	// ErrEmailTaken means a unique-email constraint was violated (HTTP 409).
	ErrEmailTaken = NewError("EMAIL_TAKEN", http.StatusConflict, "email already taken")
)

const pgUniqueViolation = "23505"

// IsUniqueViolation reports whether err is a Postgres unique-constraint
// violation (duplicate key).
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
