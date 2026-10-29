package user

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/GtFoBAE05/go-boilerplate/internal/repository"
	"github.com/GtFoBAE05/go-boilerplate/internal/service"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeStore implements repository.Store via configurable funcs. ExecTx just
// runs fn against the fake itself — no real transaction in unit tests.
type fakeStore struct {
	repository.Querier
	getUser          func(ctx context.Context, id int64) (repository.User, error)
	getUserForUpdate func(ctx context.Context, id int64) (repository.User, error)
	listUsers        func(ctx context.Context, arg repository.ListUsersParams) ([]repository.User, error)
	createUser       func(ctx context.Context, arg repository.CreateUserParams) (repository.User, error)
	updateUser       func(ctx context.Context, arg repository.UpdateUserParams) (repository.User, error)
	deleteUser       func(ctx context.Context, id int64) (int64, error)
}

var _ repository.Store = (*fakeStore)(nil)

func (f *fakeStore) GetUser(ctx context.Context, id int64) (repository.User, error) {
	return f.getUser(ctx, id)
}

func (f *fakeStore) GetUserForUpdate(ctx context.Context, id int64) (repository.User, error) {
	return f.getUserForUpdate(ctx, id)
}

func (f *fakeStore) ListUsers(ctx context.Context, arg repository.ListUsersParams) ([]repository.User, error) {
	return f.listUsers(ctx, arg)
}

func (f *fakeStore) CreateUser(ctx context.Context, arg repository.CreateUserParams) (repository.User, error) {
	return f.createUser(ctx, arg)
}

func (f *fakeStore) UpdateUser(ctx context.Context, arg repository.UpdateUserParams) (repository.User, error) {
	return f.updateUser(ctx, arg)
}

func (f *fakeStore) DeleteUser(ctx context.Context, id int64) (int64, error) {
	return f.deleteUser(ctx, id)
}

func (f *fakeStore) ExecTx(_ context.Context, fn func(repository.Querier) error) error {
	return fn(f)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestGetMapsNoRowsToErrNotFound(t *testing.T) {
	repo := &fakeStore{
		getUser: func(context.Context, int64) (repository.User, error) {
			return repository.User{}, pgx.ErrNoRows
		},
	}
	svc := NewUserService(repo, discardLogger())

	_, err := svc.Get(context.Background(), GetInput{ID: 42})
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestCreateMapsUniqueViolationToEmailTaken(t *testing.T) {
	repo := &fakeStore{
		createUser: func(context.Context, repository.CreateUserParams) (repository.User, error) {
			return repository.User{}, &pgconn.PgError{
				Code: "23505", ConstraintName: "users_email_lower_key",
			}
		},
	}
	svc := NewUserService(repo, discardLogger())

	_, err := svc.Create(context.Background(), CreateInput{Name: "alice", Email: "a@example.com"})
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("want ErrEmailTaken, got %v", err)
	}
}

// A different unique index whose name happens to contain "email" is not the
// email rule; matching a substring would turn an unrelated conflict into 409.
func TestCreateDoesNotMapOtherUniqueViolationToEmailTaken(t *testing.T) {
	repo := &fakeStore{
		createUser: func(context.Context, repository.CreateUserParams) (repository.User, error) {
			return repository.User{}, &pgconn.PgError{
				Code: "23505", ConstraintName: "users_email_key",
			}
		},
	}
	svc := NewUserService(repo, discardLogger())

	_, err := svc.Create(context.Background(), CreateInput{Name: "alice", Email: "a@example.com"})
	if errors.Is(err, ErrEmailTaken) {
		t.Fatalf("unrelated unique constraint mapped to ErrEmailTaken: %v", err)
	}
}

func TestCreateNormalizesBeforeWriting(t *testing.T) {
	var got repository.CreateUserParams
	repo := &fakeStore{
		createUser: func(_ context.Context, arg repository.CreateUserParams) (repository.User, error) {
			got = arg
			return repository.User{ID: 1, Name: arg.Name, Email: arg.Email}, nil
		},
	}
	svc := NewUserService(repo, discardLogger())

	if _, err := svc.Create(context.Background(), CreateInput{
		Name: "  Alice  ", Email: "  ALICE@Example.COM ",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "Alice" || got.Email != "alice@example.com" {
		t.Fatalf("service must normalize before writing, got %+v", got)
	}
}

func TestCreateRejectsInvalidInputBeforeStore(t *testing.T) {
	calls := 0
	repo := &fakeStore{
		createUser: func(context.Context, repository.CreateUserParams) (repository.User, error) {
			calls++
			return repository.User{}, nil
		},
	}
	svc := NewUserService(repo, discardLogger())

	_, err := svc.Create(context.Background(), CreateInput{Name: " ", Email: "not-an-email"})
	if err == nil {
		t.Fatal("want validation error, got nil")
	}
	var domainErr *service.Error
	if !errors.As(err, &domainErr) || domainErr.Kind != service.KindInvalid {
		t.Fatalf("want KindInvalid, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("store called %d times for invalid input", calls)
	}
}

func TestUpdateMapsUniqueViolationToEmailTaken(t *testing.T) {
	repo := &fakeStore{
		getUserForUpdate: func(_ context.Context, id int64) (repository.User, error) {
			return repository.User{ID: id, Name: "alice", Email: "a@example.com"}, nil
		},
		updateUser: func(context.Context, repository.UpdateUserParams) (repository.User, error) {
			return repository.User{}, &pgconn.PgError{
				Code: "23505", ConstraintName: "users_email_lower_key",
			}
		},
	}
	svc := NewUserService(repo, discardLogger())

	email := "b@example.com"
	_, err := svc.Update(context.Background(), UpdateInput{ID: 7, Email: &email})
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("want ErrEmailTaken, got %v", err)
	}
}

func TestListPropagatesStoreError(t *testing.T) {
	storeErr := errors.New("database unavailable")
	repo := &fakeStore{
		listUsers: func(context.Context, repository.ListUsersParams) ([]repository.User, error) {
			return nil, storeErr
		},
	}
	svc := NewUserService(repo, discardLogger())

	_, err := svc.List(context.Background(), ListInput{Limit: 10})
	if err == storeErr || !errors.Is(err, storeErr) {
		t.Fatalf("want wrapped store error, got %v", err)
	}
}

func TestUpdateMergesPartialInput(t *testing.T) {
	repo := &fakeStore{
		getUserForUpdate: func(_ context.Context, id int64) (repository.User, error) {
			return repository.User{ID: id, Name: "alice", Email: "a@example.com"}, nil
		},
		updateUser: func(_ context.Context, arg repository.UpdateUserParams) (repository.User, error) {
			return repository.User{ID: arg.ID, Name: arg.Name, Email: arg.Email}, nil
		},
	}
	svc := NewUserService(repo, discardLogger())

	name := "  bob  "
	user, err := svc.Update(context.Background(), UpdateInput{ID: 7, Name: &name})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.Name != "bob" {
		t.Fatalf("want updated name bob, got %q", user.Name)
	}
	if user.Email != "a@example.com" {
		t.Fatalf("want email kept from current row, got %q", user.Email)
	}
}

func TestUpdateRejectsEmptyInputBeforeTransaction(t *testing.T) {
	calls := 0
	repo := &fakeStore{
		getUserForUpdate: func(context.Context, int64) (repository.User, error) {
			calls++
			return repository.User{}, nil
		},
	}
	svc := NewUserService(repo, discardLogger())

	_, err := svc.Update(context.Background(), UpdateInput{ID: 7})
	if err == nil {
		t.Fatal("want validation error, got nil")
	}
	if calls != 0 {
		t.Fatalf("store called %d times for empty update", calls)
	}
}

func TestUpdateNormalizesEmail(t *testing.T) {
	var got repository.UpdateUserParams
	repo := &fakeStore{
		getUserForUpdate: func(_ context.Context, id int64) (repository.User, error) {
			return repository.User{ID: id, Name: "alice", Email: "a@example.com"}, nil
		},
		updateUser: func(_ context.Context, arg repository.UpdateUserParams) (repository.User, error) {
			got = arg
			return repository.User{ID: arg.ID, Name: arg.Name, Email: arg.Email}, nil
		},
	}
	svc := NewUserService(repo, discardLogger())

	email := "  NEW@Example.COM "
	if _, err := svc.Update(context.Background(), UpdateInput{ID: 7, Email: &email}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Email != "new@example.com" {
		t.Fatalf("want normalized email, got %q", got.Email)
	}
}

// Zero rows affected means the id never existed; the service reports that
// rather than letting the handler answer 204.
func TestDeleteMissingRowReturnsErrNotFound(t *testing.T) {
	repo := &fakeStore{
		deleteUser: func(context.Context, int64) (int64, error) { return 0, nil },
	}
	svc := NewUserService(repo, discardLogger())

	err := svc.Delete(context.Background(), DeleteInput{ID: 42})
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestUpdateNotFound(t *testing.T) {
	repo := &fakeStore{
		getUserForUpdate: func(context.Context, int64) (repository.User, error) {
			return repository.User{}, pgx.ErrNoRows
		},
	}
	svc := NewUserService(repo, discardLogger())

	name := "bob"
	_, err := svc.Update(context.Background(), UpdateInput{ID: 42, Name: &name})
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestValidationErrorMentionsField(t *testing.T) {
	err := CreateInput{Name: "alice", Email: "not-an-email"}.Validate()
	if err == nil {
		t.Fatal("want validation error, got nil")
	}
	var domainErr *service.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("want *service.Error, got %T", err)
	}
	fields, ok := domainErr.Details.(map[string]string)
	if !ok || !strings.Contains(fields["email"], "valid email") {
		t.Fatalf("want email field error, got %#v", domainErr.Details)
	}
}
