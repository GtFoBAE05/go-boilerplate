package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
)

// NewPprofServer builds a private pprof server bound to localhost:<port>, or
// returns nil when port is 0. It is a separate listener from the public API so
// profiling endpoints are never reachable from outside the host — pprof leaks
// memory contents and lets a caller trigger expensive profiles.
//
// Handlers are mounted on a fresh ServeMux, not http.DefaultServeMux: importing
// net/http/pprof registers them on the default mux, and reusing it would expose
// pprof anywhere DefaultServeMux is served.
func NewPprofServer(port int, log *slog.Logger) *http.Server {
	if port <= 0 {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// Bind 127.0.0.1 explicitly (not ":") so the socket is host-private.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	log.Info("pprof listening", "addr", addr)
	return &http.Server{Addr: addr, Handler: mux}
}
