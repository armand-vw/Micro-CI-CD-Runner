package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProjectsDefaultsAndNormalisation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "projects.yaml")
	content := `projects:
  - name: demo
    repo_url: git@github.com:Owner/Repo.git
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	projects, err := LoadProjects(path)
	if err != nil {
		t.Fatalf("LoadProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("len = %d, want 1", len(projects))
	}

	p := projects[0]
	if p.RepoURL != "github.com/owner/repo" {
		t.Fatalf("repo_url = %q", p.RepoURL)
	}
	if p.Branch != "main" {
		t.Fatalf("branch = %q, want main", p.Branch)
	}
	if p.Image != "node:20-alpine" {
		t.Fatalf("image = %q", p.Image)
	}
	if p.SetupCommand == "" || p.TestCommand == "" {
		t.Fatalf("commands not defaulted: %+v", p)
	}
}

func TestLoadProjectsValidation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "projects.yaml")
	content := `projects:
  - repo_url: https://github.com/owner/repo
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := LoadProjects(path); err == nil {
		t.Fatal("expected an error for a project without a name")
	}
}
