# Contributing to Vault

Vault is a Go backup daemon for Unraid with a Svelte 5 web UI, shipped as an Unraid `.plg` plugin.

> **[`AGENTS.md`](AGENTS.md) is the single source of truth** for architecture,
> conventions, safety boundaries, and validation. Path-specific rules live in
> [`.github/instructions/`](.github/instructions/). Files in
> [`.github/agents/`](.github/agents/) are optional GitHub Copilot profiles.

## Prerequisites

Before you start, install the tools used by the repo:

- **Go 1.26.x** (see `go.mod`) — the binary is pure Go and builds with `CGO_ENABLED=0`.
- **Node.js + npm** — required for the Svelte UI in `web/`.
- **Python 3 + pip** — needed for `pre-commit` and any hook tooling.
- **Docker** — optional but useful for the repo's container build target.
- **pre-commit** — install it once with `make pre-commit-install` or by running `scripts/setup-pre-commit.sh`.
- **gofmt / goimports / golangci-lint / gosec / govulncheck** — the project expects these tools in your local toolchain.

### Bootstrap a local dev environment

For a fresh machine, the repo provides a helper script that installs the common requirements and pre-commit hooks:

```bash
./scripts/setup-pre-commit.sh
```

If you want to install the tooling manually instead, use the repo targets:

```bash
make deps
make pre-commit-install
```

## Local development loop

Most day-to-day work follows this loop:

```bash
make deps               # go mod download && go mod tidy
make build-local        # builds the web UI and cross-compiles the daemon
make test               # go test ./... -v
make test-short         # go test ./... -short
make lint               # golangci-lint with .golangci.yml
make security-check     # gosec + govulncheck + go mod verify
```

### Run a single test

```bash
go test ./internal/db/... -run TestJobCreate -v
```

### Build the web UI directly

```bash
cd web && npm ci
cd web && npm run build
cd web && npm run lint
cd web && npm test
```

### Run the frontend in dev mode

```bash
cd web && npm install
cd web && npm run dev
```

The web UI is bundled into the Go binary, so changes to `web/` are normally validated by the Make targets before a commit.

## Build and run the daemon locally

Vault is a Linux/amd64 daemon and the repo's local build target produces:

```bash
./build/vault-linux-amd64 --help
```

For a quick compile-only smoke test:

```bash
make build-local
```

For a containerized build:

```bash
make docker-build
```

> `make deploy`, `make verify`, and `make redeploy` target a configured Unraid host and must not be run without explicit approval. `make redeploy` includes uninstall.

## Post-change workflow

For ordinary contributions, run the checks relevant to the change:

1. Targeted tests while iterating.
2. `make test` and `make lint` for Go changes.
3. `npm test`, `npm run lint`, and `npm run build` in `web/` for UI changes.
4. `make pre-commit-run` before committing.

If the change is user-facing or release-facing, the maintainer's release gate is:

```bash
make build
make deploy
make verify
```

That flow is for an approved Unraid deployment and is separate from local contributor testing.

## Change log entry

User-visible and release-facing changes add an entry under `## [Unreleased]`
in `CHANGELOG.md` using [Keep a Changelog](https://keepachangelog.com/)
sections: `### Added`, `### Changed`, `### Fixed`, `### Removed`, `### Security`
(any other `###` heading is silently dropped).

- Explain **what** changed **and why** — entries stand alone with no PR context.
- Reference issue numbers where applicable (for example `closes #123`).
- Bullets start with a `-` and a space at column 0. Inline markdown that renders: `**bold**`, `` `code` ``, `*italic*` — nothing else.

`CHANGELOG.md` is consumed by three systems — the in-app About/View Changelog modal (parser at `internal/release/changelog.go`), the `release.yml` GitHub-release notes extractor, and operator-facing upgrade diffs — so a malformed entry breaks all three.

## Commits & pull requests

- **Conventional Commits** with a scope: `feat(scope):`, `fix(scope):`, `refactor(scope):`, `docs:`, `chore:`, `deps:`. Examples from history: `fix(api):`, `feat(vm):`, `refactor(web):`, `chore(deps):`.
- Branch per change off `main`; open a PR against `main`.
- PRs get automated **CodeRabbit** and **Copilot** review — address their feedback before merge.
- Run `make pre-commit-run` before committing.

## Code search

Both `rg` (ripgrep) and `ast-grep` are available. Default to `rg`; reach for `ast-grep` only when you need AST-aware matching. See the Code Search section in `AGENTS.md` for the gotchas (notably: `ast-grep` does not support `.svelte`).
