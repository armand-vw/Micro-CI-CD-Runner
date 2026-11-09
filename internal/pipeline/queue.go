// Package pipeline turns queued build IDs into isolated Docker executions and
// records their status and logs.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/armand-vw/Micro-CI-CD-Runner/internal/dockerx"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/events"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/store"
)

// ErrQueueFull is returned when the bounded queue has no capacity.
var ErrQueueFull = errors.New("build queue is full")

// ContainerRunner is implemented by dockerx.Client and mocked in tests.
type ContainerRunner interface {
	Run(ctx context.Context, spec dockerx.Spec, write func(string)) (int, error)
}

// Queue is a bounded worker pool.
type Queue struct {
	builds      chan int64
	store       *store.Store
	runner      ContainerRunner
	bus         *events.Bus
	logger      *slog.Logger
	timeout     time.Duration
	maxLogBytes int
	wg          sync.WaitGroup
}

// New builds a queue. workers are started separately with Start.
func New(
	st *store.Store,
	runner ContainerRunner,
	bus *events.Bus,
	logger *slog.Logger,
	queueSize int,
	timeout time.Duration,
	maxLogBytes int,
) *Queue {
	if logger == nil {
		logger = slog.Default()
	}
	if queueSize < 1 {
		queueSize = 1
	}
	return &Queue{
		builds:      make(chan int64, queueSize),
		store:       st,
		runner:      runner,
		bus:         bus,
		logger:      logger,
		timeout:     timeout,
		maxLogBytes: maxLogBytes,
	}
}

// Enqueue attempts to add a build without blocking. The webhook relies on this
// to return 503 quickly when the runner is saturated.
func (q *Queue) Enqueue(_ context.Context, buildID int64) error {
	select {
	case q.builds <- buildID:
		return nil
	default:
		return ErrQueueFull
	}
}

// Start launches the worker goroutines.
func (q *Queue) Start(ctx context.Context, workers int) {
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		q.wg.Add(1)
		go func() {
			defer q.wg.Done()
			q.worker(ctx)
		}()
	}
}

// Wait blocks until all workers have exited.
func (q *Queue) Wait() { q.wg.Wait() }

// Len reports the current queue depth.
func (q *Queue) Len() int { return len(q.builds) }

func (q *Queue) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case buildID := <-q.builds:
			q.process(ctx, buildID)
		}
	}
}

func (q *Queue) process(ctx context.Context, buildID int64) {
	build, err := q.store.GetBuild(ctx, buildID)
	if err != nil {
		q.logger.Error("load build", "build_id", buildID, "error", err)
		return
	}
	if build.Status != store.StatusPending {
		q.logger.Warn("skipping build that is not pending", "build_id", buildID, "status", build.Status)
		return
	}

	project, err := q.store.GetProject(ctx, build.ProjectID)
	if err != nil {
		q.finish(buildID, store.StatusFailed, "\n[runner] cannot load project configuration\n")
		return
	}

	if err := q.store.MarkRunning(ctx, buildID); err != nil {
		q.logger.Error("mark build running", "build_id", buildID, "error", err)
		return
	}
	build.Status = store.StatusRunning
	build.Logs = ""
	q.bus.Publish("build.updated", build)

	sink := newLogSink(q.maxLogBytes, func(chunk string) {
		q.bus.Publish("build.log", map[string]any{
			"build_id": buildID,
			"chunk":    chunk,
		})
	})
	stopFlush := q.startFlusher(buildID, sink)
	defer stopFlush()

	runCtx, cancel := context.WithTimeout(ctx, q.timeout)
	exitCode, runErr := q.runner.Run(runCtx, dockerx.Spec{
		BuildID:      build.ID,
		RepoURL:      project.RepoURL,
		Branch:       buildBranch(build, project),
		Image:        project.Image,
		SetupCommand: project.SetupCommand,
		TestCommand:  project.TestCommand,
		CommitSHA:    build.CommitSHA,
	}, sink.WriteString)
	cancel()

	stopFlush()

	status := store.StatusSuccess
	switch {
	case runErr != nil:
		status = store.StatusFailed
		sink.WriteString(fmt.Sprintf("\n[runner] %v\n", runErr))
	case exitCode != 0:
		status = store.StatusFailed
		sink.WriteString(fmt.Sprintf("\n[runner] command exited with code %d\n", exitCode))
	}

	q.finish(buildID, status, sink.String())
}

func (q *Queue) finish(buildID int64, status, logs string) {
	// Use a fresh context so a shutdown does not discard the final state.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := q.store.FinishBuild(ctx, buildID, status, "", logs); err != nil {
		q.logger.Error("finish build", "build_id", buildID, "status", status, "error", err)
		return
	}

	build, err := q.store.GetBuild(ctx, buildID)
	if err != nil {
		q.logger.Warn("load finished build", "build_id", buildID, "error", err)
		return
	}
	build.Logs = ""
	q.bus.Publish("build.finished", build)
	q.logger.Info("build finished", "build_id", buildID, "status", status)
}

func (q *Queue) startFlusher(buildID int64, sink *logSink) func() {
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				snapshot := sink.String()
				if err := q.store.SetBuildLogs(ctx, buildID, snapshot); err != nil {
					q.logger.Debug("flush build logs", "build_id", buildID, "error", err)
				}
				cancel()
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}
}

func buildBranch(build store.Build, project store.Project) string {
	if branch := strings.TrimPrefix(build.Ref, "refs/heads/"); branch != "" && branch != build.Ref {
		return branch
	}
	if project.Branch != "" && project.Branch != "*" {
		return project.Branch
	}
	return "main"
}

// logSink is a thread-safe, size-capped log accumulator. Every accepted chunk
// is also forwarded to the SSE bus.
type logSink struct {
	mu        sync.Mutex
	buf       strings.Builder
	max       int
	truncated bool
	onChunk   func(string)
}

func newLogSink(max int, onChunk func(string)) *logSink {
	if max < 1 {
		max = 2 * 1024 * 1024
	}
	return &logSink{max: max, onChunk: onChunk}
}

func (s *logSink) Write(p []byte) (int, error) {
	chunk := string(p)

	s.mu.Lock()
	if remaining := s.max - s.buf.Len(); remaining > 0 {
		if len(chunk) > remaining {
			chunk = chunk[:remaining]
			s.truncated = true
		}
		s.buf.WriteString(chunk)
	} else if len(p) > 0 {
		s.truncated = true
		chunk = ""
	}
	s.mu.Unlock()

	if chunk != "" && s.onChunk != nil {
		s.onChunk(chunk)
	}
	return len(p), nil
}

func (s *logSink) WriteString(v string) {
	_, _ = s.Write([]byte(v))
}

func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.buf.String()
	if s.truncated {
		out += "\n[runner] log output truncated\n"
	}
	return out
}
