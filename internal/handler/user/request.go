package user

// CreateRequest is the POST /users payload; all fields are required.
type CreateRequest struct {
	Name  string `json:"name"  validate:"required,min=1,max=100"`
	Email string `json:"email" validate:"required,email"`
}

// UpdateRequest is a partial update: omitted fields keep their current
// value. Validation rules only apply to fields that are present.
type UpdateRequest struct {
	Name  *string `json:"name"  validate:"omitempty,min=1,max=100"`
	Email *string `json:"email" validate:"omitempty,email"`
}
