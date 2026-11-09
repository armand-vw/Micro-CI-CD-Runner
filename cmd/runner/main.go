// Command runner starts the Micro-CI/CD Runner HTTP server, webhook listener,
// and Docker build worker pool.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	assets "github.com/armand-vw/Micro-CI-CD-Runner"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/api"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/config"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/dockerx"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/events"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/pipeline"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/sse"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/store"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/webhook"
)

func main() {
	health := flag.Bool("healthcheck", false, "probe the local health endpoint and exit")
	flag.Parse()

	if *health {
		os.Exit(runHealthcheck())
	}

	if err := run(); err != nil {
		slog.Error("runner stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	seedProjects(ctx, cfg, db, logger)

	orphanNote := "\n[runner] process restarted before this build finished\n"
	if n, err := db.MarkOrphanedBuilds(ctx, orphanNote); err != nil {
		logger.Warn("mark orphaned builds", "error", err)
	} else if n > 0 {
		logger.Warn("marked orphaned builds as failed", "count", n)
	}

	dockerClient, err := dockerx.New(cfg.DockerHost, cfg.GitHubToken, logger)
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer dockerClient.Close()

	pingCtx, cancelPing := context.WithTimeout(ctx, 5*time.Second)
	if err := dockerClient.Ping(pingCtx); err != nil {
		logger.Warn("docker daemon unavailable; builds will fail until it is reachable", "error", err)
	} else if removed := dockerClient.CleanupStale(pingCtx); removed > 0 {
		logger.Info("removed stale build containers", "count", removed)
	}
	cancelPing()

	bus := events.NewBus()
	queue := pipeline.New(db, dockerClient, bus, logger, cfg.QueueSize, cfg.BuildTimeout, cfg.LogMaxBytes)
	queue.Start(ctx, cfg.MaxConcurrentBuilds)

	webhookHandler := webhook.NewHandler(cfg.WebhookSecret, db, queue, logger).WithPublisher(bus)
	sseHandler := sse.NewHandler(bus, logger)
	handlers := api.NewHandlers(db, queue, logger)

	webFS, err := fs.Sub(assets.WebFS, "web")
	if err != nil {
		return fmt.Errorf("open embedded dashboard: %w", err)
	}
	static := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The dashboard is small and changes with releases, so avoid stale UIs.
		w.Header().Set("Cache-Control", "no-cache")
		http.FileServer(http.FS(webFS)).ServeHTTP(w, r)
	})

	router := api.NewRouter(
		handlers,
		webhookHandler,
		sseHandler,
		static,
		logger,
		cfg.AllowedOrigins,
		cfg.AdminToken,
	)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		// WriteTimeout is intentionally zero: SSE connections are long-lived.
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("runner listening",
			"addr", srv.Addr,
			"workers", cfg.MaxConcurrentBuilds,
			"queue_size", cfg.QueueSize,
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		logger.Info("shutdown requested")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown", "error", err)
	}

	done := make(chan struct{})
	go func() {
		queue.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		logger.Warn("timed out waiting for build workers to stop")
	}

	logger.Info("runner stopped cleanly")
	return nil
}

func seedProjects(ctx context.Context, cfg *config.Config, db *store.Store, logger *slog.Logger) {
	seeds, err := config.LoadProjects(cfg.ProjectsFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			logger.Warn("projects file not found; starting with an empty project list", "path", cfg.ProjectsFile)
			return
		}
		logger.Error("load projects", "error", err, "path", cfg.ProjectsFile)
		return
	}

	projects := make([]store.Project, 0, len(seeds))
	for _, p := range seeds {
		projects = append(projects, store.Project{
			Name:         p.Name,
			RepoURL:      p.RepoURL,
			Branch:       p.Branch,
			Image:        p.Image,
			SetupCommand: p.SetupCommand,
			TestCommand:  p.TestCommand,
		})
	}

	if err := db.UpsertProjects(ctx, projects); err != nil {
		logger.Error("seed projects", "error", err)
		return
	}
	logger.Info("seeded projects", "count", len(projects), "path", cfg.ProjectsFile)
}

func runHealthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.Status)
		return 1
	}
	return 0
}
