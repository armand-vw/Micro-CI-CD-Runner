//go:build docker

package dockerx

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// TestRunContainerEndToEnd exercises the real Docker path: image pull,
// container creation, repository clone, command execution, log streaming, and
// cleanup.
//
// It is excluded from normal test runs by the "docker" build tag and only runs
// when RUNNER_DOCKER_TEST=1. CI runs it on GitHub's Docker-enabled runners.
func TestRunContainerEndToEnd(t *testing.T) {
	if os.Getenv("RUNNER_DOCKER_TEST") != "1" {
		t.Skip("set RUNNER_DOCKER_TEST=1 to run the Docker integration test")
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	docker, err := New("", "", logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = docker.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := docker.Ping(ctx); err != nil {
		t.Fatalf("docker daemon unavailable: %v", err)
	}

	buildID := time.Now().UnixNano()
	spec := Spec{
		BuildID:      buildID,
		RepoURL:      "github.com/octocat/Hello-World",
		Branch:       "master",
		Image:        "alpine:3.20",
		SetupCommand: "apk add --no-cache git",
		TestCommand:  "test -s README && git rev-parse --verify HEAD",
		CommitSHA:    "integration-test",
	}

	var (
		mu   sync.Mutex
		logs strings.Builder
	)
	exitCode, err := docker.Run(ctx, spec, func(chunk string) {
		mu.Lock()
		logs.WriteString(chunk)
		mu.Unlock()
	})
	mu.Lock()
	output := logs.String()
	mu.Unlock()

	if err != nil {
		t.Fatalf("Run: %v\nlogs:\n%s", err, output)
	}
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0\nlogs:\n%s", exitCode, output)
	}
	if output == "" {
		t.Fatal("expected container output, got none")
	}

	// The build container must be removed before Run returns.
	containers, err := docker.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", "microci.build_id="+strconv.FormatInt(buildID, 10)),
	})
	if err != nil {
		t.Fatalf("ContainerList: %v", err)
	}
	if len(containers.Items) != 0 {
		for _, container := range containers.Items {
			_, _ = docker.cli.ContainerRemove(context.Background(), container.ID, client.ContainerRemoveOptions{Force: true})
		}
		t.Fatalf("expected build container to be removed, found %d", len(containers.Items))
	}

	t.Logf("container output:\n%s", output)
}
