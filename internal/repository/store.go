// Hand-written companion to the sqlc output; `make sqlc` never touches it.
package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const pgUniqueViolation = "23505"

// IsUniqueViolation reports whether err is a Postgres unique-constraint
// violation. Storage-specific error inspection belongs here, not in a service:
// a service should not import pgconn just to turn a duplicate key into a
// domain error.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}

// ConstraintName returns the constraint/index name on a Postgres error, or ""
// when the error is not a PgError. Callers use it to distinguish which unique
// rule fired (email vs. username) without matching error strings.
func ConstraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

// Store is what services depend on: every generated query plus transaction
// support.
type Store interface {
	Querier
	// ExecTx runs fn inside a transaction. Every query made through fn's
	// Querier goes through that transaction; an error from fn rolls back,
	// otherwise the transaction commits.
	ExecTx(ctx context.Context, fn func(Querier) error) error
}

type store struct {
	*Queries
	pool *pgxpool.Pool
}

var _ Store = (*store)(nil)

// NewStore builds a Store backed by pool.
func NewStore(pool *pgxpool.Pool) Store {
	return &store{Queries: New(pool), pool: pool}
}

func (s *store) ExecTx(ctx context.Context, fn func(Querier) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	// Rollback after a successful commit is a harmless no-op.
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(s.Queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
