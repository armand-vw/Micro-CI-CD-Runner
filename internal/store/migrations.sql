-- Schema for the Micro-CI/CD Runner.
-- Timestamps are stored as RFC3339 UTC strings for easy JSON round-tripping.

CREATE TABLE IF NOT EXISTS projects (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  name          TEXT NOT NULL UNIQUE,
  repo_url      TEXT NOT NULL UNIQUE,
  branch        TEXT NOT NULL DEFAULT 'main',
  image         TEXT NOT NULL DEFAULT 'node:20-alpine',
  setup_command TEXT NOT NULL DEFAULT 'apk add --no-cache git',
  test_command  TEXT NOT NULL DEFAULT 'npm ci && npm test',
  created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS builds (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  commit_sha  TEXT NOT NULL,
  ref         TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'pending'
              CHECK (status IN ('pending', 'running', 'success', 'failed', 'cancelled')),
  logs        TEXT NOT NULL DEFAULT '',
  delivery_id TEXT,
  created_at  TEXT NOT NULL,
  started_at  TEXT,
  finished_at TEXT
);

-- GitHub retries webhooks, so the delivery ID is used to make ingestion
-- idempotent. Partial indexes are supported by modernc.org/sqlite.
CREATE UNIQUE INDEX IF NOT EXISTS idx_builds_delivery
  ON builds(delivery_id) WHERE delivery_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_builds_project_created
  ON builds(project_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_builds_status ON builds(status);
