// Package service holds the pieces shared by every resource's business logic:
// the domain error type and its transport-neutral kinds. Each resource's logic
// lives in its own subpackage (service/user, ...), exposing an interface plus
// an unexported implementation so handlers can fake the contract in tests.
//
// Domain errors are self-describing: each carries a machine-readable code and
// a Kind. handler.WriteServiceError maps Kind to HTTP, so adding a resource
// never means editing the shared handler layer and a future gRPC adapter can
// map the same errors without a second set of service rules.
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

// Error is a domain error.
type Error = errors.Error

// Kind classifies a domain error for transport mapping.
type Kind = errors.Kind

const (
	// KindInvalid is a client-supplied value that failed a domain invariant.
	KindInvalid = errors.KindInvalid
	// KindUnauthenticated means credentials are missing or wrong.
	KindUnauthenticated = errors.KindUnauthenticated
	// KindForbidden means the caller is known but not allowed.
	KindForbidden = errors.KindForbidden
	// KindNotFound means the resource does not exist.
	KindNotFound = errors.KindNotFound
	// KindConflict means the write conflicts with existing state.
	KindConflict = errors.KindConflict
	// KindInternal means an unexpected server-side failure.
	KindInternal = errors.KindInternal
)

// NewError builds a domain error for a resource-specific failure.
var NewError = errors.NewError

// NewValidationError builds a validation error carrying per-field messages.
var NewValidationError = errors.NewValidationError

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
