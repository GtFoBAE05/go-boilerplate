package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"
)

// NewPprofServer returns a loopback-only pprof listener, or nil when disabled.
// It uses a fresh ServeMux because importing net/http/pprof registers handlers
// on http.DefaultServeMux. Reach it with port-forward or SSH, never by widening
// the bind.
func NewPprofServer(enabled bool, port int, log *slog.Logger) *http.Server {
	if !enabled {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	log.Info("pprof listening", "addr", addr)
	return &http.Server{
		Addr:    addr,
		Handler: mux,
		// profile and trace stream for their full duration (30s by default), so
		// no write timeout is set. Header reads stay bounded to keep a stuck
		// client from pinning a goroutine.
		ReadHeaderTimeout: 5 * time.Second,
	}
}
