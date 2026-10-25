// Package user exposes the /users resource over HTTP. Handlers stay thin:
// decode+validate (DTO tags), call the service, map the result.
package user

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/GtFoBAE05/go-boilerplate/internal/handler"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	usersvc "github.com/GtFoBAE05/go-boilerplate/internal/service/user"
)

var tracer = otel.Tracer("handler/user")

// Handler serves the /users resource; see Routes for the endpoint list.
type Handler struct {
	svc usersvc.UserService
	log *slog.Logger
}

// NewHandler wires the user service into the HTTP handler.
func NewHandler(svc usersvc.UserService, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "UserHandler.create")
	defer span.End()
	r = r.WithContext(ctx)

	req, ok := handler.DecodeValid[CreateRequest](w, r)
	if !ok {
		span.SetStatus(codes.Error, "decode/validate failed")
		return
	}

	u, err := h.svc.Create(ctx, usersvc.CreateInput{
		Name:  req.Name,
		Email: req.Email,
	})
	if err != nil {
		handler.WriteServiceError(w, r, h.log, err)
		return
	}
	span.SetAttributes(attribute.Int64("user.id", u.ID))
	handler.OK(w, http.StatusCreated, createToDetailResponse(u))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "UserHandler.get")
	defer span.End()
	r = r.WithContext(ctx)

	id, err := pathID(r)
	if err != nil {
		span.SetStatus(codes.Error, "invalid id")
		handler.WriteError(w, http.StatusBadRequest, handler.CodeBadRequest, "id must be an integer")
		return
	}
	span.SetAttributes(attribute.Int64("user.id", id))

	u, err := h.svc.Get(ctx, id)
	if err != nil {
		handler.WriteServiceError(w, r, h.log, err)
		return
	}
	handler.OK(w, http.StatusOK, getToDetailResponse(u))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "UserHandler.list")
	defer span.End()
	r = r.WithContext(ctx)

	page, ok := handler.DecodePage(w, r)
	if !ok {
		span.SetStatus(codes.Error, "invalid pagination")
		return
	}
	span.SetAttributes(
		attribute.Int("query.limit", int(page.Limit)),
		attribute.Int("query.offset", int(page.Offset)),
	)

	users, err := h.svc.List(ctx, page.Limit, page.Offset)
	if err != nil {
		handler.WriteServiceError(w, r, h.log, err)
		return
	}
	handler.OKList(w, toListItemResponses(users), page.Meta())
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "UserHandler.update")
	defer span.End()
	r = r.WithContext(ctx)

	id, err := pathID(r)
	if err != nil {
		span.SetStatus(codes.Error, "invalid id")
		handler.WriteError(w, http.StatusBadRequest, handler.CodeBadRequest, "id must be an integer")
		return
	}
	span.SetAttributes(attribute.Int64("user.id", id))

	req, ok := handler.DecodeValid[UpdateRequest](w, r)
	if !ok {
		span.SetStatus(codes.Error, "decode/validate failed")
		return
	}
	if req.Name == nil && req.Email == nil {
		span.SetStatus(codes.Error, "no fields to update")
		handler.WriteError(w, http.StatusUnprocessableEntity, handler.CodeValidationFailed,
			"at least one of name or email must be provided")
		return
	}

	u, err := h.svc.Update(ctx, id, usersvc.UpdateInput{Name: req.Name, Email: req.Email})
	if err != nil {
		handler.WriteServiceError(w, r, h.log, err)
		return
	}
	handler.OK(w, http.StatusOK, updateToDetailResponse(u))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "UserHandler.delete",
		trace.WithAttributes(attribute.String("http.method", "DELETE")),
	)
	defer span.End()
	r = r.WithContext(ctx)

	id, err := pathID(r)
	if err != nil {
		span.SetStatus(codes.Error, "invalid id")
		handler.WriteError(w, http.StatusBadRequest, handler.CodeBadRequest, "id must be an integer")
		return
	}
	span.SetAttributes(attribute.Int64("user.id", id))

	if err := h.svc.Delete(ctx, id); err != nil {
		handler.WriteServiceError(w, r, h.log, err)
		return
	}
	handler.NoContent(w)
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}
