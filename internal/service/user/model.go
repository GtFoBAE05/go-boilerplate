package user

import (
	"time"

	"go-boilerplate/internal/repository"
)

// UserCache is what we save in Redis. Keep it separate from the Output types.
//
// If we saved GetOutput instead, adding a field to it later would break old
// data: Redis still holds the old JSON, it loads without any error, and the
// new field silently comes back empty. Nothing crashes, nothing logs — the
// user just gets wrong data.
type UserCache struct {
	ID        int64
	Name      string
	Email     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateInput holds everything Create needs. Need one more thing later? Add a
// field here — the method signature stays the same, so no caller breaks.
type CreateInput struct {
	Name  string
	Email string
}

// UpdateInput is a partial update: nil fields keep their current value.
type UpdateInput struct {
	Name  *string
	Email *string
}

// One Output per method. Yes, all four look identical right now — that is on
// purpose, so do not merge them into one shared type. The moment two endpoints
// share a struct, changing the response of one silently changes the other.
// Wait until a third method genuinely needs the same shape before merging.

// CreateOutput is what Create returns.
type CreateOutput struct {
	ID        int64
	Name      string
	Email     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// GetOutput is what Get returns.
type GetOutput struct {
	ID        int64
	Name      string
	Email     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ListOutput is one item in what List returns. It holds every field, even the
// ones the list endpoint does not show today, so adding a column to that view
// is a handler change and never touches the service.
type ListOutput struct {
	ID        int64
	Name      string
	Email     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// UpdateOutput is what Update returns.
type UpdateOutput struct {
	ID        int64
	Name      string
	Email     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func toUserCache(u repository.User) UserCache {
	return UserCache{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

func toCreateOutput(u repository.User) CreateOutput {
	return CreateOutput{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

// Takes UserCache, not repository.User, because Get often answers from Redis
// without touching the database at all.
//
// GetOutput(u) works because both structs have the exact same fields — Go
// allows that. Add a field to GetOutput and this stops compiling. When that
// happens, copy the fields over one by one. Do not "fix" it by editing
// UserCache to match: that changes what we store in Redis, which is the bug
// UserCache exists to prevent.
func toGetOutput(u UserCache) GetOutput {
	return GetOutput(u)
}

func toUpdateOutput(u repository.User) UpdateOutput {
	return UpdateOutput{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

func toListOutputs(users []repository.User) []ListOutput {
	out := make([]ListOutput, 0, len(users))
	for _, u := range users {
		out = append(out, ListOutput{
			ID:        u.ID,
			Name:      u.Name,
			Email:     u.Email,
			CreatedAt: u.CreatedAt,
			UpdatedAt: u.UpdatedAt,
		})
	}
	return out
}
