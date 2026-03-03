package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/pprof"
)

// RegisterRoutes adds /healthz, /readyz, and /debug/pprof/* to the given mux.
// If readyFn is nil, /readyz always returns 200.
func RegisterRoutes(mux *http.ServeMux, readyFn func() bool) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if readyFn != nil && !readyFn() {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "not_ready"})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
	})

	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
}

// Serve creates a standalone HTTP server with health and pprof routes.
// It blocks until ctx is cancelled, then shuts down gracefully.
func Serve(ctx context.Context, addr string, readyFn func() bool, logger *slog.Logger) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, readyFn)

	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	logger.Info("health server listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("health server error", "error", err)
	}
}
