package user

import (
	"time"

	usersvc "github.com/GtFoBAE05/go-boilerplate/internal/service/user"
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
	UpdatedAt time.Time `json:"updated_at"`
}

// ListItemResponse is the compact representation used inside list results.
type ListItemResponse struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// One mapper per service Output. The three below are identical today; keeping
// them apart is what lets create, get and update change their response
// separately later, without one accidentally changing the other two.

func createToDetailResponse(u usersvc.CreateOutput) DetailResponse {
	return DetailResponse{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

func getToDetailResponse(u usersvc.GetOutput) DetailResponse {
	return DetailResponse{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

func updateToDetailResponse(u usersvc.UpdateOutput) DetailResponse {
	return DetailResponse{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

func toListItemResponses(users []usersvc.ListOutput) []ListItemResponse {
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
