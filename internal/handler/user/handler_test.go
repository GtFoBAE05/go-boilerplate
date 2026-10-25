package user

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GtFoBAE05/go-boilerplate/internal/service"

	usersvc "github.com/GtFoBAE05/go-boilerplate/internal/service/user"
)

// mockUserService implements usersvc.UserService via configurable funcs.
type mockUserService struct {
	create func(ctx context.Context, in usersvc.CreateInput) (usersvc.CreateOutput, error)
	get    func(ctx context.Context, id int64) (usersvc.GetOutput, error)
	list   func(ctx context.Context, limit, offset int32) ([]usersvc.ListOutput, error)
	update func(ctx context.Context, id int64, in usersvc.UpdateInput) (usersvc.UpdateOutput, error)
	del    func(ctx context.Context, id int64) error
}

var _ usersvc.UserService = (*mockUserService)(nil)

func (m *mockUserService) Create(ctx context.Context, in usersvc.CreateInput) (usersvc.CreateOutput, error) {
	return m.create(ctx, in)
}

func (m *mockUserService) Get(ctx context.Context, id int64) (usersvc.GetOutput, error) {
	return m.get(ctx, id)
}

func (m *mockUserService) List(ctx context.Context, limit, offset int32) ([]usersvc.ListOutput, error) {
	return m.list(ctx, limit, offset)
}

func (m *mockUserService) Update(ctx context.Context, id int64, in usersvc.UpdateInput) (usersvc.UpdateOutput, error) {
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
		{"trailing json value", `{"name":"a","email":"a@b.co"}{"name":"b"}`, http.StatusBadRequest, ""},
		{"unknown field", `{"name":"alice","email":"a@b.co","emial":"typo"}`, http.StatusBadRequest, ""},
		{"missing name", `{"email":"a@b.co"}`, http.StatusUnprocessableEntity, "name"},
		{"missing email", `{"name":"alice"}`, http.StatusUnprocessableEntity, "email"},
		{"bad email", `{"name":"alice","email":"nope"}`, http.StatusUnprocessableEntity, "email"},
		{"name too long", `{"name":"` + strings.Repeat("x", 101) + `","email":"a@b.co"}`, http.StatusUnprocessableEntity, "name"},
		// Normalization runs before validation, so padding is not a bypass.
		{"whitespace-only name", `{"name":"   ","email":"a@b.co"}`, http.StatusUnprocessableEntity, "name"},
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

func TestCreateRejectsLongEmailBeforeService(t *testing.T) {
	calls := 0
	svc := &mockUserService{
		create: func(context.Context, usersvc.CreateInput) (usersvc.CreateOutput, error) {
			calls++
			return usersvc.CreateOutput{}, nil
		},
	}
	h := NewHandler(svc, slog.New(slog.DiscardHandler))
	body := `{"name":"a","email":"` + strings.Repeat("a", 395) + `@b.co"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Error struct {
			Fields map[string]string `json:"fields"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if _, ok := resp.Error.Fields["email"]; !ok {
		t.Fatalf("want email field error, got %v", resp.Error.Fields)
	}
	if calls != 0 {
		t.Fatalf("service called %d times for invalid email", calls)
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
		create: func(_ context.Context, in usersvc.CreateInput) (usersvc.CreateOutput, error) {
			return usersvc.CreateOutput{ID: 1, Name: in.Name, Email: in.Email}, nil
		},
	}
	h := NewHandler(svc, slog.New(slog.DiscardHandler))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{\"name\":\"alice\",\"email\":\"a@b.co\"}\n"))
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
		get: func(context.Context, int64) (usersvc.GetOutput, error) {
			return usersvc.GetOutput{}, service.ErrNotFound
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

func TestCreateTrimsBeforeService(t *testing.T) {
	var gotIn usersvc.CreateInput
	svc := &mockUserService{
		create: func(_ context.Context, in usersvc.CreateInput) (usersvc.CreateOutput, error) {
			gotIn = in
			return usersvc.CreateOutput{ID: 1, Name: in.Name, Email: in.Email}, nil
		},
	}
	h := NewHandler(svc, slog.New(slog.DiscardHandler))

	body := `{"name":"  alice  ","email":" a@b.co "}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if gotIn.Name != "alice" || gotIn.Email != "a@b.co" {
		t.Fatalf("service must receive normalized input, got %+v", gotIn)
	}
}

// Normalization runs before validation, so a whitespace-only update is a 422
// rather than an empty string written over a real value.
func TestUpdateWhitespaceOnlyNameRejected(t *testing.T) {
	h := NewHandler(nil, slog.New(slog.DiscardHandler))

	req := httptest.NewRequest(http.MethodPatch, "/7", strings.NewReader(`{"name":"   "}`))
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestUpdatePartialBody(t *testing.T) {
	var gotIn usersvc.UpdateInput
	svc := &mockUserService{
		update: func(_ context.Context, id int64, in usersvc.UpdateInput) (usersvc.UpdateOutput, error) {
			gotIn = in
			return usersvc.UpdateOutput{ID: id, Name: *in.Name, Email: "kept@example.com"}, nil
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

// Deleting a row that never existed is a client mistake worth reporting, not a
// silent 204.
func TestDeleteNotFoundMapsTo404(t *testing.T) {
	svc := &mockUserService{
		del: func(context.Context, int64) error { return service.ErrNotFound },
	}
	h := NewHandler(svc, slog.New(slog.DiscardHandler))

	req := httptest.NewRequest(http.MethodDelete, "/42", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteSuccessReturns204(t *testing.T) {
	svc := &mockUserService{
		del: func(context.Context, int64) error { return nil },
	}
	h := NewHandler(svc, slog.New(slog.DiscardHandler))

	req := httptest.NewRequest(http.MethodDelete, "/42", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

// A malformed ?limit must fail rather than silently serve the default page, and
// the service must never be reached — a nil service panics if it is.
func TestListRejectsBadPagination(t *testing.T) {
	h := NewHandler(nil, slog.New(slog.DiscardHandler))

	req := httptest.NewRequest(http.MethodGet, "/?limit=abc", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

// A valid page must reach the service unchanged.
func TestListPassesPaginationThrough(t *testing.T) {
	var gotLimit, gotOffset int32
	svc := &mockUserService{
		list: func(_ context.Context, limit, offset int32) ([]usersvc.ListOutput, error) {
			gotLimit, gotOffset = limit, offset
			return nil, nil
		},
	}
	h := NewHandler(svc, slog.New(slog.DiscardHandler))

	req := httptest.NewRequest(http.MethodGet, "/?limit=7&offset=14", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if gotLimit != 7 || gotOffset != 14 {
		t.Fatalf("want limit=7 offset=14 at the service, got %d/%d", gotLimit, gotOffset)
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
