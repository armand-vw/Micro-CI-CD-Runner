package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/armand-vw/Micro-CI-CD-Runner/internal/pipeline"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/store"
)

// Handlers implements the JSON API.
type Handlers struct {
	store  *store.Store
	queue  *pipeline.Queue
	logger *slog.Logger
}

// NewHandlers wires the API dependencies.
func NewHandlers(st *store.Store, queue *pipeline.Queue, logger *slog.Logger) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handlers{store: st, queue: queue, logger: logger}
}

// Health reports database connectivity.
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := h.store.Ping(ctx); err != nil {
		h.logger.Error("health check failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"queue_len": h.queue.Len(),
	})
}

// ListProjects returns configured projects.
func (h *Handlers) ListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := h.store.ListProjects(r.Context())
	if err != nil {
		h.logger.Error("list projects", "error", err)
		writeError(w, http.StatusInternalServerError, "cannot list projects")
		return
	}
	if projects == nil {
		projects = []store.Project{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

// Stats returns aggregate build counts.
func (h *Handlers) Stats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.store.Stats(r.Context())
	if err != nil {
		h.logger.Error("build stats", "error", err)
		writeError(w, http.StatusInternalServerError, "cannot load stats")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// ListBuilds returns a filtered, paginated build list.
func (h *Handlers) ListBuilds(w http.ResponseWriter, r *http.Request) {
	filter := store.BuildFilter{
		Status: r.URL.Query().Get("status"),
	}

	if raw := r.URL.Query().Get("project_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid project_id")
			return
		}
		filter.ProjectID = id
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			filter.Limit = v
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			filter.Offset = v
		}
	}

	builds, err := h.store.ListBuilds(r.Context(), filter)
	if err != nil {
		h.logger.Error("list builds", "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if builds == nil {
		builds = []store.Build{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"builds": builds})
}

// GetBuild returns one build without its log body.
func (h *Handlers) GetBuild(w http.ResponseWriter, r *http.Request) {
	id, ok := buildID(w, r)
	if !ok {
		return
	}
	build, err := h.store.GetBuild(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "build not found")
		return
	}
	if err != nil {
		h.logger.Error("get build", "build_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "cannot load build")
		return
	}
	build.Logs = ""
	writeJSON(w, http.StatusOK, build)
}

// GetBuildLogs streams the stored log body as plain text.
func (h *Handlers) GetBuildLogs(w http.ResponseWriter, r *http.Request) {
	id, ok := buildID(w, r)
	if !ok {
		return
	}
	build, err := h.store.GetBuild(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "build not found")
		return
	}
	if err != nil {
		h.logger.Error("get build logs", "build_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "cannot load logs")
		return
	}

	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=build-%d.log", disposition, id))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(build.Logs))
}

// RetryBuild queues a fresh build for the same commit.
func (h *Handlers) RetryBuild(w http.ResponseWriter, r *http.Request) {
	id, ok := buildID(w, r)
	if !ok {
		return
	}

	previous, err := h.store.GetBuild(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "build not found")
		return
	}
	if err != nil {
		h.logger.Error("load build for retry", "build_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "cannot load build")
		return
	}

	build, created, err := h.store.CreateBuild(r.Context(), store.Build{
		ProjectID: previous.ProjectID,
		CommitSHA: previous.CommitSHA,
		Ref:       previous.Ref,
	})
	if err != nil {
		h.logger.Error("create retry build", "build_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "cannot create retry build")
		return
	}
	if !created {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "duplicate", "build_id": build.ID})
		return
	}

	if err := h.queue.Enqueue(r.Context(), build.ID); err != nil {
		_ = h.store.FinishBuild(r.Context(), build.ID, store.StatusFailed, "", "\n[runner] build queue is full\n")
		writeError(w, http.StatusServiceUnavailable, "build queue is full")
		return
	}

	h.logger.Info("queued retry", "previous_build_id", id, "build_id", build.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "queued", "build_id": build.ID})
}

func buildID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid build id")
		return 0, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
