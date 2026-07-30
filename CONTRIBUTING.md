# Contributing to Vault

Vault is a Go backup daemon for Unraid with a Svelte 5 web UI, shipped as an Unraid `.plg` plugin.

> **[`AGENTS.md`](AGENTS.md) is the single source of truth** for architecture,
> conventions, safety boundaries, and validation. Path-specific rules live in
> [`.github/instructions/`](.github/instructions/). Files in
> [`.github/agents/`](.github/agents/) are optional GitHub Copilot profiles.

## Prerequisites

- **Go 1.26** (see `go.mod`) — the binary is pure Go (`CGO_ENABLED=0`).
- **Node/npm** — for the Svelte 5 web UI in `web/`.
- **golangci-lint** — linting is enforced with zero tolerance.
- **pre-commit** — run `make pre-commit-install` once to set up the hooks.

## Local Development

```bash
make deps               # go mod download && go mod tidy
make build-local        # Build for Linux/amd64 → build/vault-linux-amd64 (also builds the web UI)
make test               # go test ./... -v
make test-short         # go test ./... -short
make lint               # golangci-lint with .golangci.yml
make security-check     # gosec + govulncheck + go mod verify
```

Run a single test:

```bash
go test ./internal/db/... -run TestJobCreate -v
```

Web UI:

```bash
cd web && npm run build    # Build the UI
cd web && npm run lint     # Lint the UI (also: make lint-web)
cd web && npm test         # Run Vitest
```

## Post-Change Workflow

For ordinary contributions, run the local checks relevant to the change:

1. Targeted tests while iterating.
2. `make test` and `make lint` for Go changes.
3. `npm test`, `npm run lint`, and `npm run build` in `web/` for UI changes.
4. `make pre-commit-run` before committing.

The maintainer runs the release-facing `make build` → approved `make deploy` →
`make verify` → UI verification gate. `make deploy`, `make verify`, and
`make redeploy` target a configured Unraid host and must not be run without
explicit approval. `make redeploy` includes uninstall.

## Changelog

User-visible and release-facing changes add an entry under `## [Unreleased]`
in `CHANGELOG.md` using [Keep a Changelog](https://keepachangelog.com/)
sections: `### Added`, `### Changed`, `### Fixed`, `### Removed`, `### Security`
(any other `###` heading is silently dropped).

- Explain **what** changed **and why** — entries stand alone with no PR context.
- Reference issue numbers where applicable (e.g. `closes #123`).
- Bullets start with a `-` and a space at column 0. Inline markdown that renders: `**bold**`, `` `code` ``, `*italic*` — nothing else.

`CHANGELOG.md` is consumed by three systems — the in-app About/View Changelog modal (parser at `internal/release/changelog.go`), the `release.yml` GitHub-release notes extractor, and operator-facing upgrade diffs — so a malformed entry breaks all three.

## Commits & Pull Requests

- **Conventional Commits** with a scope: `feat(scope):`, `fix(scope):`, `refactor(scope):`, `docs:`, `chore:`, `deps:`. Examples from history: `fix(api):`, `feat(vm):`, `refactor(web):`, `chore(deps):`.
- Branch per change off `main`; open a PR against `main`.
- PRs get automated **CodeRabbit** and **Copilot** review — address their feedback before merge.
- Run `make pre-commit-run` before committing.

## Code Search

Both `rg` (ripgrep) and `ast-grep` are available. Default to `rg`; reach for `ast-grep` only when you need AST-aware matching. See the Code Search section in `AGENTS.md` for the gotchas (notably: `ast-grep` does not support `.svelte`).
