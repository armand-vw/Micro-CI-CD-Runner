# Hosting the dashboard on GitHub Pages

GitHub Pages is **static hosting**. It cannot run the webhook listener, talk to
Docker, write SQLite, or serve Server-Sent Events. The runner backend always
needs a machine with Docker.

That leaves three valid setups.

## Option 1: Runner serves the dashboard (recommended)

The Go binary embeds `web/` and serves it at `/`. This is the default and needs
no GitHub Pages configuration. It is the simplest and safest option because the
UI and API share an origin and no CORS or public admin API is required.

## Option 2: GitHub Pages dashboard + remote runner

Use Pages to host the UI while the runner stays on a VPS or homelab behind HTTPS.

The included `.github/workflows/pages.yml` publishes `web/` on every push to
`main`. To point the UI at your backend:

1. Go to **Settings → Secrets and variables → Actions → Variables**.
2. Add `RUNNER_API_URL` with your public runner URL, for example
   `https://ci.example.com`.
3. Re-run the **Pages** workflow.
4. On the runner, allow the Pages origin:

   ```env
   ALLOWED_ORIGINS=https://<owner>.github.io
   ADMIN_TOKEN=generate-a-long-random-token
   ```

5. Protect the runner API with a reverse proxy or a VPN. CORS is not
   authentication; it only controls browser behaviour.

The dashboard at `https://<owner>.github.io/<repo>/` will then call
`https://ci.example.com/api/...` and open an SSE connection to
`https://ci.example.com/api/events`.

Security warning: a Pages-hosted UI runs in every visitor's browser. Do not put
an admin bearer token in the static files. If the API is public, anyone can read
build history and logs. Use authentication at the proxy.

## Option 3: Pages as a static demo

Publish `web/` with an empty `RUNNER_CONFIG.apiBase`. The layout, themes, and
navigation work, but live data is unavailable because there is no backend at the
Pages origin. This is useful as a landing page or design preview.

## How the Pages workflow works

```yaml
- run: |
    api_base="${{ vars.RUNNER_API_URL }}"
    printf 'window.RUNNER_CONFIG = { apiBase: "%s" };\n' "$api_base" > web/config.js
- uses: actions/configure-pages@v5
- uses: actions/upload-pages-artifact@v3
  with:
    path: web
- uses: actions/deploy-pages@v4
```

The workflow rewrites `web/config.js` at build time. The committed version keeps
`apiBase` empty so the runner's embedded UI uses same-origin requests.

## Enabling Pages

1. **Settings → Pages → Build and deployment → Source:** GitHub Actions.
2. Push to `main` or run the **Pages** workflow manually.
3. If the repository is private, Pages availability depends on your GitHub plan.

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| Dashboard loads but data is empty | `RUNNER_API_URL` is unset or unreachable. |
| Browser blocks requests | Missing CORS allowlist on the runner. |
| SSE never connects | Proxy buffering is enabled; disable buffering for `/api/events`. |
| 401 on retry | `ADMIN_TOKEN` is set; enter it when prompted. |
| 404 on refresh | The dashboard does not use client-side routes, so this should not happen; verify the artifact includes `index.html`. |

The UI uses relative asset paths, so it works both at a repository subpath on
Pages and at the root of the runner.
