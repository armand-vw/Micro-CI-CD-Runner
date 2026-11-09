# Security guide

This document explains the operational security model. The reporting policy is
in [SECURITY.md](../SECURITY.md).

## The most important fact

The runner talks to the host Docker daemon to start build containers. Access to
that socket is effectively **root on the host**. Anyone who can make the runner
build an arbitrary image or command can take over the machine.

Therefore:

- `projects.yaml` is a trusted file. Do not let untrusted users edit it.
- The runner's API is an admin surface. Protect it with TLS and authentication.
- Run the runner on a dedicated host or VM when possible.
- Never publish the Docker socket over TCP without mutual TLS.

## What the runner protects against

| Risk | Control |
| --- | --- |
| Forged webhooks | HMAC-SHA256 over the raw body, verified in constant time. |
| Missing secret | Startup fails when `GITHUB_WEBHOOK_SECRET` is empty. |
| Oversized payloads | `http.MaxBytesReader` caps bodies at 1 MiB. |
| Duplicate builds from GitHub retries | Unique delivery ID with idempotent inserts. |
| Pushes to unrelated branches | Per-project branch policy. |
| Unregistered repositories | Repository allowlist through `projects.yaml`. |
| Hostile build code | Capabilities dropped, no privilege escalation, PID/memory/CPU limits, `tmpfs` workspace, no Docker socket inside the build container, hard timeout. |
| Log flooding | Per-build log byte cap and bounded SSE client buffers. |
| Resource exhaustion | Bounded queue, bounded worker pool, request limits. |
| Unauthorised retries | Optional `ADMIN_TOKEN` bearer check. |
| Cross-origin dashboard abuse | Optional `ALLOWED_ORIGINS` allowlist. |

## What it does not protect against

- **Malicious repository code escaping the container.** Containers are a strong
  but imperfect boundary. Keep Docker and the kernel patched.
- **A compromised host.** If the host is compromised, the runner and its secrets
  are compromised.
- **Unauthenticated dashboard access.** There is no built-in user system. Use a
  reverse proxy or VPN.
- **Secret leakage through build output.** A test command can print environment
  variables. Do not pass unnecessary secrets to builds.
- **Denial of service by a trusted repository.** Builds are capped, but repeated
  pushes still consume resources.

## Recommended deployment

1. Put the runner behind a TLS-terminating reverse proxy (Caddy, nginx, Traefik).
2. Require authentication for everything except `/webhook` and `/healthz`. For
   example, use Caddy `basicauth` on the dashboard and API.
3. Set `ADMIN_TOKEN` to a long random value.
4. Leave `ALLOWED_ORIGINS` empty unless you intentionally host the UI elsewhere.
5. Restrict inbound network access to the reverse proxy ports only.
6. Keep the Docker socket local to the host.

Example Caddy snippet with basic auth on the dashboard and API while leaving the
webhook public:

```caddyfile
ci.example.com {
    @public path /webhook /healthz /api/events
    handle @public {
        reverse_proxy runner:8080
    }

    handle {
        basicauth {
            admin $2a$14$replace_with_a_bcrypt_hash
        }
        reverse_proxy runner:8080
    }
}
```

Generate the hash with `caddy hash-password`.

## Secrets

- `GITHUB_WEBHOOK_SECRET`: rotate immediately if it leaks. Update the GitHub
  webhook and restart the runner.
- `GITHUB_TOKEN`: use a fine-grained token with read-only repository contents.
  It is passed only to the build container that clones the repository.
- `ADMIN_TOKEN`: rotate periodically and store it in a password manager.
- Never commit `.env`. It is already ignored by git.

## Build container hardening

The runner creates build containers with:

- `CapDrop: ["ALL"]`
- `SecurityOpt: ["no-new-privileges:true"]`
- memory limit of 512 MiB
- one CPU
- a PID limit of 256
- a 512 MiB `tmpfs` workspace
- bridge networking (egress is needed to clone and download dependencies)
- no Docker socket, no host mounts, no privileged mode
- automatic removal after the build

If you need stricter isolation, run the entire runner inside a dedicated VM and
consider rootless Docker or a disposable worker host.

## Monitoring

- Watch for repeated `401` responses on `/webhook` (secret mismatch or attack).
- Watch for `503` responses (queue saturation) and long-running builds.
- Keep an eye on disk usage: SQLite holds build logs, and images accumulate on
  the host. Prune unused images periodically.

## Incident response

1. Stop the runner: `docker compose -f deploy/docker-compose.yml down`.
2. Rotate the webhook secret and `ADMIN_TOKEN`.
3. Revoke any exposed GitHub token.
4. Preserve the SQLite database and container logs for analysis.
5. Rebuild the host if you suspect container escape or daemon compromise.
