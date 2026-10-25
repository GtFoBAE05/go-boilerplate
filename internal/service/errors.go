// Package service holds shared business-logic pieces: the domain error type and
// its transport-neutral kinds. It re-exports platform/errors so call sites read
// service.NewError; handler.WriteServiceError maps Kind to HTTP. Resource-
// specific errors belong in that resource's package, not here.
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
