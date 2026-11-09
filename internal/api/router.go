package api

import (
	"log/slog"
	"net/http"
)

// NewRouter wires all HTTP routes. Go 1.22 method patterns keep routing in the
// standard library, so no third-party router is needed.
func NewRouter(
	handlers *Handlers,
	webhook http.Handler,
	sseHandler http.Handler,
	static http.Handler,
	logger *slog.Logger,
	allowedOrigins []string,
	adminToken string,
) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", handlers.Health)
	mux.Handle("POST /webhook", webhook)

	mux.HandleFunc("GET /api/projects", handlers.ListProjects)
	mux.HandleFunc("GET /api/stats", handlers.Stats)
	mux.HandleFunc("GET /api/builds", handlers.ListBuilds)
	mux.HandleFunc("GET /api/builds/{id}", handlers.GetBuild)
	mux.HandleFunc("GET /api/builds/{id}/logs", handlers.GetBuildLogs)
	mux.Handle("POST /api/builds/{id}/retry", requireAdmin(adminToken)(http.HandlerFunc(handlers.RetryBuild)))
	mux.Handle("GET /api/events", sseHandler)

	mux.Handle("GET /", static)

	return requestLogger(logger)(recoverer(logger)(cors(allowedOrigins)(mux)))
}
