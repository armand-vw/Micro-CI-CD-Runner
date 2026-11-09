package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Project is a repository the runner is allowed to build.
type Project struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	RepoURL      string `json:"repo_url"`
	Branch       string `json:"branch"`
	Image        string `json:"image"`
	SetupCommand string `json:"setup_command"`
	TestCommand  string `json:"test_command"`
	CreatedAt    string `json:"created_at"`
}

// UpsertProjects inserts or updates projects, matched by repo_url. Existing
// rows keep their ID and therefore their build history.
func (s *Store) UpsertProjects(ctx context.Context, projects []Project) error {
	if len(projects) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin projects transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const q = `
		INSERT INTO projects (name, repo_url, branch, image, setup_command, test_command, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_url) DO UPDATE SET
			name = excluded.name,
			branch = excluded.branch,
			image = excluded.image,
			setup_command = excluded.setup_command,
			test_command = excluded.test_command`

	for _, p := range projects {
		if _, err := tx.ExecContext(ctx, q,
			p.Name, p.RepoURL, p.Branch, p.Image, p.SetupCommand, p.TestCommand, now(),
		); err != nil {
			return fmt.Errorf("upsert project %q: %w", p.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit projects transaction: %w", err)
	}
	return nil
}

// ListProjects returns all projects ordered by name.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	const q = `
		SELECT id, name, repo_url, branch, image, setup_command, test_command, created_at
		FROM projects ORDER BY name`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(
			&p.ID, &p.Name, &p.RepoURL, &p.Branch, &p.Image,
			&p.SetupCommand, &p.TestCommand, &p.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// GetProject fetches one project by ID.
func (s *Store) GetProject(ctx context.Context, id int64) (Project, error) {
	const q = `
		SELECT id, name, repo_url, branch, image, setup_command, test_command, created_at
		FROM projects WHERE id = ?`
	return scanProject(s.db.QueryRowContext(ctx, q, id))
}

// FindProjectByRepo returns the first project matching any of the supplied
// canonical repository URLs. GitHub reports several equivalent URLs in a single
// payload, so accepting a set keeps matching robust.
func (s *Store) FindProjectByRepo(ctx context.Context, repos []string) (Project, error) {
	unique := make([]string, 0, len(repos))
	seen := make(map[string]struct{}, len(repos))
	for _, repo := range repos {
		if repo = strings.TrimSpace(repo); repo != "" {
			if _, ok := seen[repo]; !ok {
				seen[repo] = struct{}{}
				unique = append(unique, repo)
			}
		}
	}
	if len(unique) == 0 {
		return Project{}, ErrNotFound
	}

	placeholders := make([]string, len(unique))
	args := make([]any, len(unique))
	for i, repo := range unique {
		placeholders[i] = "?"
		args[i] = repo
	}

	q := fmt.Sprintf(`
		SELECT id, name, repo_url, branch, image, setup_command, test_command, created_at
		FROM projects WHERE repo_url IN (%s) LIMIT 1`, strings.Join(placeholders, ", "))

	return scanProject(s.db.QueryRowContext(ctx, q, args...))
}

func scanProject(row *sql.Row) (Project, error) {
	var p Project
	err := row.Scan(
		&p.ID, &p.Name, &p.RepoURL, &p.Branch, &p.Image,
		&p.SetupCommand, &p.TestCommand, &p.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}
