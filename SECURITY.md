# Security Policy

## Supported versions

Security fixes are applied to the latest release on `main`.

| Version | Supported |
| --- | --- |
| 0.1.x | Yes |
| Older | No |

## Reporting a vulnerability

Please **do not** open a public issue for a security problem. Use GitHub's
[private vulnerability reporting](https://github.com/armand-vw/Micro-CI-CD-Runner/security/advisories/new)
or contact the maintainer through the repository's private channels.

Include:

- A description of the issue and its impact.
- Steps to reproduce or a proof of concept.
- Affected versions and configuration.
- Any suggested remediation.

You can expect an acknowledgement within a few days. Please allow time for a fix
and a coordinated disclosure before publishing details.

## Threat model

This runner is designed for a trusted operator on a trusted host. Understand
these boundaries before exposing it:

1. **Docker daemon access is root-equivalent.** Anyone who can call the runner's
   build path can ultimately run code on the host through the daemon. The runner
   only executes images and commands from the trusted `projects.yaml`, but the
   API and webhook surfaces must remain protected.
2. **The webhook endpoint is public by design.** It is protected by the GitHub
   HMAC secret. Invalid signatures are rejected before parsing.
3. **Build code is untrusted.** Build containers run repository code. They are
   isolated with dropped capabilities, no privilege escalation, resource limits,
   a short-lived `tmpfs` workspace, and a hard timeout, but containers are not a
   perfect security boundary. The Docker socket is not mounted into build
   containers.
4. **The dashboard is an admin surface.** Do not expose `/api/*` to the public
   internet without HTTPS and authentication at a reverse proxy, and set
   `ADMIN_TOKEN` if retries are enabled.

## Implemented controls

- HMAC-SHA256 verification with `hmac.Equal` (constant-time) over the raw body.
- Required `GITHUB_WEBHOOK_SECRET`; the process refuses to start without it.
- `X-GitHub-Event` validation and rejection of deleted/zero-SHA pushes.
- 1 MiB request body cap via `http.MaxBytesReader`.
- Delivery-ID idempotency to prevent duplicate builds on retries.
- Branch and repository allowlisting through `projects.yaml`.
- Bounded queue and worker pool to prevent resource exhaustion.
- Build containers: `CapDrop: ALL`, `no-new-privileges`, bridge networking,
  memory/CPU/PID limits, ephemeral `tmpfs` workspace, and a build timeout.
- Log size caps per build.
- Optional admin bearer token for mutating API routes.
- Optional CORS allowlist for a remotely hosted dashboard.
- Stale-container cleanup after crashes and orphaned-build recovery.

## Hardening checklist

- [ ] Use a long, random `GITHUB_WEBHOOK_SECRET` and rotate it if leaked.
- [ ] Terminate TLS with a reverse proxy (Caddy, nginx, Traefik) or a tunnel.
- [ ] Put authentication in front of the dashboard and API.
- [ ] Set `ADMIN_TOKEN` if you enable retries.
- [ ] Keep `MAX_CONCURRENT_BUILDS`, `QUEUE_SIZE`, and `BUILD_TIMEOUT` conservative.
- [ ] Do not add untrusted repositories or shell commands to `projects.yaml`.
- [ ] Run the runner on a dedicated host or VM, and keep Docker patched.
- [ ] Back up the SQLite database volume.
- [ ] Review build images regularly and pin trusted tags or digests.

## Known limitations

- Build logs may contain repository output. Treat the database and dashboard as
  sensitive.
- The runner does not currently implement role-based access control.
- Webhook replay is limited to delivery-ID deduplication; there is no timestamp
  window because GitHub does not include one in the signature.
- SQLite is intended for a single runner instance, not horizontal scaling.
