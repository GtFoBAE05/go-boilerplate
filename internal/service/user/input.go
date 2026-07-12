package user

// CreateInput carries everything Create needs. Growing it later adds a
// field here instead of breaking the interface signature.
type CreateInput struct {
	Name  string
	Email string
}

// UpdateInput is a partial update: nil fields keep their current value.
type UpdateInput struct {
	Name  *string
	Email *string
}
