package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Build statuses. The database enforces the same set with a CHECK constraint.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusSuccess   = "success"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// ErrInvalidTransition is returned when a status update does not match the
// current state (for example finishing a build that was never started).
var ErrInvalidTransition = errors.New("invalid build status transition")

// Build is one execution of a project.
type Build struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	ProjectName string `json:"project_name,omitempty"`
	CommitSHA   string `json:"commit_sha"`
	Ref         string `json:"ref"`
	Status      string `json:"status"`
	Logs        string `json:"logs,omitempty"`
	CreatedAt   string `json:"created_at"`
	StartedAt   string `json:"started_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	DeliveryID  string `json:"-"`
}

// BuildFilter narrows a build listing.
type BuildFilter struct {
	ProjectID int64
	Status    string
	Limit     int
	Offset    int
}

// Stats is an aggregate snapshot used by the dashboard cards.
type Stats struct {
	Total   int64 `json:"total"`
	Pending int64 `json:"pending"`
	Running int64 `json:"running"`
	Success int64 `json:"success"`
	Failed  int64 `json:"failed"`
}

// CreateBuild inserts a pending build. When the GitHub delivery ID has already
// been seen, the existing build is returned and created is false.
func (s *Store) CreateBuild(ctx context.Context, b Build) (Build, bool, error) {
	if b.CreatedAt == "" {
		b.CreatedAt = now()
	}

	var delivery any
	if b.DeliveryID != "" {
		delivery = b.DeliveryID
	}

	const q = `
		INSERT INTO builds (project_id, commit_sha, ref, status, delivery_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`

	res, err := s.db.ExecContext(ctx, q,
		b.ProjectID, b.CommitSHA, b.Ref, StatusPending, delivery, b.CreatedAt,
	)
	if err != nil {
		return Build{}, false, fmt.Errorf("create build: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return Build{}, false, fmt.Errorf("create build rows affected: %w", err)
	}
	if affected == 0 {
		if b.DeliveryID == "" {
			return Build{}, false, errors.New("create build conflicted without a delivery ID")
		}
		existing, err := s.buildByDelivery(ctx, b.DeliveryID)
		if err != nil {
			return Build{}, false, err
		}
		return existing, false, nil
	}

	id, err := res.LastInsertId()
	if err != nil {
		return Build{}, false, fmt.Errorf("create build id: %w", err)
	}
	b.ID = id
	b.Status = StatusPending
	return b, true, nil
}

// buildByDelivery loads a build by its GitHub delivery ID.
func (s *Store) buildByDelivery(ctx context.Context, deliveryID string) (Build, error) {
	const q = `
		SELECT b.id, b.project_id, p.name, b.commit_sha, b.ref, b.status, b.logs,
		       b.created_at, COALESCE(b.started_at, ''), COALESCE(b.finished_at, ''),
		       COALESCE(b.delivery_id, '')
		FROM builds b JOIN projects p ON p.id = b.project_id
		WHERE b.delivery_id = ? LIMIT 1`
	return scanBuild(s.db.QueryRowContext(ctx, q, deliveryID), true)
}

// GetBuild loads a build including its logs.
func (s *Store) GetBuild(ctx context.Context, id int64) (Build, error) {
	const q = `
		SELECT b.id, b.project_id, p.name, b.commit_sha, b.ref, b.status, b.logs,
		       b.created_at, COALESCE(b.started_at, ''), COALESCE(b.finished_at, ''),
		       COALESCE(b.delivery_id, '')
		FROM builds b JOIN projects p ON p.id = b.project_id
		WHERE b.id = ?`
	return scanBuild(s.db.QueryRowContext(ctx, q, id), true)
}

// ListBuilds returns builds without their log bodies, newest first.
func (s *Store) ListBuilds(ctx context.Context, f BuildFilter) ([]Build, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	where := []string{"1 = 1"}
	args := make([]any, 0, 4)
	if f.ProjectID > 0 {
		where = append(where, "b.project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Status != "" {
		if !validStatus(f.Status) {
			return nil, fmt.Errorf("invalid status filter %q", f.Status)
		}
		where = append(where, "b.status = ?")
		args = append(args, f.Status)
	}
	args = append(args, f.Limit, f.Offset)

	q := fmt.Sprintf(`
		SELECT b.id, b.project_id, p.name, b.commit_sha, b.ref, b.status,
		       b.created_at, COALESCE(b.started_at, ''), COALESCE(b.finished_at, ''),
		       COALESCE(b.delivery_id, '')
		FROM builds b JOIN projects p ON p.id = b.project_id
		WHERE %s
		ORDER BY b.created_at DESC, b.id DESC
		LIMIT ? OFFSET ?`, strings.Join(where, " AND "))

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list builds: %w", err)
	}
	defer rows.Close()

	var builds []Build
	for rows.Next() {
		var b Build
		if err := rows.Scan(
			&b.ID, &b.ProjectID, &b.ProjectName, &b.CommitSHA, &b.Ref, &b.Status,
			&b.CreatedAt, &b.StartedAt, &b.FinishedAt, &b.DeliveryID,
		); err != nil {
			return nil, fmt.Errorf("scan build: %w", err)
		}
		builds = append(builds, b)
	}
	return builds, rows.Err()
}

// MarkRunning transitions a pending build to running.
func (s *Store) MarkRunning(ctx context.Context, id int64) error {
	const q = `UPDATE builds SET status = ?, started_at = ? WHERE id = ? AND status = ?`
	res, err := s.db.ExecContext(ctx, q, StatusRunning, now(), id, StatusPending)
	if err != nil {
		return fmt.Errorf("mark build running: %w", err)
	}
	return requireAffected(res, id)
}

// SetBuildLogs persists an intermediate log snapshot while a build is running.
// Final logs are written by FinishBuild so the two writers cannot race.
func (s *Store) SetBuildLogs(ctx context.Context, id int64, logs string) error {
	const q = `UPDATE builds SET logs = ? WHERE id = ? AND status = ?`
	res, err := s.db.ExecContext(ctx, q, logs, id, StatusRunning)
	if err != nil {
		return fmt.Errorf("set build logs: %w", err)
	}
	_, _ = res.RowsAffected()
	return nil
}

// FinishBuild writes the terminal status and final log body.
func (s *Store) FinishBuild(ctx context.Context, id int64, status, finishedAt, logs string) error {
	if status != StatusSuccess && status != StatusFailed && status != StatusCancelled {
		return fmt.Errorf("invalid terminal status %q", status)
	}
	if finishedAt == "" {
		finishedAt = now()
	}
	const q = `
		UPDATE builds SET status = ?, finished_at = ?, logs = ?
		WHERE id = ? AND status IN (?, ?)`
	res, err := s.db.ExecContext(ctx, q, status, finishedAt, logs, id, StatusPending, StatusRunning)
	if err != nil {
		return fmt.Errorf("finish build: %w", err)
	}
	return requireAffected(res, id)
}

// MarkOrphanedBuilds fails builds that were pending or running when the process
// stopped. Their queue and containers are gone after a restart.
func (s *Store) MarkOrphanedBuilds(ctx context.Context, note string) (int64, error) {
	const q = `
		UPDATE builds SET status = ?, finished_at = ?, logs = logs || ?
		WHERE status IN (?, ?)`
	res, err := s.db.ExecContext(ctx, q, StatusFailed, now(), note, StatusPending, StatusRunning)
	if err != nil {
		return 0, fmt.Errorf("mark orphaned builds: %w", err)
	}
	return res.RowsAffected()
}

// Stats returns aggregate build counts.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	const q = `
		SELECT COUNT(*),
		       COALESCE(SUM(status = ?), 0),
		       COALESCE(SUM(status = ?), 0),
		       COALESCE(SUM(status = ?), 0),
		       COALESCE(SUM(status = ?), 0)
		FROM builds`
	var st Stats
	err := s.db.QueryRowContext(ctx, q,
		StatusPending, StatusRunning, StatusSuccess, StatusFailed,
	).Scan(&st.Total, &st.Pending, &st.Running, &st.Success, &st.Failed)
	if err != nil {
		return Stats{}, fmt.Errorf("build stats: %w", err)
	}
	return st, nil
}

func scanBuild(row *sql.Row, withLogs bool) (Build, error) {
	var b Build
	var err error
	if withLogs {
		err = row.Scan(
			&b.ID, &b.ProjectID, &b.ProjectName, &b.CommitSHA, &b.Ref, &b.Status, &b.Logs,
			&b.CreatedAt, &b.StartedAt, &b.FinishedAt, &b.DeliveryID,
		)
	} else {
		err = row.Scan(
			&b.ID, &b.ProjectID, &b.ProjectName, &b.CommitSHA, &b.Ref, &b.Status,
			&b.CreatedAt, &b.StartedAt, &b.FinishedAt, &b.DeliveryID,
		)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Build{}, ErrNotFound
	}
	if err != nil {
		return Build{}, fmt.Errorf("get build: %w", err)
	}
	return b, nil
}

func requireAffected(res sql.Result, id int64) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w for build %d", ErrInvalidTransition, id)
	}
	return nil
}

func validStatus(status string) bool {
	switch status {
	case StatusPending, StatusRunning, StatusSuccess, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}
