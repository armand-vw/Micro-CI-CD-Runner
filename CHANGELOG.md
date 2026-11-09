# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Planned

- Per-repository pipeline definitions.
- Build cancellation from the dashboard.
- Prometheus metrics and audit logging.
- Optional Postgres backend.

## [0.1.0] - 2026-11-09

### Added

- Go HTTP server with `/webhook`, REST API, SSE stream, and embedded dashboard.
- GitHub push webhook validation with HMAC-SHA256 signatures and delivery-ID
  idempotency.
- SQLite persistence for projects and builds, with migrations, WAL mode, and
  build status transitions.
- Bounded build queue with a configurable worker pool.
- Docker/Moby SDK integration: image pull, isolated container creation, log
  streaming, timeouts, and cleanup.
- Build container hardening: dropped capabilities, `no-new-privileges`, CPU,
  memory, and PID limits, ephemeral `tmpfs` workspace, and a log size cap.
- `projects.yaml` seeding with URL normalisation and per-project image, branch,
  setup command, and test command.
- Real-time dashboard with project sidebar, stats, build filters, live logs,
  retry, dark mode, and reconnect handling.
- Graceful shutdown, stale-container cleanup, and orphaned-build recovery.
- Docker Compose deployment, distroless image, Caddy example, CI, CodeQL,
  release, Pages, and GHCR publishing workflows.
- Tests for webhook signatures, URL normalisation, configuration, persistence,
  and the pipeline queue.
