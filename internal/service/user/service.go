// Package user implements the user business logic: cache-aside reads,
// transactional writes, and storage-error → domain-error mapping.
//
// Consumers import it aliased to avoid clashing with handler/user:
//
//	usersvc "go-boilerplate/internal/service/user"
package user

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"go-boilerplate/internal/platform/cache"
	"go-boilerplate/internal/repository"
	"go-boilerplate/internal/service"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Service is the user business-logic contract consumed by handlers.
type Service interface {
	Create(ctx context.Context, in CreateInput) (User, error)
	Get(ctx context.Context, id int64) (User, error)
	List(ctx context.Context, limit, offset int32) ([]User, error)
	Update(ctx context.Context, id int64, in UpdateInput) (User, error)
	Delete(ctx context.Context, id int64) error
}

type svc struct {
	repo   repository.Store
	cache  cache.Cache
	log    *slog.Logger
	tracer trace.Tracer
}

var _ Service = (*svc)(nil)

// New builds the user Service; pass a nil cache to run uncached.
func New(repo repository.Store, c cache.Cache, log *slog.Logger) Service {
	return &svc{
		repo:   repo,
		cache:  c,
		log:    log,
		tracer: otel.Tracer("service/user"),
	}
}

func cacheKey(id int64) string { return fmt.Sprintf("user:%d", id) }

func (s *svc) Create(ctx context.Context, in CreateInput) (User, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.Create",
		trace.WithAttributes(attribute.String("user.email", in.Email)),
	)
	defer span.End()

	user, err := s.repo.CreateUser(ctx, repository.CreateUserParams{Name: in.Name, Email: in.Email})
	if err != nil {
		if service.IsUniqueViolation(err) {
			span.RecordError(err)
			span.SetStatus(codes.Error, "email already taken")
			return User{}, service.ErrEmailTaken
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "create user failed")
		return User{}, fmt.Errorf("create user: %w", err)
	}
	span.SetAttributes(attribute.Int64("user.id", user.ID))
	return toUser(user), nil
}

func (s *svc) Get(ctx context.Context, id int64) (User, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.Get",
		trace.WithAttributes(attribute.Int64("user.id", id)),
	)
	defer span.End()

	var user User
	if s.cache != nil {
		hit, err := s.cache.Get(ctx, cacheKey(id), &user)
		if err != nil {
			// Cache failure is not fatal; fall through to the database.
			s.log.WarnContext(ctx, "cache read failed", "error", err)
		} else if hit {
			span.SetAttributes(attribute.Bool("cache.hit", true))
			return user, nil
		}
	}
	span.SetAttributes(attribute.Bool("cache.hit", false))

	row, err := s.repo.GetUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		span.SetStatus(codes.Error, "user not found")
		return User{}, service.ErrNotFound
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "get user failed")
		return User{}, fmt.Errorf("get user %d: %w", id, err)
	}
	user = toUser(row)

	if s.cache != nil {
		if err := s.cache.Set(ctx, cacheKey(id), user); err != nil {
			s.log.WarnContext(ctx, "cache write failed", "error", err)
		}
	}
	return user, nil
}

func (s *svc) List(ctx context.Context, limit, offset int32) ([]User, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.List",
		trace.WithAttributes(
			attribute.Int("db.limit", int(limit)),
			attribute.Int("db.offset", int(offset)),
		),
	)
	defer span.End()

	users, err := s.repo.ListUsers(ctx, repository.ListUsersParams{Limit: limit, Offset: offset})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "list users failed")
		return nil, fmt.Errorf("list users: %w", err)
	}
	span.SetAttributes(attribute.Int("db.result_count", len(users)))
	return toUsers(users), nil
}

func (s *svc) Update(ctx context.Context, id int64, in UpdateInput) (User, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.Update",
		trace.WithAttributes(attribute.Int64("user.id", id)),
	)
	defer span.End()

	// Read-modify-write inside one transaction: FOR UPDATE locks the row so
	// two concurrent partial updates can't overwrite each other's fields.
	var updated repository.User
	err := s.repo.ExecTx(ctx, func(q repository.Querier) error {
		current, err := q.GetUserForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return service.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock user %d: %w", id, err)
		}

		params := repository.UpdateUserParams{ID: id, Name: current.Name, Email: current.Email}
		if in.Name != nil {
			params.Name = *in.Name
		}
		if in.Email != nil {
			params.Email = *in.Email
		}

		updated, err = q.UpdateUser(ctx, params)
		if err != nil {
			if service.IsUniqueViolation(err) {
				return service.ErrEmailTaken
			}
			return fmt.Errorf("update user %d: %w", id, err)
		}
		return nil
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "update user failed")
		return User{}, err
	}

	// Invalidate-on-write: drop the stale entry, the next read repopulates.
	if s.cache != nil {
		if err := s.cache.Delete(ctx, cacheKey(id)); err != nil {
			s.log.WarnContext(ctx, "cache invalidation failed", "error", err)
		}
	}
	return toUser(updated), nil
}

func (s *svc) Delete(ctx context.Context, id int64) error {
	ctx, span := s.tracer.Start(ctx, "UserService.Delete",
		trace.WithAttributes(attribute.Int64("user.id", id)),
	)
	defer span.End()

	rows, err := s.repo.DeleteUser(ctx, id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "delete user failed")
		return fmt.Errorf("delete user %d: %w", id, err)
	}
	if rows == 0 {
		// Report the miss instead of a silent 204, so a caller deleting the
		// wrong id finds out. Costs idempotency: a repeated DELETE 404s.
		span.SetStatus(codes.Error, "user not found")
		return service.ErrNotFound
	}
	if s.cache != nil {
		if err := s.cache.Delete(ctx, cacheKey(id)); err != nil {
			s.log.WarnContext(ctx, "cache invalidation failed", "error", err)
		}
	}
	return nil
}
