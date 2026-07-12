// Package service holds cross-resource service bits: domain errors and
// storage-error helpers. Each resource's business logic lives in its own
// subpackage (service/user, ...), exposing an interface plus an unexported
// implementation so handlers can mock the contract in tests.
//
// Domain errors stay here, shared, so handler.WriteServiceError maps them
// once for every resource.
package service

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// Domain errors returned by services; handler.WriteServiceError maps each to
// an HTTP status and machine-readable code.
var (
	// ErrNotFound means the requested resource does not exist (HTTP 404).
	ErrNotFound = errors.New("resource not found")
	// ErrEmailTaken means a unique-email constraint was violated (HTTP 409).
	ErrEmailTaken = errors.New("email already taken")
)

const pgUniqueViolation = "23505"

// IsUniqueViolation reports whether err is a Postgres unique-constraint
// violation (duplicate key).
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
