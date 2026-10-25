//go:build integration

// This is the one exemplary integration test in the repo: copy it as the
// template for testing a new resource against a real Postgres. It spins
// postgres:16-alpine via testcontainers, applies the goose migrations (so it
// also guards that migrations still create a working schema), and exercises a
// full CRUD vertical through the sqlc Store.
//
// Guarded by the `integration` build tag so `go test ./...` stays fast and
// Docker-free; run it with `make test-integration`.
package repository_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/GtFoBAE05/go-boilerplate/internal/platform/postgres"
	"github.com/GtFoBAE05/go-boilerplate/internal/repository"
	"github.com/GtFoBAE05/go-boilerplate/internal/service"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// setupPool starts a throwaway Postgres, migrates it, and returns a live pool.
// Registered cleanups terminate the container and close the pool.
func setupPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("app"),
		tcpostgres.WithUsername("app"),
		tcpostgres.WithPassword("app"),
		// The module does not apply its readiness wait by default, and the
		// old WithWaitStrategy override replaced whatever was there. Wait for
		// the ready log twice plus the host port, or the pool pings a server
		// that is still starting.
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	// Reuse the production constructor so the otelpgx tracer is exercised too.
	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)

	// Reuse the production migration path — this doubles as a migration test.
	if err := postgres.Migrate(ctx, pool, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

func TestUserRepositoryCRUD(t *testing.T) {
	ctx := context.Background()
	store := repository.NewStore(setupPool(t))

	// Create
	created, err := store.CreateUser(ctx, repository.CreateUserParams{
		Name:  "alice",
		Email: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 || created.Name != "alice" || created.Email != "alice@example.com" {
		t.Fatalf("unexpected created row: %+v", created)
	}
	if created.CreatedAt.IsZero() {
		t.Fatalf("created_at not populated")
	}

	// Get by id
	got, err := store.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != created.ID || got.Email != created.Email {
		t.Fatalf("get mismatch: got %+v want %+v", got, created)
	}

	// Duplicate email violates the UNIQUE constraint, and the raw storage error
	// must map to the domain error the service layer relies on.
	_, err = store.CreateUser(ctx, repository.CreateUserParams{
		Name:  "alice2",
		Email: "alice@example.com",
	})
	if err == nil {
		t.Fatalf("duplicate email: want error, got nil")
	}
	if !service.IsUniqueViolation(err) {
		t.Fatalf("duplicate email: want unique violation, got %v", err)
	}

	// List with limit/offset: add a second user, page through.
	second, err := store.CreateUser(ctx, repository.CreateUserParams{
		Name:  "bob",
		Email: "bob@example.com",
	})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	list, err := store.ListUsers(ctx, repository.ListUsersParams{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != second.ID {
		t.Fatalf("list limit/offset: want [%d], got %+v", second.ID, list)
	}

	// Update bumps updated_at past created_at, which is what lets a client
	// tell a modified row from a fresh one.
	updated, err := store.UpdateUser(ctx, repository.UpdateUserParams{
		ID:    created.ID,
		Name:  "alice renamed",
		Email: created.Email,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "alice renamed" {
		t.Fatalf("update: want renamed row, got %+v", updated)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("update: updated_at not advanced (created %s, updated %s)",
			created.UpdatedAt, updated.UpdatedAt)
	}

	// Delete reports rows affected, which is what the service turns into a 404
	// instead of a silent 204.
	rows, err := store.DeleteUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rows != 1 {
		t.Fatalf("delete: want 1 row affected, got %d", rows)
	}
	if _, err := store.GetUser(ctx, created.ID); err != pgx.ErrNoRows {
		t.Fatalf("get after delete: want pgx.ErrNoRows, got %v", err)
	}

	// Deleting again affects nothing; the service maps this to ErrNotFound.
	rows, err = store.DeleteUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("delete missing row: unexpected error %v", err)
	}
	if rows != 0 {
		t.Fatalf("delete missing row: want 0 rows affected, got %d", rows)
	}
}
