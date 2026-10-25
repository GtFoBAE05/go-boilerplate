package user

import "strings"

// Normalize canonicalizes the payload before validation sees it. DecodeValid
// calls it, so the whitespace-only name that would otherwise pass `min=1` and
// be stored as "" is rejected instead.
func (r *CreateRequest) Normalize() {
	r.Name = strings.TrimSpace(r.Name)
	r.Email = strings.TrimSpace(r.Email)
}

// Normalize trims the fields that are present and leaves absent ones nil, so
// the handler can still tell "not provided" from "explicitly cleared".
func (r *UpdateRequest) Normalize() {
	trim(r.Name)
	trim(r.Email)
}

func trim(s *string) {
	if s != nil {
		*s = strings.TrimSpace(*s)
	}
}

// CreateRequest is the POST /users payload; all fields are required.
type CreateRequest struct {
	Name  string `json:"name"  validate:"required,min=1,max=100"`
	Email string `json:"email" validate:"required,email,max=320"`
}

// UpdateRequest is a partial update: omitted fields keep their current
// value. Validation rules only apply to fields that are present.
type UpdateRequest struct {
	Name  *string `json:"name"  validate:"omitempty,min=1,max=100"`
	Email *string `json:"email" validate:"omitempty,email,max=320"`
}
