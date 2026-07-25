// Hand-written companion to the sqlc output; `make sqlc` never touches it.
package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

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
