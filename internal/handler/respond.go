// Package handler holds HTTP plumbing shared by all resource handlers:
// the response envelope, error mapping, request validation.
// Resource handlers live in subpackages (handler/user, ...).
//
// Every API response has exactly one shape, enforced here:
//
//	success: {"data": ..., "meta": {...}?}
//	failure: {"error": {"code": "...", "message": "...", "fields": {...}?}}
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"go-boilerplate/internal/service"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Machine-readable error codes; clients branch on these, never on message.
const (
	CodeBadRequest       = "BAD_REQUEST"
	CodeValidationFailed = "VALIDATION_FAILED"
	CodeNotFound         = "NOT_FOUND"
	CodeMethodNotAllowed = "METHOD_NOT_ALLOWED"
	CodePayloadTooLarge  = "PAYLOAD_TOO_LARGE"
	CodeEmailTaken       = "EMAIL_TAKEN"
	CodeRateLimited      = "RATE_LIMITED"
	CodeInternal         = "INTERNAL"
)

// Meta carries list pagination info.
type Meta struct {
	Limit  int32 `json:"limit"`
	Offset int32 `json:"offset"`
}

type successEnvelope struct {
	Data any   `json:"data"`
	Meta *Meta `json:"meta,omitempty"`
}

type errorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

// WriteJSON writes v as-is, without the envelope. Reserved for
// infrastructure endpoints (health probes); API handlers use OK/OKList.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// OK writes a success envelope: {"data": ...}.
func OK(w http.ResponseWriter, status int, data any) {
	WriteJSON(w, status, successEnvelope{Data: data})
}

// OKList writes a success envelope with pagination meta:
// {"data": [...], "meta": {...}}.
func OKList(w http.ResponseWriter, data any, meta Meta) {
	WriteJSON(w, http.StatusOK, successEnvelope{Data: data, Meta: &meta})
}

// WriteError writes an error envelope: {"error": {"code", "message"}}.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message}})
}

// writeValidationError adds per-field messages; used by DecodeValid.
func writeValidationError(w http.ResponseWriter, fields map[string]string) {
	WriteJSON(w, http.StatusUnprocessableEntity, errorEnvelope{Error: errorBody{
		Code:    CodeValidationFailed,
		Message: "validation failed",
		Fields:  fields,
	}})
}

// WriteServiceError maps domain errors to HTTP status + error code; anything
// unmapped is logged and returned as a 500 without leaking internals.
// It also marks the active span as errored so traces reflect HTTP-level failures.
func WriteServiceError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	span := trace.SpanFromContext(r.Context())
	switch {
	case errors.Is(err, service.ErrNotFound):
		span.SetStatus(codes.Error, "not found")
		WriteError(w, http.StatusNotFound, CodeNotFound, "resource not found")
	case errors.Is(err, service.ErrEmailTaken):
		span.SetStatus(codes.Error, "email taken")
		WriteError(w, http.StatusConflict, CodeEmailTaken, "email already taken")
	default:
		span.RecordError(err)
		span.SetStatus(codes.Error, "internal error")
		log.ErrorContext(r.Context(), "unhandled service error", "error", err)
		WriteError(w, http.StatusInternalServerError, CodeInternal, "internal server error")
	}
}
