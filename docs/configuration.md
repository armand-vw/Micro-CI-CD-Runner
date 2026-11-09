# Configuration

Configuration comes from environment variables. Copy `.env.example` to `.env`
and edit it; Docker Compose loads it automatically.

## Environment variables

| Variable | Default | Required | Description |
| --- | --- | --- | --- |
| `PORT` | `8080` | No | HTTP listen port. |
| `DB_PATH` | `./data/runner.db` | No | SQLite file. Parent directories are created. |
| `PROJECTS_FILE` | `./config/projects.yaml` | No | Project definitions. |
| `GITHUB_WEBHOOK_SECRET` | — | **Yes** | Shared HMAC secret. The process refuses to start without it. |
| `GITHUB_TOKEN` | empty | No | Token used for cloning private repositories. |
| `DOCKER_HOST` | SDK default | No | Docker endpoint, e.g. `unix:///var/run/docker.sock` or `tcp://host:2376`. |
| `MAX_CONCURRENT_BUILDS` | `1` | No | Number of build workers. |
| `QUEUE_SIZE` | `100` | No | Pending builds held in memory. |
| `BUILD_TIMEOUT` | `15m` | No | Hard timeout per build. |
| `LOG_MAX_BYTES` | `2097152` | No | Maximum log bytes stored per build (2 MiB). |
| `ADMIN_TOKEN` | empty | No | Bearer token required for `POST /api/builds/{id}/retry`. Empty means the route is open. |
| `PUBLIC_BASE_URL` | empty | No | Informational public URL. |
| `ALLOWED_ORIGINS` | empty | No | Comma-separated CORS allowlist for a remotely hosted dashboard. |
| `LOG_LEVEL` | `info` | No | `debug`, `info`, `warn`, or `error`. |

Notes:

- `MAX_CONCURRENT_BUILDS` above 1 means multiple containers can run at once.
  Size it to the host's CPU, memory, and network.
- `BUILD_TIMEOUT` includes image pull, clone, setup, and tests.
- If `ADMIN_TOKEN` is empty and the API is public, anyone can trigger retries.
  Put authentication in front of the API or set a token.
- `ALLOWED_ORIGINS` accepts exact origins such as
  `https://armand-vw.github.io`. `*` is allowed but not recommended.

## Project definitions

Projects live in `config/projects.yaml`:

```yaml
projects:
  - name: my-app
    repo_url: https://github.com/owner/my-app
    branch: main
    image: node:20-alpine
    setup_command: apk add --no-cache git
    test_command: npm ci && npm test
```

| Field | Default | Description |
| --- | --- | --- |
| `name` | — (required) | Unique display name. |
| `repo_url` | — (required) | Any GitHub clone, SSH, or HTML URL. Normalised on load. |
| `branch` | `main` | Branch to build. Use `*` for every branch. |
| `image` | `node:20-alpine` | Build container image. |
| `setup_command` | `apk add --no-cache git` | Runs before the clone. Useful for installing `git`. |
| `test_command` | `npm ci && npm test` | Runs inside the clone directory. |

### How commands run

The container executes roughly:

```sh
set -eu
# setup_command
git clone --depth 1 --branch "<branch>" "<repo>" /workspace/repo
cd /workspace/repo
# test_command
```

Commands come from the trusted configuration file, not from the webhook payload.
Treat anyone who can edit `projects.yaml` as able to run code on your host.

### Examples

Go project:

```yaml
- name: api
  repo_url: https://github.com/owner/api
  branch: main
  image: golang:1.27-alpine
  setup_command: apk add --no-cache git
  test_command: CGO_ENABLED=0 go test ./...
```

Python project:

```yaml
- name: worker
  repo_url: git@github.com:owner/worker.git
  branch: main
  image: python:3.13-alpine
  setup_command: apk add --no-cache git
  test_command: pip install -r requirements.txt && pytest
```

Private repository:

1. Create a token with read-only repository access.
2. Set `GITHUB_TOKEN` in `.env`.
3. Keep the clone URL clean; the runner supplies credentials through a git
   credential helper.

### Updating projects

Projects are upserted by `repo_url` on startup. Editing the file and restarting
is enough. Removing an entry leaves its historical builds in the database.
