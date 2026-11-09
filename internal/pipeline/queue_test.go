package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/armand-vw/Micro-CI-CD-Runner/internal/dockerx"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/events"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/store"
)

type fakeRunner struct {
	exit   int
	err    error
	output string
}

func (f *fakeRunner) Run(_ context.Context, _ dockerx.Spec, write func(string)) (int, error) {
	if f.output != "" {
		write(f.output)
	}
	return f.exit, f.err
}

func testQueue(t *testing.T, runner ContainerRunner, queueSize int) (*Queue, *store.Store, store.Build, context.Context, context.CancelFunc) {
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

	q := New(db, runner, events.NewBus(), slog.Default(), queueSize, 5*time.Second, 4096)
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return q, db, build, runCtx, cancel
}

func TestQueueSuccess(t *testing.T) {
	t.Parallel()
	q, db, build, ctx, cancel := testQueue(t, &fakeRunner{output: "hello from the build\n"}, 4)
	q.Start(ctx, 1)
	defer func() { cancel(); q.Wait() }()

	if err := q.Enqueue(ctx, build.ID); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	got := waitForStatus(t, db, build.ID, store.StatusSuccess)
	if !strings.Contains(got.Logs, "hello from the build") {
		t.Fatalf("logs = %q", got.Logs)
	}
}

func TestQueueNonZeroExitFails(t *testing.T) {
	t.Parallel()
	q, db, build, ctx, cancel := testQueue(t, &fakeRunner{exit: 3, output: "tests failed\n"}, 4)
	q.Start(ctx, 1)
	defer func() { cancel(); q.Wait() }()

	if err := q.Enqueue(ctx, build.ID); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	got := waitForStatus(t, db, build.ID, store.StatusFailed)
	if !strings.Contains(got.Logs, "exited with code 3") {
		t.Fatalf("logs = %q", got.Logs)
	}
}

func TestQueueRunnerErrorFails(t *testing.T) {
	t.Parallel()
	q, db, build, ctx, cancel := testQueue(t, &fakeRunner{err: errors.New("docker exploded")}, 4)
	q.Start(ctx, 1)
	defer func() { cancel(); q.Wait() }()

	if err := q.Enqueue(ctx, build.ID); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	got := waitForStatus(t, db, build.ID, store.StatusFailed)
	if !strings.Contains(got.Logs, "docker exploded") {
		t.Fatalf("logs = %q", got.Logs)
	}
}

func TestQueueFull(t *testing.T) {
	t.Parallel()
	q, _, _, _, cancel := testQueue(t, &fakeRunner{}, 1)
	defer cancel()

	if err := q.Enqueue(context.Background(), 1); err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}
	if err := q.Enqueue(context.Background(), 2); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("second Enqueue err = %v, want ErrQueueFull", err)
	}
}

func waitForStatus(t *testing.T, db *store.Store, id int64, want string) store.Build {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		build, err := db.GetBuild(context.Background(), id)
		if err != nil {
			t.Fatalf("GetBuild: %v", err)
		}
		if build.Status == want {
			return build
		}
		time.Sleep(10 * time.Millisecond)
	}
	build, _ := db.GetBuild(context.Background(), id)
	t.Fatalf("build status = %q, want %q (logs: %s)", build.Status, want, build.Logs)
	return store.Build{}
}
