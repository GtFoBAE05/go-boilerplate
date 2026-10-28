//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	rootconfig "github.com/GtFoBAE05/go-boilerplate/internal/config"
	"github.com/GtFoBAE05/go-boilerplate/internal/platform/postgres"
	"github.com/GtFoBAE05/go-boilerplate/internal/repository"
	usersvc "github.com/GtFoBAE05/go-boilerplate/internal/service/user"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func setupServer(t *testing.T) http.Handler {
	t.Helper()
	ctx := context.Background()

	ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine@sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab",
		tcpostgres.WithDatabase("app"),
		tcpostgres.WithUsername("app"),
		tcpostgres.WithPassword("app"),
		// Without an explicit readiness wait, the pool can ping a server that is
		// still starting.
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)

	log := slog.New(slog.DiscardHandler)
	if err := postgres.Migrate(ctx, pool, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	repo := repository.NewStore(pool)
	svc := usersvc.NewUserService(repo, log)
	cfg := &rootconfig.Config{
		Service: rootconfig.ServiceConfig{Name: "test", Env: rootconfig.EnvDevelopment},
		Server: rootconfig.ServerConfig{
			Port: 8080, ReadTimeout: 10 * time.Second, ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
			RequestTimeout: 20 * time.Second,
		},
		Redis: rootconfig.RedisConfig{Mode: rootconfig.RedisModeDisabled},
	}
	srv, err := New(cfg, log, Deps{Users: svc, Postgres: pool})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	return srv.http.Handler
}

type apiResponse struct {
	Data json.RawMessage `json:"data"`
	Meta struct {
		Limit int `json:"limit"`
	} `json:"meta"`
	Error struct {
		Code   string            `json:"code"`
		Fields map[string]string `json:"fields"`
	} `json:"error"`
}

type userDetail struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func do(t *testing.T, h http.Handler, method, path, body string, wantStatus int) []byte {
	t.Helper()
	var requestBody *strings.Reader
	if body != "" {
		requestBody = strings.NewReader(body)
	}
	var req *http.Request
	if requestBody == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, requestBody)
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("%s %s: want status %d, got %d (body: %s)", method, path, wantStatus, rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}

func decodeResponse(t *testing.T, body []byte) apiResponse {
	t.Helper()
	var resp apiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	return resp
}

func decodeUser(t *testing.T, body []byte) userDetail {
	t.Helper()
	resp := decodeResponse(t, body)
	var user userDetail
	if err := json.Unmarshal(resp.Data, &user); err != nil {
		t.Fatalf("decode user response %q: %v", body, err)
	}
	return user
}

func TestUserHTTPVertical(t *testing.T) {
	h := setupServer(t)

	created := decodeUser(t, do(t, h, http.MethodPost, "/api/v1/users",
		`{"name":"  Ada  ","email":"ada@example.com"}`, http.StatusCreated))
	if created.ID == 0 || created.Name != "Ada" || created.Email != "ada@example.com" {
		t.Fatalf("unexpected created user: %+v", created)
	}

	path := fmt.Sprintf("/api/v1/users/%d", created.ID)
	got := decodeUser(t, do(t, h, http.MethodGet, path, "", http.StatusOK))
	if got.Email != created.Email {
		t.Fatalf("get email: want %q, got %q", created.Email, got.Email)
	}

	updated := decodeUser(t, do(t, h, http.MethodPatch, path, `{"name":"Ada L."}`, http.StatusOK))
	if updated.Name != "Ada L." {
		t.Fatalf("patch name: want %q, got %q", "Ada L.", updated.Name)
	}
	got = decodeUser(t, do(t, h, http.MethodGet, path, "", http.StatusOK))
	if got.Name != "Ada L." {
		t.Fatalf("get after patch: want %q, got %q", "Ada L.", got.Name)
	}

	duplicate := decodeResponse(t, do(t, h, http.MethodPost, "/api/v1/users",
		`{"name":"Other","email":"ada@example.com"}`, http.StatusConflict))
	if duplicate.Error.Code != "EMAIL_TAKEN" {
		t.Fatalf("duplicate email: want EMAIL_TAKEN, got %q", duplicate.Error.Code)
	}

	caseVariant := decodeResponse(t, do(t, h, http.MethodPost, "/api/v1/users",
		`{"name":"Other","email":"ADA@Example.COM"}`, http.StatusConflict))
	if caseVariant.Error.Code != "EMAIL_TAKEN" {
		t.Fatalf("case-variant duplicate: want EMAIL_TAKEN, got %q", caseVariant.Error.Code)
	}

	invalid := decodeResponse(t, do(t, h, http.MethodPost, "/api/v1/users",
		`{"name":"   ","email":"x@example.com"}`, http.StatusUnprocessableEntity))
	if _, ok := invalid.Error.Fields["name"]; !ok {
		t.Fatalf("want validation error for name, got %v", invalid.Error.Fields)
	}

	listed := decodeResponse(t, do(t, h, http.MethodGet, "/api/v1/users?limit=1", "", http.StatusOK))
	if listed.Meta.Limit != 1 {
		t.Fatalf("list limit: want 1, got %d", listed.Meta.Limit)
	}

	if body := do(t, h, http.MethodDelete, path, "", http.StatusNoContent); len(body) != 0 {
		t.Fatalf("delete body must be empty, got %q", body)
	}

	missing := decodeResponse(t, do(t, h, http.MethodGet, path, "", http.StatusNotFound))
	if missing.Error.Code != "NOT_FOUND" {
		t.Fatalf("get after delete: want NOT_FOUND, got %q", missing.Error.Code)
	}
}
