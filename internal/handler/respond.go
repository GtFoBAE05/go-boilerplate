// Package handler holds HTTP plumbing shared by all resource handlers:
// the response envelope, error mapping, request validation, pagination.
// Resource handlers live in subpackages (handler/user, ...).
//
// Every API response has exactly one shape, enforced here:
//
//	success: {"data": ..., "meta": {...}?}
//	failure: {"error": {"code": "...", "message": "...", "fields": {...}?}}
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/GtFoBAE05/go-boilerplate/internal/service"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Transport-level error codes owned by this layer. Domain error codes live on
// the domain errors themselves (see service.Error) so this list never has to
// grow when a resource is added.
const (
	CodeBadRequest       = "BAD_REQUEST"
	CodeValidationFailed = "VALIDATION_FAILED"
	CodeNotFound         = "NOT_FOUND"
	CodeMethodNotAllowed = "METHOD_NOT_ALLOWED"
	CodePayloadTooLarge  = "PAYLOAD_TOO_LARGE"
	CodeRateLimited      = "RATE_LIMITED"
	// CodeRateLimitUnavailable indicates the shared Redis limiter could not
	// check a request, so the request was refused rather than let through.
	CodeRateLimitUnavailable = "RATE_LIMIT_UNAVAILABLE"
	CodeTimeout              = "TIMEOUT"
	CodeInternal             = "INTERNAL"
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
	// Details carries structured context the client acts on (e.g. the rules
	// blocking a member removal). Populated from the domain error's optional
	// Details; nil for every other error.
	Details any `json:"details,omitempty"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

// WriteJSON writes v as-is, without the envelope. Reserved for
// infrastructure endpoints (health probes); API handlers use OK/OKList.
//
// v is marshalled into a buffer before any header is written: encoding
// straight to the ResponseWriter commits a 200 first, so a marshal failure
// halfway through would ship a truncated body under a success status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if v == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		return
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		// Nothing has been written yet, so a clean 500 is still possible.
		// Hand-rolled body: re-entering WriteJSON could fail the same way.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL","message":"internal server error"}}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
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

// NoContent writes a 204 with no body. It exists so a handler that returns no
// data still goes through this file: "every response through respond.go" is an
// invariant worth keeping literally true, or the one hand-written status is
// where a future envelope change quietly stops applying.
func NoContent(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNoContent)
}

// WriteError writes an error envelope: {"error": {"code", "message"}}.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message}})
}

// writeValidationError adds per-field messages; used by DecodeValid.
func writeValidationError(w http.ResponseWriter, fields map[string]string) {
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusUnprocessableEntity, errorEnvelope{Error: errorBody{
		Code:    CodeValidationFailed,
		Message: "validation failed",
		Fields:  fields,
	}})
}

// statusForKind is the only place HTTP statuses enter the domain-error path.
// A new Kind belongs here and in platform/errors; a new resource does not.
func statusForKind(kind service.Kind) int {
	switch kind {
	case service.KindInvalid:
		return http.StatusUnprocessableEntity
	case service.KindUnauthenticated:
		return http.StatusUnauthorized
	case service.KindForbidden:
		return http.StatusForbidden
	case service.KindNotFound:
		return http.StatusNotFound
	case service.KindConflict:
		return http.StatusConflict
	case service.KindInternal:
		return http.StatusInternalServerError
	default:
		// An unclassified domain error is a programming error; do not leak
		// the fact that it was unclassified to the client.
		return http.StatusInternalServerError
	}
}

// WriteServiceError turns a service-layer error into a response. A
// *service.Error supplies its own code and Kind, so this function needs no
// per-resource cases; context errors map to timeouts; anything else is logged
// and returned as a 500 without leaking internals. It also marks the active
// span so traces reflect HTTP-level failures.
func WriteServiceError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	span := trace.SpanFromContext(r.Context())

	// Context errors are checked before the domain error: a cancelled or
	// expired request is a transport outcome, not a server fault, and must
	// not be logged as an unhandled error or counted as a 5xx bug.
	if ctxErr := r.Context().Err(); ctxErr != nil {
		switch {
		case errors.Is(ctxErr, context.DeadlineExceeded):
			// The requestTimeout middleware cancels the context; this is
			// what writes the 504 envelope when the handler notices.
			span.SetStatus(codes.Error, "request timeout")
			log.WarnContext(r.Context(), "request timed out", "error", err)
			WriteError(w, http.StatusGatewayTimeout, CodeTimeout, "request timed out")
			return
		case errors.Is(ctxErr, context.Canceled):
			// Client hung up. No one is left to read a body, so only the
			// span is annotated. 499 is nginx's non-standard code, never
			// sent on the wire; it exists here purely as a log/trace marker.
			span.SetStatus(codes.Error, "client disconnected")
			log.DebugContext(r.Context(), "client disconnected before response", "error", err)
			return
		}
	}

	var domainErr *service.Error
	if errors.As(err, &domainErr) {
		span.SetStatus(codes.Error, domainErr.Code)
		body := errorBody{
			Code:    domainErr.Code,
			Message: domainErr.Message,
			Details: domainErr.Details,
		}
		// A validation error carries map[string]string in Details; render it
		// as the contract's "fields" object instead of opaque details.
		if fields, ok := domainErr.Details.(map[string]string); ok && domainErr.Kind == service.KindInvalid {
			body.Fields = fields
			body.Details = nil
		}
		w.Header().Set("Cache-Control", "no-store")
		WriteJSON(w, statusForKind(domainErr.Kind), errorEnvelope{Error: body})
		return
	}

	span.RecordError(err)
	span.SetStatus(codes.Error, "internal error")
	log.ErrorContext(r.Context(), "unhandled service error", "error", err)
	WriteError(w, http.StatusInternalServerError, CodeInternal, "internal server error")
}
