package user

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"go-boilerplate/internal/repository"
	"go-boilerplate/internal/service"

	"github.com/jackc/pgx/v5"
)

// fakeStore implements repository.Store via configurable funcs. ExecTx just
// runs fn against the fake itself — no real transaction in unit tests.
type fakeStore struct {
	repository.Querier
	getUser          func(ctx context.Context, id int64) (repository.User, error)
	getUserForUpdate func(ctx context.Context, id int64) (repository.User, error)
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

type fakeCache struct {
	store map[string][]byte
	sets  int
}

func (f *fakeCache) Get(_ context.Context, key string, _ any) (bool, error) {
	_, ok := f.store[key]
	return ok, nil
}

func (f *fakeCache) Set(_ context.Context, key string, _ any) error {
	f.sets++
	f.store[key] = []byte("cached")
	return nil
}

func (f *fakeCache) Delete(_ context.Context, keys ...string) error {
	for _, k := range keys {
		delete(f.store, k)
	}
	return nil
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
	svc := NewUserService(repo, nil, discardLogger())

	_, err := svc.Get(context.Background(), 42)
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestGetPopulatesCacheOnMiss(t *testing.T) {
	repo := &fakeStore{
		getUser: func(_ context.Context, id int64) (repository.User, error) {
			return repository.User{ID: id, Name: "alice", Email: "a@example.com"}, nil
		},
	}
	c := &fakeCache{store: map[string][]byte{}}
	svc := NewUserService(repo, c, discardLogger())

	user, err := svc.Get(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID != 7 {
		t.Fatalf("want user ID 7, got %d", user.ID)
	}
	if c.sets != 1 {
		t.Fatalf("want 1 cache set, got %d", c.sets)
	}
	if _, ok := c.store["user:7"]; !ok {
		t.Fatal("expected user:7 cached")
	}
}

func TestUpdateMergesPartialInputAndInvalidatesCache(t *testing.T) {
	repo := &fakeStore{
		getUserForUpdate: func(_ context.Context, id int64) (repository.User, error) {
			return repository.User{ID: id, Name: "alice", Email: "a@example.com"}, nil
		},
		updateUser: func(_ context.Context, arg repository.UpdateUserParams) (repository.User, error) {
			return repository.User{ID: arg.ID, Name: arg.Name, Email: arg.Email}, nil
		},
	}
	c := &fakeCache{store: map[string][]byte{"user:7": []byte("stale")}}
	svc := NewUserService(repo, c, discardLogger())

	name := "bob"
	user, err := svc.Update(context.Background(), 7, UpdateInput{Name: &name})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.Name != "bob" {
		t.Fatalf("want updated name bob, got %q", user.Name)
	}
	if user.Email != "a@example.com" {
		t.Fatalf("want email kept from current row, got %q", user.Email)
	}
	if _, ok := c.store["user:7"]; ok {
		t.Fatal("expected user:7 invalidated after update")
	}
}

// Zero rows affected means the id never existed; the service reports that
// rather than letting the handler answer 204.
func TestDeleteMissingRowReturnsErrNotFound(t *testing.T) {
	repo := &fakeStore{
		deleteUser: func(context.Context, int64) (int64, error) { return 0, nil },
	}
	c := &fakeCache{store: map[string][]byte{}}
	svc := NewUserService(repo, c, discardLogger())

	err := svc.Delete(context.Background(), 42)
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDeleteInvalidatesCache(t *testing.T) {
	repo := &fakeStore{
		deleteUser: func(context.Context, int64) (int64, error) { return 1, nil },
	}
	c := &fakeCache{store: map[string][]byte{"user:7": []byte("stale")}}
	svc := NewUserService(repo, c, discardLogger())

	if err := svc.Delete(context.Background(), 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := c.store["user:7"]; ok {
		t.Fatal("expected user:7 invalidated after delete")
	}
}

func TestUpdateNotFound(t *testing.T) {
	repo := &fakeStore{
		getUserForUpdate: func(context.Context, int64) (repository.User, error) {
			return repository.User{}, pgx.ErrNoRows
		},
	}
	svc := NewUserService(repo, nil, discardLogger())

	name := "bob"
	_, err := svc.Update(context.Background(), 42, UpdateInput{Name: &name})
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
