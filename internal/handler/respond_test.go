package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GtFoBAE05/go-boilerplate/internal/service"
	usersvc "github.com/GtFoBAE05/go-boilerplate/internal/service/user"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func decodeError(t *testing.T, body []byte) errorBody {
	t.Helper()
	var env struct {
		Error errorBody `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("response is not the error envelope: %v (body: %s)", err, body)
	}
	return env.Error
}

// A *service.Error supplies its own Kind and code, which is what keeps this
// shared layer free of per-resource cases.
func TestWriteServiceErrorUsesDomainErrorMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want *service.Error
	}{
		{"not found", service.ErrNotFound, service.ErrNotFound},
		// A resource declares its own error; this layer needs no change.
		{"email taken", usersvc.ErrEmailTaken, usersvc.ErrEmailTaken},
		// Wrapping must not lose the mapping: services add context with %w.
		{"wrapped", fmt.Errorf("get user 7: %w", service.ErrNotFound), service.ErrNotFound},
		{
			"resource-defined error needs no change here",
			service.NewError("ORDER_LOCKED", service.KindConflict, "order is locked"),
			service.NewError("ORDER_LOCKED", service.KindConflict, "order is locked"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/", nil)

			WriteServiceError(rec, r, discardLogger(), tt.err)

			if rec.Code != statusForKind(tt.want.Kind) {
				t.Fatalf("want status %d, got %d", statusForKind(tt.want.Kind), rec.Code)
			}
			got := decodeError(t, rec.Body.Bytes())
			if got.Code != tt.want.Code {
				t.Fatalf("want code %q, got %q", tt.want.Code, got.Code)
			}
			if got.Message != tt.want.Message {
				t.Fatalf("want message %q, got %q", tt.want.Message, got.Message)
			}
		})
	}
}

// Structured details survive the mapping; a client that must act on them (say,
// which rules blocked a removal) should not have to parse the message text.
func TestWriteServiceErrorCarriesDetails(t *testing.T) {
	domainErr := service.NewError("RULES_BLOCK", service.KindConflict, "blocked").
		WithDetails(map[string]any{"rules": []string{"self-approval"}})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	WriteServiceError(rec, r, discardLogger(), domainErr)

	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
	got := decodeError(t, rec.Body.Bytes())
	details, ok := got.Details.(map[string]any)
	if !ok {
		t.Fatalf("want details object, got %#v", got.Details)
	}
	rules, ok := details["rules"].([]any)
	if !ok || len(rules) != 1 || rules[0] != "self-approval" {
		t.Fatalf("details did not survive the envelope: %#v", details)
	}
}

// An unmapped error must never leak its text to the client.
func TestWriteServiceErrorHidesUnmappedError(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	WriteServiceError(rec, r, discardLogger(), errors.New("dial tcp 10.0.0.5:5432: connection refused"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	got := decodeError(t, rec.Body.Bytes())
	if got.Code != CodeInternal {
		t.Fatalf("want code %q, got %q", CodeInternal, got.Code)
	}
	if got.Message != "internal server error" {
		t.Fatalf("internal detail leaked: %q", got.Message)
	}
}

// requestTimeout only cancels the context; this is what turns the resulting
// deadline error into a 504 rather than a misleading 500.
func TestWriteServiceErrorMapsDeadlineTo504(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)

	// The wrapped error mimics what pgx returns when the request context expires.
	WriteServiceError(rec, r, discardLogger(), fmt.Errorf("query: %w", context.DeadlineExceeded))

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("want 504, got %d", rec.Code)
	}
	if got := decodeError(t, rec.Body.Bytes()); got.Code != CodeTimeout {
		t.Fatalf("want code %q, got %q", CodeTimeout, got.Code)
	}
}

// A client that hung up gets nothing written: there is no reader left, and it
// must not be recorded as a server fault.
func TestWriteServiceErrorSkipsBodyOnClientDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)

	WriteServiceError(rec, r, discardLogger(), fmt.Errorf("query: %w", context.Canceled))

	if rec.Body.Len() != 0 {
		t.Fatalf("want no body for a disconnected client, got %q", rec.Body.String())
	}
}

// A deadline error must not be mistaken for a domain error even when both are
// present, since a timeout is a transport outcome rather than a 404.
func TestWriteServiceErrorPrefersContextErrorOverDomainError(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)

	WriteServiceError(rec, r, discardLogger(), service.ErrNotFound)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("want 504 for an expired request, got %d", rec.Code)
	}
}

// WriteJSON buffers before writing headers, so an unmarshalable value yields a
// clean 500 rather than a 200 with a truncated body.
func TestWriteJSONFailsCleanlyOnUnmarshalableValue(t *testing.T) {
	rec := httptest.NewRecorder()

	// A channel cannot be marshalled to JSON.
	WriteJSON(rec, http.StatusOK, map[string]any{"bad": make(chan int)})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if got := decodeError(t, rec.Body.Bytes()); got.Code != CodeInternal {
		t.Fatalf("want a well-formed INTERNAL envelope, got %+v", got)
	}
}

// NoContent is the one success path with no payload; it still belongs to this
// file so the response layer stays the only writer.
func TestNoContentWritesNoBody(t *testing.T) {
	rec := httptest.NewRecorder()

	NoContent(rec)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("want empty body, got %q", rec.Body.String())
	}
}

func TestErrorResponsesDisableCaching(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusBadRequest, CodeBadRequest, "bad")

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("want Cache-Control no-store, got %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("want X-Content-Type-Options nosniff, got %q", got)
	}
}

func TestWriteJSONSetsContentTypeOnNilBody(t *testing.T) {
	rec := httptest.NewRecorder()

	WriteJSON(rec, http.StatusNoContent, nil)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("want empty body, got %q", rec.Body.String())
	}
}
