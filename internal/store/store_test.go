package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "runner.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedProject(t *testing.T, s *Store) Project {
	t.Helper()
	err := s.UpsertProjects(context.Background(), []Project{{
		Name:         "demo",
		RepoURL:      "github.com/owner/demo",
		Branch:       "main",
		Image:        "node:20-alpine",
		SetupCommand: "apk add --no-cache git",
		TestCommand:  "npm test",
	}})
	if err != nil {
		t.Fatalf("UpsertProjects: %v", err)
	}
	projects, err := s.ListProjects(context.Background())
	if err != nil || len(projects) != 1 {
		t.Fatalf("ListProjects: %v, %d projects", err, len(projects))
	}
	return projects[0]
}

func TestProjectUpsertAndFind(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	project := seedProject(t, s)

	ctx := context.Background()
	found, err := s.FindProjectByRepo(ctx, []string{"github.com/owner/demo"})
	if err != nil {
		t.Fatalf("FindProjectByRepo: %v", err)
	}
	if found.ID != project.ID || found.Name != "demo" {
		t.Fatalf("found = %+v, want project %d", found, project.ID)
	}

	// Upserting the same repo must not create a second row.
	if err := s.UpsertProjects(ctx, []Project{{
		Name: "renamed", RepoURL: "github.com/owner/demo", Branch: "dev",
		Image: "node:20-alpine", SetupCommand: "true", TestCommand: "npm test",
	}}); err != nil {
		t.Fatalf("UpsertProjects update: %v", err)
	}
	projects, err := s.ListProjects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("ListProjects after upsert: %v, %d", err, len(projects))
	}
	if projects[0].Name != "renamed" || projects[0].Branch != "dev" {
		t.Fatalf("project not updated: %+v", projects[0])
	}

	if _, err := s.FindProjectByRepo(ctx, []string{"github.com/owner/missing"}); err != ErrNotFound {
		t.Fatalf("missing project err = %v, want ErrNotFound", err)
	}
}

func TestBuildLifecycle(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	project := seedProject(t, s)
	ctx := context.Background()

	build, created, err := s.CreateBuild(ctx, Build{
		ProjectID:  project.ID,
		CommitSHA:  "abcdef123456",
		Ref:        "refs/heads/main",
		DeliveryID: "delivery-1",
	})
	if err != nil || !created {
		t.Fatalf("CreateBuild: created=%v err=%v", created, err)
	}

	duplicate, createdAgain, err := s.CreateBuild(ctx, Build{
		ProjectID: project.ID, CommitSHA: "other", DeliveryID: "delivery-1",
	})
	if err != nil {
		t.Fatalf("duplicate CreateBuild: %v", err)
	}
	if createdAgain {
		t.Fatal("duplicate delivery must not create a second build")
	}
	if duplicate.ID != build.ID {
		t.Fatalf("duplicate ID = %d, want %d", duplicate.ID, build.ID)
	}

	if err := s.MarkRunning(ctx, build.ID); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := s.SetBuildLogs(ctx, build.ID, "partial output"); err != nil {
		t.Fatalf("SetBuildLogs: %v", err)
	}
	if err := s.FinishBuild(ctx, build.ID, StatusSuccess, "", "final output"); err != nil {
		t.Fatalf("FinishBuild: %v", err)
	}

	got, err := s.GetBuild(ctx, build.ID)
	if err != nil {
		t.Fatalf("GetBuild: %v", err)
	}
	if got.Status != StatusSuccess {
		t.Fatalf("status = %q, want success", got.Status)
	}
	if got.Logs != "final output" {
		t.Fatalf("logs = %q, want final output", got.Logs)
	}
	if got.ProjectName != "demo" {
		t.Fatalf("project name = %q, want demo", got.ProjectName)
	}
	if got.StartedAt == "" || got.FinishedAt == "" {
		t.Fatalf("timestamps not recorded: %+v", got)
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Total != 1 || stats.Success != 1 || stats.Running != 0 {
		t.Fatalf("stats = %+v", stats)
	}

	if err := s.FinishBuild(ctx, build.ID, StatusFailed, "", "again"); err == nil {
		t.Fatal("finishing a completed build should fail")
	}
}

func TestListBuildsAndOrphans(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	project := seedProject(t, s)
	ctx := context.Background()

	first, _, err := s.CreateBuild(ctx, Build{ProjectID: project.ID, CommitSHA: "1", Ref: "refs/heads/main"})
	if err != nil {
		t.Fatalf("CreateBuild first: %v", err)
	}
	second, _, err := s.CreateBuild(ctx, Build{ProjectID: project.ID, CommitSHA: "2", Ref: "refs/heads/main"})
	if err != nil {
		t.Fatalf("CreateBuild second: %v", err)
	}
	if err := s.MarkRunning(ctx, first.ID); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	builds, err := s.ListBuilds(ctx, BuildFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListBuilds: %v", err)
	}
	if len(builds) != 2 {
		t.Fatalf("ListBuilds len = %d, want 2", len(builds))
	}
	if builds[0].Logs != "" {
		t.Fatal("list results must not include log bodies")
	}

	running, err := s.ListBuilds(ctx, BuildFilter{Status: StatusRunning})
	if err != nil || len(running) != 1 || running[0].ID != first.ID {
		t.Fatalf("running filter = %+v, err=%v", running, err)
	}

	note := "\n[runner] restarted\n"
	n, err := s.MarkOrphanedBuilds(ctx, note)
	if err != nil {
		t.Fatalf("MarkOrphanedBuilds: %v", err)
	}
	if n != 2 {
		t.Fatalf("orphaned = %d, want 2", n)
	}
	for _, id := range []int64{first.ID, second.ID} {
		build, err := s.GetBuild(ctx, id)
		if err != nil {
			t.Fatalf("GetBuild %d: %v", id, err)
		}
		if build.Status != StatusFailed || !strings.Contains(build.Logs, note) {
			t.Fatalf("build %d not marked orphaned: %+v", id, build)
		}
	}
}
