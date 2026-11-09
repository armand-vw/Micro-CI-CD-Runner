package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/armand-vw/Micro-CI-CD-Runner/internal/dockerx"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/events"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/pipeline"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/store"
)

type noopRunner struct{}

func (noopRunner) Run(context.Context, dockerx.Spec, func(string)) (int, error) { return 0, nil }

func newTestRouter(t *testing.T) (*store.Store, http.Handler, int64) {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "runner.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if err := db.UpsertProjects(ctx, []store.Project{{
		Name: "demo", RepoURL: "github.com/owner/demo", Branch: "main",
		Image: "node:20-alpine", SetupCommand: "true", TestCommand: "npm test",
	}}); err != nil {
		t.Fatalf("UpsertProjects: %v", err)
	}
	projects, err := db.ListProjects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("ListProjects: %v, %d", err, len(projects))
	}

	build, _, err := db.CreateBuild(ctx, store.Build{
		ProjectID: projects[0].ID,
		CommitSHA: "abc123",
		Ref:       "refs/heads/main",
	})
	if err != nil {
		t.Fatalf("CreateBuild: %v", err)
	}
	if err := db.MarkRunning(ctx, build.ID); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := db.FinishBuild(ctx, build.ID, store.StatusSuccess, "", "line one\nline two\n"); err != nil {
		t.Fatalf("FinishBuild: %v", err)
	}

	queue := pipeline.New(db, noopRunner{}, events.NewBus(), slog.Default(), 10, time.Minute, 1024)
	handlers := NewHandlers(db, queue, slog.Default())
	router := NewRouter(
		handlers,
		http.NotFoundHandler(),
		http.NotFoundHandler(),
		http.NotFoundHandler(),
		slog.Default(),
		nil,
		"test-admin",
	)
	return db, router, build.ID
}

func serve(router http.Handler, method, path string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestHealthAndProjects(t *testing.T) {
	t.Parallel()
	_, router, _ := newTestRouter(t)

	rec := serve(router, http.MethodGet, "/healthz", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("health = %d %s", rec.Code, rec.Body.String())
	}

	rec = serve(router, http.MethodGet, "/api/projects", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("projects status = %d", rec.Code)
	}
	var payload struct {
		Projects []store.Project `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	if len(payload.Projects) != 1 || payload.Projects[0].Name != "demo" {
		t.Fatalf("projects = %+v", payload.Projects)
	}
}

func TestBuildsAndLogs(t *testing.T) {
	t.Parallel()
	_, router, buildID := newTestRouter(t)

	rec := serve(router, http.MethodGet, "/api/builds?limit=10", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("builds status = %d", rec.Code)
	}
	var list struct {
		Builds []store.Build `json:"builds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode builds: %v", err)
	}
	if len(list.Builds) != 1 || list.Builds[0].ID != buildID {
		t.Fatalf("builds = %+v", list.Builds)
	}
	if list.Builds[0].Logs != "" {
		t.Fatal("build list must not include logs")
	}

	rec = serve(router, http.MethodGet, "/api/builds/"+strconv.FormatInt(buildID, 10), nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("build detail status = %d", rec.Code)
	}

	rec = serve(router, http.MethodGet, "/api/builds/"+strconv.FormatInt(buildID, 10)+"/logs?download=1", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "line one") {
		t.Fatalf("logs = %d %s", rec.Code, rec.Body.String())
	}
	if disposition := rec.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") {
		t.Fatalf("disposition = %q", disposition)
	}

	rec = serve(router, http.MethodGet, "/api/builds/99999", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing build status = %d, want 404", rec.Code)
	}

	rec = serve(router, http.MethodGet, "/api/builds?status=bogus", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad filter status = %d, want 400", rec.Code)
	}
}

func TestRetryRequiresAdminToken(t *testing.T) {
	t.Parallel()
	_, router, buildID := newTestRouter(t)

	rec := serve(router, http.MethodPost, "/api/builds/"+strconv.FormatInt(buildID, 10)+"/retry", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("retry without token = %d, want 401", rec.Code)
	}

	rec = serve(router, http.MethodPost, "/api/builds/"+strconv.FormatInt(buildID, 10)+"/retry", nil, map[string]string{
		"Authorization": "Bearer test-admin",
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry with token = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode retry: %v", err)
	}
	if payload["status"] != "queued" {
		t.Fatalf("retry payload = %+v", payload)
	}
}
