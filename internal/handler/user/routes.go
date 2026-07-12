package user

import "github.com/go-chi/chi/v5"

// Routes returns the /users sub-router, mounted under /api/v1 by the server.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.create)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Patch("/{id}", h.update)
	r.Delete("/{id}", h.delete)
	return r
}
