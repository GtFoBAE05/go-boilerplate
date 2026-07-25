package user

import (
	"time"

	"go-boilerplate/internal/repository"
)

// User is the domain model. Handlers and cache entries see this type, not
// repository.User, so schema changes stop at the service boundary.
type User struct {
	ID        int64
	Name      string
	Email     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func toUser(u repository.User) User {
	return User{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

func toUsers(users []repository.User) []User {
	out := make([]User, 0, len(users))
	for _, u := range users {
		out = append(out, toUser(u))
	}
	return out
}
