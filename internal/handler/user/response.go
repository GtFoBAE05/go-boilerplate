package user

import (
	"time"

	usersvc "go-boilerplate/internal/service/user"
)

// One response type per representation, so each operation's shape can
// evolve independently. All are decoupled from the domain model: internal
// changes don't leak into the API contract.

// DetailResponse is the full representation, returned by create, get and update.
type DetailResponse struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

// ListItemResponse is the compact representation used inside list results.
type ListItemResponse struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func toDetailResponse(u usersvc.User) DetailResponse {
	return DetailResponse{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		CreatedAt: u.CreatedAt,
	}
}

func toListItemResponses(users []usersvc.User) []ListItemResponse {
	out := make([]ListItemResponse, 0, len(users))
	for _, u := range users {
		out = append(out, ListItemResponse{
			ID:    u.ID,
			Name:  u.Name,
			Email: u.Email,
		})
	}
	return out
}
