package user

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-boilerplate/internal/service"

	usersvc "go-boilerplate/internal/service/user"
)

// mockUserService implements usersvc.Service via configurable funcs.
type mockUserService struct {
	create func(ctx context.Context, in usersvc.CreateInput) (usersvc.User, error)
	get    func(ctx context.Context, id int64) (usersvc.User, error)
	list   func(ctx context.Context, limit, offset int32) ([]usersvc.User, error)
	update func(ctx context.Context, id int64, in usersvc.UpdateInput) (usersvc.User, error)
	del    func(ctx context.Context, id int64) error
}

var _ usersvc.Service = (*mockUserService)(nil)

func (m *mockUserService) Create(ctx context.Context, in usersvc.CreateInput) (usersvc.User, error) {
	return m.create(ctx, in)
}

func (m *mockUserService) Get(ctx context.Context, id int64) (usersvc.User, error) {
	return m.get(ctx, id)
}

func (m *mockUserService) List(ctx context.Context, limit, offset int32) ([]usersvc.User, error) {
	return m.list(ctx, limit, offset)
}

func (m *mockUserService) Update(ctx context.Context, id int64, in usersvc.UpdateInput) (usersvc.User, error) {
	return m.update(ctx, id, in)
}

func (m *mockUserService) Delete(ctx context.Context, id int64) error {
	return m.del(ctx, id)
}

func TestCreateValidation(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantField  string
	}{
		{"invalid json", "{", http.StatusBadRequest, ""},
		{"unknown field", `{"name":"alice","email":"a@b.co","emial":"typo"}`, http.StatusBadRequest, ""},
		{"missing name", `{"email":"a@b.co"}`, http.StatusUnprocessableEntity, "name"},
		{"missing email", `{"name":"alice"}`, http.StatusUnprocessableEntity, "email"},
		{"bad email", `{"name":"alice","email":"nope"}`, http.StatusUnprocessableEntity, "email"},
		{"name too long", `{"name":"` + strings.Repeat("x", 101) + `","email":"a@b.co"}`, http.StatusUnprocessableEntity, "name"},
	}

	// Validation short-circuits before the service is touched, so a nil
	// service is safe for these cases.
	h := NewHandler(nil, nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()

			h.create(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("want status %d, got %d (body: %s)", tt.wantStatus, rec.Code, rec.Body.String())
			}
			if tt.wantField == "" {
				return
			}
			var resp struct {
				Error struct {
					Code   string            `json:"code"`
					Fields map[string]string `json:"fields"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("response is not JSON: %v", err)
			}
			if resp.Error.Code != "VALIDATION_FAILED" {
				t.Fatalf("want code VALIDATION_FAILED, got %q", resp.Error.Code)
			}
			if _, ok := resp.Error.Fields[tt.wantField]; !ok {
				t.Fatalf("want field error for %q, got %v", tt.wantField, resp.Error.Fields)
			}
		})
	}
}

func TestCreateBodyTooLarge(t *testing.T) {
	// A body over the 1 MiB DecodeValid cap must be rejected with 413 and the
	// PAYLOAD_TOO_LARGE envelope, before the service is ever reached.
	h := NewHandler(nil, nil)

	huge := `{"name":"` + strings.Repeat("x", 2<<20) + `","email":"a@b.co"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(huge))
	rec := httptest.NewRecorder()
	h.create(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if resp.Error.Code != "PAYLOAD_TOO_LARGE" {
		t.Fatalf("want code PAYLOAD_TOO_LARGE, got %q", resp.Error.Code)
	}
}

func TestCreateSuccess(t *testing.T) {
	svc := &mockUserService{
		create: func(_ context.Context, in usersvc.CreateInput) (usersvc.User, error) {
			return usersvc.User{ID: 1, Name: in.Name, Email: in.Email}, nil
		},
	}
	h := NewHandler(svc, slog.New(slog.DiscardHandler))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"alice","email":"a@b.co"}`))
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data DetailResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if resp.Data.ID != 1 || resp.Data.Name != "alice" {
		t.Fatalf("unexpected response: %+v", resp.Data)
	}
}

func TestGetNotFoundMapsTo404(t *testing.T) {
	svc := &mockUserService{
		get: func(context.Context, int64) (usersvc.User, error) {
			return usersvc.User{}, service.ErrNotFound
		},
	}
	h := NewHandler(svc, slog.New(slog.DiscardHandler))

	req := httptest.NewRequest(http.MethodGet, "/42", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestUpdatePartialBody(t *testing.T) {
	var gotIn usersvc.UpdateInput
	svc := &mockUserService{
		update: func(_ context.Context, id int64, in usersvc.UpdateInput) (usersvc.User, error) {
			gotIn = in
			return usersvc.User{ID: id, Name: *in.Name, Email: "kept@example.com"}, nil
		},
	}
	h := NewHandler(svc, slog.New(slog.DiscardHandler))

	req := httptest.NewRequest(http.MethodPatch, "/7", strings.NewReader(`{"name":"  bob  "}`))
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if gotIn.Name == nil || *gotIn.Name != "bob" {
		t.Fatalf("want trimmed name %q passed to service, got %v", "bob", gotIn.Name)
	}
	if gotIn.Email != nil {
		t.Fatalf("want nil email (not provided), got %q", *gotIn.Email)
	}
}

func TestUpdateEmptyBodyRejected(t *testing.T) {
	// No service call expected: a nil service panics if the handler slips through.
	h := NewHandler(nil, slog.New(slog.DiscardHandler))

	req := httptest.NewRequest(http.MethodPatch, "/7", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}
