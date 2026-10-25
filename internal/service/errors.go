// Package service holds the pieces shared by every resource's business logic:
// the domain error type and storage-error helpers. Each resource's logic lives
// in its own subpackage (service/user, ...), exposing an interface plus an
// unexported implementation so handlers can fake the contract in tests.
//
// Domain errors are self-describing: each carries the HTTP status and
// machine-readable code it maps to. handler.WriteServiceError reads those
// fields, so adding a resource never means editing the shared handler layer.
//
// This package is a thin re-export of platform/errors, not a second error
// model. It exists so call sites read service.NewError and service.ErrNotFound
// while the implementation stays in the package that cannot import back into
// the application. A resource-specific error belongs in that resource's
// package - do not add it here.
package service

import (
	"github.com/GtFoBAE05/go-boilerplate/internal/platform/errors"
)

// Error is a domain error that knows how it surfaces over HTTP.
type Error = errors.Error

// NewError builds a domain error for a resource-specific failure.
var NewError = errors.NewError

var (
	// ErrUnauthenticated reports invalid credentials.
	ErrUnauthenticated = errors.ErrUnauthenticated
	// ErrForbidden reports a caller that is known but not allowed.
	ErrForbidden = errors.ErrForbidden
	// ErrNotFound reports a missing resource; handlers map it to 404.
	ErrNotFound = errors.ErrNotFound
	// ErrConflict reports a write that conflicts with existing data.
	ErrConflict = errors.ErrConflict
	// ErrInternal reports a failure whose detail must not reach the client.
	ErrInternal = errors.ErrInternal
)

// IsUniqueViolation reports whether err is a Postgres unique-constraint
// violation.
var IsUniqueViolation = errors.IsUniqueViolation
