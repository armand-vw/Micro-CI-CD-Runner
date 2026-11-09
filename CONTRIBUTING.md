# Contributing to Micro-CI/CD Runner

Thanks for taking the time to contribute. This project favours small, focused
changes that keep the runner easy to audit and operate.

## Before you start

- Read [README.md](README.md), [docs/architecture.md](docs/architecture.md), and
  [SECURITY.md](SECURITY.md).
- For non-trivial changes, open an issue first so the design can be discussed.
- Never include secrets, tokens, webhook payloads with credentials, or personal
  data in issues, commits, or tests.

## Development setup

Requirements:

- Go 1.26 or newer.
- Docker Engine reachable through the local socket (for integration work).
- `make` (optional but convenient).

```bash
git clone https://github.com/armand-vw/Micro-CI-CD-Runner.git
cd Micro-CI-CD-Runner
cp .env.example .env    # set GITHUB_WEBHOOK_SECRET
make tidy
make test
make run
```

The dashboard is plain HTML/CSS/JS in `web/` and is embedded at build time. No
Node.js toolchain is required.

## Quality gates

Before opening a pull request, run:

```bash
make fmt
make vet
make test
make build

# If a C compiler is available, also run the race detector:
make test-race
```

CI runs `gofmt`, `go vet`, `go test -race ./...`, `go build ./...`, and CodeQL.
Pull requests that fail these checks will not be merged.

## Commit and pull request style

- Use [Conventional Commits](https://www.conventionalcommits.org/) where
  practical, for example `feat: stream build logs over SSE` or
  `fix: reject webhooks with a missing signature`.
- Keep commits focused and the history readable.
- Describe the motivation and the user-visible behaviour in the pull request.
- Link the related issue when there is one.

## Pull request checklist

- [ ] Tests cover new behaviour and existing tests still pass.
- [ ] `make fmt`, `make vet`, `make test`, and `make build` pass locally.
- [ ] Documentation and `.env.example` are updated when configuration changes.
- [ ] No secrets, credentials, or generated data are committed.
- [ ] Security implications of the change have been considered.

## Code guidelines

- Prefer the standard library. Add a dependency only when it clearly earns its
  place.
- Wrap errors with context (`fmt.Errorf("...: %w", err)`).
- Keep packages small and single-purpose.
- Comments should explain intent and invariants, not restate the code.
- Do not log secrets, tokens, full webhook bodies, or raw credentials.

## Reporting security issues

Do not open a public issue for a vulnerability. Follow the private reporting
process in [SECURITY.md](SECURITY.md).

## License

By contributing, you agree that your contributions are licensed under the
project's [MIT License](LICENSE).
