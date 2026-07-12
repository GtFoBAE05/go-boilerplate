package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-boilerplate/internal/config"

	usersvc "go-boilerplate/internal/service/user"
)

// stubUserService lets the router build without a real service; List returns
// empty so allowed requests succeed and we can observe the 429 on overflow.
type stubUserService struct{ usersvc.Service }

func (stubUserService) List(context.Context, int32, int32) ([]usersvc.User, error) {
	return nil, nil
}

// newTestServer builds the real router with rate limiting set to a burst of 1
// so the second request in a window is throttled.
func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	cfg := &config.Config{
		Redis:     config.RedisConfig{Mode: config.RedisModeDisabled},
		RateLimit: config.RateLimitConfig{Enabled: true, RPS: 1, Burst: 1},
	}
	srv := New(cfg, slog.New(slog.DiscardHandler), Deps{Users: stubUserService{}})
	return srv.http.Handler
}

func TestRateLimitEnvelope(t *testing.T) {
	h := newTestServer(t)

	do := func() *httptest.ResponseRecorder {
		// Same RemoteAddr each call so httprate keys them to one client.
		req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
		req.RemoteAddr = "203.0.113.5:1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := do(); rec.Code != http.StatusOK {
		t.Fatalf("first request: want 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	rec := do()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: want 429, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("429 response is not JSON: %v", err)
	}
	if resp.Error.Code != "RATE_LIMITED" {
		t.Fatalf("want code RATE_LIMITED, got %q", resp.Error.Code)
	}
	if resp.Error.Message == "" {
		t.Fatalf("want non-empty message")
	}
}

// Probes and /metrics live outside /api/v1 and must never be rate-limited,
// even under a burst that would throttle the API.
func TestProbesNotRateLimited(t *testing.T) {
	h := newTestServer(t)

	for _, path := range []string{"/healthz", "/metrics"} {
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "203.0.113.5:1234"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code == http.StatusTooManyRequests {
				t.Fatalf("%s was rate-limited on call %d", path, i+1)
			}
		}
	}
}
