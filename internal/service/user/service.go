// Package user implements the user business logic: transactional writes and
// storage-error → domain-error mapping.
//
// Consumers import it aliased to avoid clashing with handler/user:
//
//	usersvc "github.com/GtFoBAE05/go-boilerplate/internal/service/user"
package user

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/GtFoBAE05/go-boilerplate/internal/repository"
	"github.com/GtFoBAE05/go-boilerplate/internal/service"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ErrEmailTaken is this resource's failure, not a shared one: only user writes
// enforce a unique email. Declared here so the shared error file never grows a
// case per resource.
var ErrEmailTaken = service.NewError("EMAIL_TAKEN", service.KindConflict, "email already taken")

// UserService is the user business-logic contract consumed by handlers.
type UserService interface {
	Create(ctx context.Context, in CreateInput) (CreateOutput, error)
	Get(ctx context.Context, in GetInput) (GetOutput, error)
	List(ctx context.Context, in ListInput) ([]ListOutput, error)
	Update(ctx context.Context, in UpdateInput) (UpdateOutput, error)
	Delete(ctx context.Context, in DeleteInput) error
}

type userService struct {
	repo   repository.Store
	log    *slog.Logger
	tracer trace.Tracer
}

// NewUserService builds the user service.
func NewUserService(repo repository.Store, log *slog.Logger) UserService {
	return &userService{
		repo:   repo,
		log:    log,
		tracer: otel.Tracer("service/user"),
	}
}

// isEmailConflict distinguishes the email rule from any other unique index on
// users. The constraint name is a storage detail, so the inspection lives in
// repository and the service only asks which rule fired.
func isEmailConflict(err error) bool {
	return repository.IsUniqueViolation(err) &&
		strings.Contains(repository.ConstraintName(err), "email")
}

func (s *userService) Create(ctx context.Context, in CreateInput) (CreateOutput, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.Create")
	defer span.End()

	in.Normalize()
	if err := in.Validate(); err != nil {
		span.SetStatus(codes.Error, "validation failed")
		return CreateOutput{}, err
	}

	user, err := s.repo.CreateUser(ctx, repository.CreateUserParams{Name: in.Name, Email: in.Email})
	if err != nil {
		if isEmailConflict(err) {
			span.RecordError(err)
			span.SetStatus(codes.Error, "email already taken")
			return CreateOutput{}, ErrEmailTaken
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "create user failed")
		return CreateOutput{}, fmt.Errorf("create user: %w", err)
	}
	span.SetAttributes(attribute.Int64("user.id", user.ID))
	s.log.DebugContext(ctx, "user created", "user_id", user.ID)
	return toCreateOutput(user), nil
}

func (s *userService) Get(ctx context.Context, in GetInput) (GetOutput, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.Get",
		trace.WithAttributes(attribute.Int64("user.id", in.ID)),
	)
	defer span.End()

	row, err := s.repo.GetUser(ctx, in.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		span.SetStatus(codes.Error, "user not found")
		return GetOutput{}, service.ErrNotFound
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "get user failed")
		return GetOutput{}, fmt.Errorf("get user %d: %w", in.ID, err)
	}
	return toGetOutput(row), nil
}

func (s *userService) List(ctx context.Context, in ListInput) ([]ListOutput, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.List",
		trace.WithAttributes(
			attribute.Int("db.limit", int(in.Limit)),
			attribute.Int("db.offset", int(in.Offset)),
		),
	)
	defer span.End()

	users, err := s.repo.ListUsers(ctx, repository.ListUsersParams{Limit: in.Limit, Offset: in.Offset})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "list users failed")
		return nil, fmt.Errorf("list users: %w", err)
	}
	span.SetAttributes(attribute.Int("db.result_count", len(users)))
	return toListOutputs(users), nil
}

func (s *userService) Update(ctx context.Context, in UpdateInput) (UpdateOutput, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.Update",
		trace.WithAttributes(attribute.Int64("user.id", in.ID)),
	)
	defer span.End()

	in.Normalize()
	if err := in.Validate(); err != nil {
		span.SetStatus(codes.Error, "validation failed")
		return UpdateOutput{}, err
	}

	// Read-modify-write inside one transaction: FOR UPDATE locks the row so
	// two concurrent partial updates can't overwrite each other's fields.
	var updated repository.User
	err := s.repo.ExecTx(ctx, func(q repository.Querier) error {
		current, err := q.GetUserForUpdate(ctx, in.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return service.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock user %d: %w", in.ID, err)
		}

		params := repository.UpdateUserParams{ID: in.ID, Name: current.Name, Email: current.Email}
		if in.Name != nil {
			params.Name = *in.Name
		}
		if in.Email != nil {
			params.Email = *in.Email
		}

		updated, err = q.UpdateUser(ctx, params)
		if err != nil {
			if isEmailConflict(err) {
				return ErrEmailTaken
			}
			return fmt.Errorf("update user %d: %w", in.ID, err)
		}
		return nil
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "update user failed")
		return UpdateOutput{}, err
	}

	s.log.DebugContext(ctx, "user updated", "user_id", updated.ID)
	return toUpdateOutput(updated), nil
}

func (s *userService) Delete(ctx context.Context, in DeleteInput) error {
	ctx, span := s.tracer.Start(ctx, "UserService.Delete",
		trace.WithAttributes(attribute.Int64("user.id", in.ID)),
	)
	defer span.End()

	rows, err := s.repo.DeleteUser(ctx, in.ID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "delete user failed")
		return fmt.Errorf("delete user %d: %w", in.ID, err)
	}
	if rows == 0 {
		// Report the miss instead of a silent 204, so a caller deleting the
		// wrong id finds out. Costs idempotency: a repeated DELETE 404s.
		span.SetStatus(codes.Error, "user not found")
		return service.ErrNotFound
	}
	s.log.DebugContext(ctx, "user deleted", "user_id", in.ID)
	return nil
}
