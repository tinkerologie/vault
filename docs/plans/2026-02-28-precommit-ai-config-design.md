# Pre-commit & AI Configuration Design

## Overview

Add pre-commit hooks, golangci-lint config, and AI assistant configuration files to the Vault project, adapted from the unraid-management-agent reference repository.

## Pre-commit Configuration

`.pre-commit-config.yaml` hooks:

### General file checks (pre-commit-hooks v5.0.0)

- trailing-whitespace, end-of-file-fixer, check-yaml (unsafe), check-json, check-toml
- check-added-large-files (1000KB), check-merge-conflict, check-case-conflict
- mixed-line-ending (LF), detect-private-key

### Go formatting (dnephin/pre-commit-golang v0.5.1)

- go-fmt (-s -w), go-imports, go-vet (exclude vendor/tests)

### Go build (local)

- `go build -o /dev/null ./cmd/vault/`

### go-mod-tidy (dnephin/pre-commit-golang v0.5.1)

### golangci-lint (local)

- `golangci-lint run --timeout=5m --config=.golangci.yml --max-issues-per-linter=0 --max-same-issues=0 --issues-exit-code=1 ./...`

### gosec (local)

- `-fmt=text -exclude-dir=vendor -exclude-dir=build -exclude=G115,G304,G301,G306,G703,G204,G117,G704 -severity=medium -confidence=medium ./...`

### Prettier (v4.0.0-alpha.8)

- markdown, yaml, json, xml
- Excludes: `.claude/`, `.ai-scratch/`

### markdownlint (v0.43.0)

- `--fix --disable MD013 MD025 MD003`
- Excludes: `.claude/`, `.github/agents/`

### shellcheck (v0.10.0.1)

- `--severity=error`

### detect-secrets (v1.5.0)

- Excludes: `go.sum`, `*.lock`

### codespell (v2.3.0)

- Skip: `*.sum,*.lock,*.json,*.mod,coverage.html`

### govulncheck (local, graceful skip if not installed)

### go-mod-verify (local)

### check-version-format (local)

- Validate VERSION file matches expected format

### Dropped from reference

- `no-debug-prints` — Vault uses log.Printf directly
- `check-changelog-updated` — Vault has no CHANGELOG.md

## golangci-lint Configuration

`.golangci.yml` (v2 format):

- **Linters:** errcheck, govet (enable-all, disable fieldalignment/shadow), ineffassign, staticcheck, unused, misspell, gosec
- **Formatters:** gofmt, goimports
- **errcheck exclusions:** Close(), strconv.Parse*, fmt.Sscanf/Fprintf, json.Marshal*, http.ResponseWriter.Write, url.Parse, os.Remove\*
- **gosec exclusions:** G204, G304, G115, G104, G301, G306
- **Run:** timeout 5m, tests: false
- **Issues:** max-issues-per-linter 0, max-same-issues 0
- **Exclude dirs:** build, ansible, plugin
- **Vault-specific:** SA1019 for deprecated Docker API

## AGENTS.md

Single source of truth for all AI assistants. Contains:

- Project identity (Vault, Go 1.26, Linux/amd64, Unraid plugin)
- Architecture overview (layered: CLI → API → Handlers → DB/Storage/Engine)
- Key interfaces (storage.Adapter, engine.Handler)
- Build commands (all Makefile targets)
- Core patterns (factory pattern, build tags, WAL mode, WebSocket hub)
- Step-by-step guides (add storage adapter, add API endpoint, add engine handler)
- Anti-patterns (no CGO, no bypassing factory, no secrets)

## Supporting AI Files

### .github/copilot-instructions.md

Points to AGENTS.md. Copilot workflow, path-specific instruction references, prompt references.

### .github/instructions/ (7 files)

| File                          | applyTo                    | Focus                                           |
| ----------------------------- | -------------------------- | ----------------------------------------------- |
| go.instructions.md            | `**/*.go`                  | Go style, imports, errors, context              |
| engine.instructions.md        | `internal/engine/**/*.go`  | Backup/restore, Docker SDK, libvirt, build tags |
| api-handlers.instructions.md  | `internal/api/**/*.go`     | Chi router, handlers, routes                    |
| storage.instructions.md       | `internal/storage/**/*.go` | Adapter interface, factory, config JSON         |
| db.instructions.md            | `internal/db/**/*.go`      | SQLite, WAL, migrations, repos                  |
| tests.instructions.md         | `**/*_test.go`             | Table-driven, httptest, t.TempDir()             |
| yaml-markdown.instructions.md | `**/*.{yaml,yml,md}`       | YAML indent, markdownlint, ATX headers          |

### .github/prompts/ (5 files)

| File                             | Purpose                                                  |
| -------------------------------- | -------------------------------------------------------- |
| Add Storage Adapter.prompt.md    | Implement Adapter → factory → constant → test → document |
| Add API Endpoint.prompt.md       | Handler → route → test → document                        |
| Add Engine Handler.prompt.md     | Handler interface → build tags → stub → test → document  |
| Add Scheduler Job Type.prompt.md | Job type → execution → scheduler → test → document       |
| Debug Backup Issue.prompt.md     | Identify → logs → reproduce → trace → fix → test         |

### Other files

- `.ai-scratch/.gitkeep` — Empty scratch directory
- `.cursorrules` — Bullet-point summary pointing to AGENTS.md
- CLAUDE.md update — Add AGENTS.md reference

## Makefile & Setup

### New Makefile targets

- `pre-commit-install` — install pre-commit hooks
- `pre-commit-run` — run all hooks

### scripts/setup-pre-commit.sh

Automated setup: install Python/pip/pre-commit, install Go tools (golangci-lint, gosec, govulncheck), install hooks, create .secrets.baseline, smoke test. All tools installed automatically before running pre-commit.

## Verification

All of these must pass before completion:

1. `go test ./...`
2. `go vet ./...`
3. `go build -o /dev/null ./cmd/vault/` (amd64)
4. `go mod tidy` (clean)
5. `govulncheck ./...`
6. `golangci-lint run --config=.golangci.yml ./...`
7. `gosec` with exclusions
8. `pre-commit run --all-files`
