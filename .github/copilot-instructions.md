# Copilot Instructions

> **Read [`../AGENTS.md`](../AGENTS.md) first** — it is the single source of truth for this project.

Go-based Unraid backup/restore plugin with REST API and WebSocket. **Language:** Go 1.26, **Target:** Linux/amd64 (Unraid OS). Third-party community plugin.

## Copilot Workflow

- Follow Go best practices: idiomatic style, `fmt.Errorf("context: %w", err)`, context propagation
- Code must pass `golangci-lint` and `go vet`
- Run `make pre-commit-run` before committing
- Follow **Conventional Commits**: `feat(scope):`, `fix(scope):`, `docs(scope):`

## Path-Specific Instructions

These files in `.github/instructions/` are auto-applied via `applyTo` globs:

| File                            | Applies To                 |
| ------------------------------- | -------------------------- |
| `go.instructions.md`            | `**/*.go`                  |
| `engine.instructions.md`        | `internal/engine/**/*.go`  |
| `api-handlers.instructions.md`  | `internal/api/**/*.go`     |
| `storage.instructions.md`       | `internal/storage/**/*.go` |
| `db.instructions.md`            | `internal/db/**/*.go`      |
| `tests.instructions.md`         | `**/*_test.go`             |
| `yaml-markdown.instructions.md` | `**/*.{yaml,yml,md}`       |

## Reusable Prompts

Task-oriented step-by-step guides in `.github/prompts/`:

- `Add Storage Adapter.prompt.md` — Adding a new storage backend
- `Add API Endpoint.prompt.md` — Adding a REST API endpoint
- `Add Engine Handler.prompt.md` — Adding a backup engine handler
- `Add Scheduler Job Type.prompt.md` — Adding a new job type
- `Debug Backup Issue.prompt.md` — Debugging backup/restore failures

## Quick Commands

```bash
make deps && make build-local  # Setup and build (local dev)
make test                      # Run unit tests
make pre-commit-run            # Lint + security checks
make build                     # Ansible: lint → test → cross-compile
make deploy                    # Ansible: deploy to Unraid
make verify                    # Ansible: endpoint checks plus folder/VM smoke tests
make redeploy                  # Ansible: full lifecycle
```
