package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"
)

// NewPprofServer builds a private pprof server bound to 127.0.0.1:<port>, or
// returns nil when disabled — in which case no socket is opened and the caller
// starts no goroutine for it.
//
// It gets its own loopback-only listener rather than a path on the public
// router: pprof exposes raw memory and lets any caller trigger an expensive
// profile, unlike /metrics which a scraper must reach from off-host. Reach it
// with `kubectl port-forward` or over SSH, never by widening the bind.
//
// Handlers are mounted on a fresh ServeMux, not http.DefaultServeMux: importing
// net/http/pprof registers them on the default mux, and reusing it would expose
// pprof anywhere DefaultServeMux is served.
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
