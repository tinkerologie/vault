# Contributing to Vault

Vault is a Go backup daemon for Unraid with a Svelte 5 web UI, shipped as an Unraid `.plg` plugin.

> **[`AGENTS.md`](AGENTS.md) is the single source of truth** for architecture, conventions, and the layered design. Read it first. Role-specific guidance lives in [`.github/agents/`](.github/agents/) (API, DevOps, security review, QA, planning, etc.) — read the file matching your task.

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

Run the daemon locally:

```bash
./build/vault daemon --db=vault.db --addr=:24085
```

Web UI:

```bash
cd web && npm run build    # Build the UI
cd web && npm run lint     # Lint the UI (also: make lint-web)
```

## Post-Change Workflow

Before a change is ready for integration, follow the workflow in `AGENTS.md`:

1. **Build & test** — `make build` (Ansible: lint → test → web build → cross-compile).
2. **Deploy** — `make deploy` (binary + assets to Unraid).
3. **Verify API** — `make verify` (endpoint checks + folder/VM smoke tests).
4. **Verify UI** — navigate affected pages and confirm they render.
5. **Update `CHANGELOG.md`** — see below (non-negotiable for code changes).

`make deploy` and `make verify` target a **maintainer's Unraid host** and are not reproducible for outside contributors. As a contributor, run `make build-local` and `make test` (plus `make lint`), and describe UI changes in your PR — the maintainer runs deploy/verify.

## CHANGELOG (Required)

Every code change adds an entry under `## [Unreleased]` in `CHANGELOG.md` using [Keep a Changelog](https://keepachangelog.com/) sections: `### Added`, `### Changed`, `### Fixed`, `### Removed`, `### Security` (any other `###` heading is silently dropped).

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
