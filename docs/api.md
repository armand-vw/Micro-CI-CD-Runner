# HTTP API

Base URL: the runner's address, for example `http://localhost:8080`.

All responses are JSON unless noted. Errors use:

```json
{ "error": "human readable message" }
```

## Webhook

### `POST /webhook`

Receives GitHub push events. Requires the `X-GitHub-Event` header and a valid
`X-Hub-Signature-256` header computed with `GITHUB_WEBHOOK_SECRET`.

Responses:

| Status | Meaning |
| --- | --- |
| `202` | Build queued, duplicate delivery, or ignored push. |
| `200` | Ping or unsupported event. |
| `400` | Missing/invalid headers or JSON. |
| `401` | Invalid signature. |
| `413` | Body larger than 1 MiB. |
| `503` | Queue full; GitHub may retry. |

```json
{ "status": "queued", "build_id": 42 }
```

Other possible `status` values: `pong`, `ignored`, `duplicate`.

## Health

### `GET /healthz`

```json
{ "status": "ok", "queue_len": 0 }
```

Returns `503` with `{"status":"unhealthy"}` if SQLite is unavailable.

## Projects

### `GET /api/projects`

```json
{
  "projects": [
    {
      "id": 1,
      "name": "micro-ci-cd-runner",
      "repo_url": "github.com/armand-vw/Micro-CI-CD-Runner",
      "branch": "main",
      "image": "golang:1.27-alpine",
      "setup_command": "apk add --no-cache git",
      "test_command": "go test ./...",
      "created_at": "2026-11-09T10:00:00Z"
    }
  ]
}
```

## Stats

### `GET /api/stats`

```json
{ "total": 12, "pending": 0, "running": 1, "success": 9, "failed": 2 }
```

## Builds

### `GET /api/builds`

Query parameters:

| Parameter | Description |
| --- | --- |
| `project_id` | Filter by project. |
| `status` | `pending`, `running`, `success`, `failed`, or `cancelled`. |
| `limit` | 1–200, default 50. |
| `offset` | Paging offset, default 0. |

```json
{
  "builds": [
    {
      "id": 42,
      "project_id": 1,
      "project_name": "micro-ci-cd-runner",
      "commit_sha": "abcdef...",
      "ref": "refs/heads/main",
      "status": "running",
      "created_at": "2026-11-09T10:00:00Z",
      "started_at": "2026-11-09T10:00:02Z"
    }
  ]
}
```

Log bodies are omitted from list responses.

### `GET /api/builds/{id}`

Returns one build without its log body. `404` if it does not exist.

### `GET /api/builds/{id}/logs`

Returns `text/plain` logs. Add `?download=1` to receive an attachment named
`build-<id>.log`.

### `POST /api/builds/{id}/retry`

Creates a new pending build for the same project, commit, and ref, then queues
it. If `ADMIN_TOKEN` is set, send:

```
Authorization: Bearer <token>
```

Responses: `202` with `{"status":"queued","build_id":43}`, `401`, `404`, or
`503` when the queue is full.

## Server-Sent Events

### `GET /api/events`

`Content-Type: text/event-stream`. The stream sends a heartbeat comment every
15 seconds and reconnects automatically via the browser's `EventSource`.

Events:

| Event | Data |
| --- | --- |
| `build.created` | Build object (pending). |
| `build.updated` | Build object (typically running). |
| `build.finished` | Build object with terminal status. |
| `build.log` | `{"build_id": 42, "chunk": "..."}` |

Example:

```
id: 17
event: build.log
data: {"build_id":42,"chunk":"PASS src/app.test.js\n"}

```

Logs are also persisted, so opening a build loads its existing output before
live chunks are appended.

## Running the API behind a proxy

- Disable response buffering for `/api/events`.
- Forward `X-Forwarded-For` if you log client addresses.
- Keep `/webhook` public but protect the rest of the API.
