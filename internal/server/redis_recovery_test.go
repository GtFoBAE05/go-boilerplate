package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GtFoBAE05/go-boilerplate/internal/config"
	platformredis "github.com/GtFoBAE05/go-boilerplate/internal/platform/redis"
)

// fakeRedisServer starts rejecting connections, then switches to a minimal
// RESP server. That reproduces the optional-Redis path: startup fails, the
// client is kept, and a later readiness probe must see it recover.
type fakeRedisServer struct {
	ln      net.Listener
	serving atomic.Bool
	mu      sync.Mutex
	conns   map[net.Conn]struct{}
}

func newFakeRedisServer(t *testing.T) *fakeRedisServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeRedisServer{ln: ln, conns: map[net.Conn]struct{}{}}
	go s.accept()
	t.Cleanup(s.Close)
	return s
}

func (s *fakeRedisServer) Addr() string { return s.ln.Addr().String() }

func (s *fakeRedisServer) Serve() { s.serving.Store(true) }

func (s *fakeRedisServer) Close() {
	_ = s.ln.Close()
	s.mu.Lock()
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
}

func (s *fakeRedisServer) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		go s.handle(conn)
	}
}

func (s *fakeRedisServer) handle(conn net.Conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		_ = conn.Close()
	}()

	if !s.serving.Load() {
		return
	}

	r := bufio.NewReader(conn)
	for {
		args, err := readRESPCommand(r)
		if err != nil {
			return
		}
		if len(args) == 0 {
			continue
		}
		switch strings.ToUpper(args[0]) {
		case "HELLO":
			// Force go-redis onto RESP2; the client is not configured for
			// RESP3-specific commands, so this is enough for PING.
			_, _ = conn.Write([]byte("-ERR unknown command 'hello'\r\n"))
		case "PING":
			_, _ = conn.Write([]byte("+PONG\r\n"))
		default:
			_, _ = conn.Write([]byte("+OK\r\n"))
		}
	}
}

func readRESPCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("expected array header, got %q", line)
	}
	n, err := strconv.Atoi(line[1:])
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		hdr, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		hdr = strings.TrimRight(hdr, "\r\n")
		if !strings.HasPrefix(hdr, "$") {
			return nil, fmt.Errorf("expected bulk header, got %q", hdr)
		}
		length, err := strconv.Atoi(hdr[1:])
		if err != nil {
			return nil, err
		}
		buf := make([]byte, length)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		if _, err := r.Discard(2); err != nil {
			return nil, err
		}
		args = append(args, string(buf))
	}
	return args, nil
}

func readinessRedis(t *testing.T, srv *Server) string {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("optional Redis must not fail readiness, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode readiness: %v", err)
	}
	return body.Checks["redis"]
}

func TestOptionalRedisRecoversOnReadiness(t *testing.T) {
	fake := newFakeRedisServer(t)

	client, err := platformredis.New(context.Background(), fake.Addr(),
		platformredis.DialTimeout(50*time.Millisecond),
		platformredis.ReadTimeout(100*time.Millisecond),
		platformredis.WriteTimeout(100*time.Millisecond),
	)
	if err == nil {
		t.Fatal("want the initial ping to fail while the server is rejecting connections")
	}
	if client == nil {
		t.Fatal("want the client kept for lazy reconnect")
	}
	t.Cleanup(func() { _ = client.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, RequestTimeout: 20 * time.Second},
		Redis:  config.RedisConfig{Mode: config.RedisModeOptional},
	}
	srv, err := New(cfg, slog.New(slog.DiscardHandler), Deps{
		Users:    stubUserService{},
		Postgres: stubPinger{},
		Redis:    client,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := readinessRedis(t, srv); got != "degraded" {
		t.Fatalf("want redis degraded before recovery, got %q", got)
	}

	fake.Serve()

	if got := readinessRedis(t, srv); got != "up" {
		t.Fatalf("want redis up after recovery, got %q", got)
	}
}
