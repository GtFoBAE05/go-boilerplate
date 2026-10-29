package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// NewPprofServer returns a loopback-only pprof listener, or nil when disabled.
// It uses a fresh ServeMux because importing net/http/pprof registers handlers
// on http.DefaultServeMux. Reach it with port-forward or SSH, never by widening
// the bind.
func NewPprofServer(enabled bool, port int) *http.Server {
	if !enabled {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	return &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: mux,
		// profile and trace stream for their full duration (30s by default), so
		// no write timeout is set. Header reads stay bounded to keep a stuck
		// client from pinning a goroutine.
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// StartPprof binds the private listener and then logs, so a failed bind is not
// reported as a running profiler. It blocks until the server stops.
func StartPprof(srv *http.Server, log *slog.Logger) error {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen pprof: %w", err)
	}
	log.Info("pprof listening", "addr", ln.Addr().String())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("pprof server: %w", err)
	}
	return nil
}
