# Deployment

The runner needs a Linux host with Docker Engine and a public HTTPS URL for
GitHub webhooks. A small VPS, a homelab server, or a NAS with Docker works.

## 1. Prepare the host

- Install Docker Engine and the Compose plugin.
- Ensure the user running Compose can access the Docker socket.
- Open only the port used by your reverse proxy or tunnel. Do not expose the
  Docker socket over the network.

## 2. Configure

```bash
git clone https://github.com/armand-vw/Micro-CI-CD-Runner.git
cd Micro-CI-CD-Runner
cp .env.example .env
```

Generate a webhook secret and set it in `.env`:

```bash
openssl rand -hex 32
```

Edit `config/projects.yaml` for your repositories. The checked-in entry builds
this project itself.

## 3. Start with Docker Compose

```bash
docker compose -f deploy/docker-compose.yml up -d --build
docker compose -f deploy/docker-compose.yml ps
curl -fsS http://localhost:8080/healthz
```

The Compose file:

- mounts `/var/run/docker.sock` so the runner can create build containers,
- persists SQLite in the `runner-data` volume,
- mounts `config/` read-only,
- runs a built-in healthcheck.

## 4. Publish an HTTPS endpoint

### Option A: reverse proxy with Caddy

Point a DNS record at the host and use the example Caddyfile:

```bash
caddy run --config deploy/Caddyfile
```

Change `ci.example.com` to your domain. Caddy obtains and renews certificates
automatically.

### Option B: tunnel (no public IP)

```bash
cloudflared tunnel --url http://localhost:8080
```

Use the generated `https://…trycloudflare.com` host. For production, configure a
named Cloudflare Tunnel instead of a quick tunnel.

### Option C: ngrok

```bash
ngrok http 8080
```

Quick tunnels are suitable for testing, not long-term operations.

## 5. Configure the GitHub webhook

Repository **Settings → Webhooks → Add webhook**:

- **Payload URL:** `https://<your-host>/webhook`
- **Content type:** `application/json`
- **Secret:** the value of `GITHUB_WEBHOOK_SECRET`
- **SSL verification:** enabled
- **Events:** just the push event

GitHub sends a `ping` event first; the runner replies `200 pong`.

## 6. Verify

Push a commit to the configured branch. In the dashboard:

1. A build appears as `pending`.
2. The worker moves it to `running`.
3. Logs stream into the build drawer.
4. The build ends as `success` or `failed`.

If nothing happens, check:

- The webhook delivery response in GitHub (should be `202`).
- Runner logs: `docker compose -f deploy/docker-compose.yml logs -f runner`.
- That `repo_url`, branch, and the webhook secret match.

## Operations

### Logs

```bash
docker compose -f deploy/docker-compose.yml logs -f runner
```

### Backups

The SQLite database lives in the `runner-data` volume at `/data/runner.db`.
Back it up with a volume copy or by running `sqlite3 .backup` inside a helper
container. Stop the runner for a consistent file copy if you cannot use the
SQLite backup API.

### Upgrades

```bash
git pull
docker compose -f deploy/docker-compose.yml up -d --build
```

Migrations run automatically at startup. Pending and running builds are marked
failed across a restart, and stale build containers are removed.

### Resource tuning

- `MAX_CONCURRENT_BUILDS=1` is the safe default.
- Give the host enough CPU and RAM for the runner plus concurrent build
  containers (each is capped at 1 CPU and 512 MiB inside the container, but
  image pulls and clones use additional disk and network).
- Set `BUILD_TIMEOUT` conservatively.

### Running without Compose

```bash
cp .env.example .env
make build
./bin/runner
```

The process needs access to the Docker socket and to the paths referenced by
`DB_PATH` and `PROJECTS_FILE`.
