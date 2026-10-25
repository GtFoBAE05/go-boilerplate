package user

import (
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GtFoBAE05/go-boilerplate/internal/repository"
	"github.com/GtFoBAE05/go-boilerplate/internal/service"
)

// CreateInput holds everything Create needs. Need one more thing later? Add a
// field here — the method signature stays the same, so no caller breaks.
type CreateInput struct {
	Name  string
	Email string
}

// GetInput identifies one user.
type GetInput struct {
	ID int64
}

// ListInput carries the page window. Pagination bounds are a transport concern
// (handler.DecodePage), but the service still receives the validated values.
type ListInput struct {
	Limit  int32
	Offset int32
}

// UpdateInput is a partial update: nil fields keep their current value.
type UpdateInput struct {
	ID    int64
	Name  *string
	Email *string
}

// DeleteInput identifies one user.
type DeleteInput struct {
	ID int64
}

// Normalize canonicalizes the values that will be persisted. The handler does
// this before its DTO validation too; repeating it here is deliberate: the
// service is the last gate before the database, and a future gRPC caller will
// not pass through the HTTP DTO.
func (in *CreateInput) Normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Email = normalizeEmail(in.Email)
}

// Normalize trims the fields that are present and leaves absent ones nil, so
// the service can still tell "not provided" from "explicitly cleared".
func (in *UpdateInput) Normalize() {
	trim(in.Name)
	if in.Email != nil {
		normalized := normalizeEmail(*in.Email)
		in.Email = &normalized
	}
}

func trim(s *string) {
	if s != nil {
		*s = strings.TrimSpace(*s)
	}
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Validate enforces the user invariants that must hold no matter which
// transport called the service. The HTTP DTO tags are a fast, friendly first
// pass; this is the rule the database also enforces.
func (in CreateInput) Validate() error {
	return validateUserFields(in.Name, in.Email)
}

// Validate checks only the fields that are present.
func (in UpdateInput) Validate() error {
	if in.Name == nil && in.Email == nil {
		return service.NewValidationError(map[string]string{
			"body": "at least one of name or email must be provided",
		})
	}
	fields := map[string]string{}
	if in.Name != nil {
		if msg := validateName(*in.Name); msg != "" {
			fields["name"] = msg
		}
	}
	if in.Email != nil {
		if msg := validateEmail(*in.Email); msg != "" {
			fields["email"] = msg
		}
	}
	if len(fields) > 0 {
		return service.NewValidationError(fields)
	}
	return nil
}

func validateUserFields(name, email string) error {
	fields := map[string]string{}
	if msg := validateName(name); msg != "" {
		fields["name"] = msg
	}
	if msg := validateEmail(email); msg != "" {
		fields["email"] = msg
	}
	if len(fields) > 0 {
		return service.NewValidationError(fields)
	}
	return nil
}

func validateName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "is required"
	}
	if utf8.RuneCountInString(name) > 100 {
		return "must be at most 100 characters"
	}
	return ""
}

func validateEmail(email string) string {
	if strings.TrimSpace(email) == "" {
		return "is required"
	}
	if utf8.RuneCountInString(email) > 320 {
		return "must be at most 320 characters"
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "must be a valid email address"
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 || !strings.Contains(email[at+1:], ".") {
		return "must be a valid email address"
	}
	return ""
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

func toCreateOutput(u repository.User) CreateOutput {
	return CreateOutput{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

func toGetOutput(u repository.User) GetOutput {
	return GetOutput{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
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
