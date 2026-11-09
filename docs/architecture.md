# Architecture

Micro-CI/CD Runner is a single Go process that serves the dashboard, accepts
GitHub webhooks, persists state in SQLite, and runs one container per build. It
is designed for a single trusted host with Docker Engine.

## Components

| Package | Responsibility |
| --- | --- |
| `cmd/runner` | Startup, configuration, seeding, HTTP server, graceful shutdown, healthcheck mode. |
| `internal/config` | Environment parsing, validation, defaults, and `projects.yaml` loading. |
| `internal/giturl` | Normalises clone/ssh/https URLs to `host/owner/repo`. |
| `internal/store` | SQLite access, migrations, projects, builds, status transitions, stats. |
| `internal/webhook` | HMAC-SHA256 verification, payload validation, build creation, enqueueing. |
| `internal/pipeline` | Bounded queue, worker pool, log sink, status transitions, SSE publishing. |
| `internal/dockerx` | Docker/Moby SDK client, image pull, container lifecycle, log streaming. |
| `internal/events` | In-process pub/sub with non-blocking fan-out. |
| `internal/sse` | Server-Sent Events endpoint with heartbeats. |
| `internal/api` | REST handlers, routing, middleware, CORS, admin auth. |
| `web` + `assets.go` | Static dashboard embedded into the binary. |

## Request flow

```
GitHub
  │  POST /webhook + X-Hub-Signature-256
  ▼
webhook.Handler
  ├─ read body (1 MiB cap)
  ├─ verify HMAC in constant time
  ├─ check X-GitHub-Event == push
  ├─ reject deletes / zero SHA / non-branch refs
  ├─ normalise repository URLs
  ├─ find project by canonical repo_url
  ├─ enforce branch policy
  ├─ INSERT build (pending), idempotent on delivery ID
  ├─ Enqueue(build.id)              ── 503 if the queue is full
  └─ 202 Accepted
        │
        ▼
pipeline.Queue worker
  ├─ pending ──▶ running
  ├─ dockerx.Run
  │    ├─ pull image if absent
  │    ├─ create hardened container
  │    ├─ clone repo into /workspace/repo
  │    ├─ run setup + test command
  │    ├─ stream logs ──▶ logSink ──▶ SSE bus
  │    └─ remove container
  └─ running ──▶ success | failed
```

The webhook returns before the build starts. GitHub therefore sees a fast
response, and slow builds cannot hold the webhook request open.

## Database

SQLite in WAL mode with a single write connection. Timestamps are RFC3339 UTC
strings.

```sql
projects (
  id, name UNIQUE, repo_url UNIQUE, branch, image,
  setup_command, test_command, created_at
)

builds (
  id, project_id → projects(id), commit_sha, ref, status,
  logs, delivery_id, created_at, started_at, finished_at
)
```

Key indexes:

- Unique partial index on `delivery_id` for webhook idempotency.
- `(project_id, created_at DESC)` for project history.
- `status` for filtering and statistics.

Logs are accumulated in memory, flushed to SQLite about once per second while a
build runs, and written once more on completion. A configurable byte cap protects
the database from runaway output.

## Status lifecycle

```
pending ──▶ running ──▶ success
   │            │
   │            └────▶ failed
   └──────────────────▶ failed   (queue full, orphaned after restart)
```

`cancelled` is reserved for future cancellation support. Status transitions are
guarded by SQL `WHERE` clauses so a completed build cannot be finished twice.

## Concurrency and backpressure

- `MAX_CONCURRENT_BUILDS` workers consume from a bounded channel of size
  `QUEUE_SIZE`.
- Enqueueing is non-blocking. If the queue is full the webhook returns `503` so
  GitHub can retry, and the rejected build is marked failed.
- Each worker owns one build and one container at a time.
- SSE subscribers have buffered channels; slow clients are dropped instead of
  blocking workers.

## Failure handling

| Failure | Behaviour |
| --- | --- |
| Invalid signature | `401`, no database write. |
| Duplicate delivery | Existing build returned, no second enqueue. |
| Unknown repository | `202` ignored and logged. |
| Branch mismatch | `202` ignored. |
| Queue full | Build marked failed, webhook returns `503`. |
| Docker error or timeout | Build marked failed with the error appended to logs. |
| Process crash | On restart, pending/running builds are marked failed; stale labelled containers are removed. |
| Slow SSE client | Event dropped for that client; EventSource reconnects. |

## Security boundaries

- The webhook endpoint is public and protected by the shared secret.
- The API is an admin surface; deploy it behind TLS and authentication.
- Build containers are untrusted. They drop all capabilities, cannot gain new
  privileges, have CPU/memory/PID limits, use a `tmpfs` workspace, and never see
  the Docker socket.
- The runner process itself can control Docker, which is root-equivalent on the
  host. Isolate the host accordingly.

See [security.md](security.md) for the operational checklist.
