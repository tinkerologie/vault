# AGENTS.md — AI Agent Instructions

> Single source of truth for all AI coding assistants working on this project.
> Individual tool files (CLAUDE.md, GEMINI.md, copilot-instructions.md, .cursorrules) point here.

## Role-Specific Agent Instructions

Before starting work, read the relevant agent file(s) in `.github/agents/` that match your current task:

| File                                | When to read                         |
| ----------------------------------- | ------------------------------------ |
| `api-architect.agent.md`            | Designing or modifying API endpoints |
| `debug.agent.md`                    | Debugging issues                     |
| `devops-expert.agent.md`            | Build, deploy, CI/CD, infrastructure |
| `gem-documentation-writer.agent.md` | Writing documentation                |
| `github-actions-expert.agent.md`    | GitHub Actions workflows             |
| `plan.agent.md`                     | Planning features or refactors       |
| `playwright-tester.agent.md`        | Writing or running E2E tests         |
| `qa-subagent.agent.md`              | Quality assurance and testing        |
| `refine-issue.agent.md`             | Refining issues or requirements      |
| `se-security-reviewer.agent.md`     | Security review of code changes      |

## Project Identity

| Key          | Value                                                           |
| ------------ | --------------------------------------------------------------- |
| **Name**     | Vault                                                           |
| **Language** | Go 1.26                                                         |
| **Target**   | Linux/amd64 (Unraid OS)                                         |
| **Type**     | Third-party community plugin (backup/restore daemon)            |
| **Purpose**  | REST API + WebSocket for Docker container and libvirt VM backup |
| **Repo**     | `github.com/ruaan-deysel/vault`                                 |

## Project Structure

```text
/
├── cmd/vault/                  # CLI entry point (main.go → cli.Execute())
├── internal/
│   ├── api/                    # HTTP server (Chi router), REST handlers, WebSocket
│   │   ├── server.go           # Server struct, ListenAndServe, respondJSON
│   │   ├── routes.go           # Route definitions (/api/v1/...)
│   │   └── handlers/           # Job and Storage CRUD handlers
│   ├── cli/                    # Cobra CLI commands (root, daemon)
│   ├── config/                 # Enum constants (CompressionType, BackupType, StorageType)
│   ├── db/                     # SQLite database (pure Go via modernc.org/sqlite)
│   │   ├── db.go               # Open, ping, WAL mode, schema
│   │   ├── migrations.go       # Inline schema (CREATE TABLE IF NOT EXISTS)
│   │   ├── models.go           # Job, JobItem, JobRun, RestorePoint, StorageDestination
│   │   ├── job_repo.go         # Job CRUD, items, runs, restore points
│   │   └── storage_repo.go     # StorageDestination CRUD
│   ├── engine/                 # Backup/restore logic
│   │   ├── types.go            # BackupItem, BackupResult, Handler interface
│   │   ├── container.go        # Docker SDK: stop→image→volumes→start
│   │   ├── vm.go               # libvirt RPC backup/restore via backup jobs (linux only)
│   │   ├── vm_stub.go          # Stub for non-Linux builds
│   │   └── fileutil.go         # File copy utilities (linux only)
│   ├── notify/                 # Unraid notification integration
│   ├── scheduler/              # Cron-based job scheduler (robfig/cron)
│   ├── storage/                # Pluggable storage backends
│   │   ├── adapter.go          # Adapter interface definition
│   │   ├── factory.go          # NewAdapter() factory dispatch
│   │   ├── local.go            # LocalAdapter
│   │   ├── sftp.go             # SFTPAdapter
│   │   ├── nfs.go              # NFSAdapter (NFS mount-based)
│   │   └── smb.go              # SMBAdapter
│   └── ws/                     # WebSocket hub (pub/sub broadcast)
├── plugin/                     # Unraid plugin (.plg installer, PHP pages, JS/CSS)
├── ansible/                    # Deployment automation
├── docs/plans/                 # Design docs and implementation plans
├── .github/
│   ├── agents/                 # Role-specific agent instructions (.agent.md)
│   ├── instructions/           # Path-specific AI instructions (applyTo globs)
│   ├── prompts/                # Reusable task prompts for common workflows
│   └── workflows/              # CI/CD (build.yml, release.yml)
├── Makefile                    # Build automation
├── go.mod / go.sum             # Go dependencies
└── VERSION                     # Current version (YYYY.M.D format)
```

## Architecture

### Layered Design

```text
CLI (Cobra) → API Server (Chi + WebSocket Hub) → Handlers → DB / Storage / Engine
```

- **CLI layer** (`internal/cli/`): Cobra commands. `vault daemon` starts the server.
- **API layer** (`internal/api/`): Chi router with middleware (Logger, Recoverer, Heartbeat). Routes at `/api/v1/`. WebSocket at `/api/v1/ws`.
- **Handler layer** (`internal/api/handlers/`): CRUD for jobs and storage destinations. Each handler takes a `*db.DB`.
- **Data layer** (`internal/db/`): SQLite with WAL mode and foreign keys. Repos handle all SQL.
- **Storage layer** (`internal/storage/`): `Adapter` interface with factory pattern. Config stored as JSON blob in DB.
- **Engine layer** (`internal/engine/`): `Handler` interface for backup/restore. Container backups use the Docker SDK. VM backups use the pure-Go libvirt RPC client and backup jobs on Linux. Platform-specific via build tags.
- **Scheduler** (`internal/scheduler/`): Cron scheduler loads jobs from DB. Supports Start/Stop/Reload.
- **WebSocket** (`internal/ws/`): Hub with register/unregister/broadcast channels for real-time progress.

### Key Interfaces

**`storage.Adapter`** (`internal/storage/adapter.go`):

```go
type Adapter interface {
    Write(path string, reader io.Reader) error
    Read(path string) (io.ReadCloser, error)
    Delete(path string) error
    List(prefix string) ([]FileInfo, error)
    Stat(path string) (FileInfo, error)
    TestConnection() error
}
```

**`engine.Handler`** (`internal/engine/types.go`):

```go
type Handler interface {
    Backup(item BackupItem, dest string, progress ProgressFunc) (*BackupResult, error)
    Restore(item BackupItem, source string, progress ProgressFunc) error
    ListItems() ([]BackupItem, error)
}
```

### Build-Tag Platform Isolation

- `vm.go` and `fileutil.go`: `//go:build linux` — real libvirt RPC and file operations
- `vm_stub.go`: `//go:build !linux` — stubs for macOS/Windows development
- Tests and local builds work on macOS without libvirt installed

### Storage Factory Pattern

`storage.NewAdapter(storageType, configJSON)` in `factory.go` dispatches to concrete adapters. Each adapter parses its own config struct from JSON. Storage config is stored as a JSON blob in the `storage_destinations.config` column.

### SQLite Configuration

```go
sql.Open("sqlite", path+"?_txlock=immediate&_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=journal_size_limit(67108864)")
```

WAL mode for concurrent reads. Foreign keys, busy timeout and WAL are set as DSN pragmas so every connection gets them (modernc.org/sqlite ignores the mattn-style \_journal_mode=/\_busy_timeout= keys). Schema applied inline at `Open()` time via `CREATE TABLE IF NOT EXISTS` (no migration versioning framework).

## Build Commands

### Plugin Lifecycle (Ansible-driven)

```bash
make build               # Ansible: lint → test → web build → cross-compile Linux/amd64
make deploy              # Ansible: deploy binary + assets to Unraid, start daemon
make verify              # Ansible: run endpoint checks plus folder/VM smoke tests against Unraid
make redeploy            # Ansible: full lifecycle (uninstall → build → deploy → verify)
```

### Local Development

```bash
make deps                # Install and tidy dependencies
make build-local         # Build for current platform
make test                # Run unit tests (go test ./... -v)
make test-short          # Run short tests only
make test-coverage       # Generate coverage.html
make lint                # Run golangci-lint with .golangci.yml
make security-check      # Run gosec + govulncheck + go mod verify
make clean               # Remove build artifacts
make pre-commit-install  # Install pre-commit hooks
make pre-commit-run      # Run all pre-commit checks
```

### Running the Daemon

```bash
./build/vault daemon --db=vault.db --addr=:24085
```

Defaults: DB at `/boot/config/plugins/vault/vault.db`, API on port 24085.

## Code Search

Both `rg` (ripgrep) and `ast-grep` are available. **Default to `rg` for most queries** (~5–20 ms on this repo, handles every file type, no parser surprises). Reach for `ast-grep` (~30–130 ms) only when you need AST-aware matching — typically a structural refactor or a pattern whose meaning depends on syntactic context.

**Use `rg` for:**

- Literal strings (log messages, error strings, URLs, JSON keys)
- Definitions by name: `rg -n '^func .*FolderHandler.* Backup\b' --type go`
- **Package-qualified call sites**: `rg -n 'runner\.New\(' --type go` — ast-grep returns zero matches for this; see Gotchas
- Comments and TODOs
- Per-file counts and lists (`rg -c '%w' --type go`, `rg -l 'panic\(' --type go`)
- Svelte files: `rg --type-add 'svelte:*.svelte' --type svelte 'pattern'` — **ast-grep does not support svelte** (0.42.3)

**Use `ast-grep` for:**

- Function definitions matching a structural shape: `ast-grep run -p 'func ($H *FolderHandler) Backup($$$ARGS) $$$RET { $$$BODY }' -l go`
- All `fmt.Errorf(...)` calls without false-positive matches in strings/comments
- Struct/interface declarations
- Multi-clause queries via `ast-grep scan --rule rule.yml` (`has` / `inside` / `where`)

**Verified gotchas:**

- Package-qualified calls (`db.Open(...)`, `runner.New(...)`) return **zero** matches with the obvious pattern because the Go parser treats them as type conversions. Workaround: a YAML rule with `kind: call_expression`, or use `rg`.
- Languages confirmed working: `go`, `ts`, `tsx`, `js`, `html`, `css`, `yaml`, `json`, `bash`, `python`, `rust`, `java`. **Not** `svelte` or `sh`.
- Variadic metavariables must be **named** and usually preceded by a positional: `fmt.Errorf($FMT, $$$ARGS)`, not `fmt.Errorf($$$)`.
- Full ast-grep skill: `.agents/skills/ast-grep/SKILL.md`.

## Code Style and Conventions

- **Standard Go**: `gofmt` and `goimports` enforced. Zero tolerance for linting errors.
- **Error handling**: Wrap errors with context: `fmt.Errorf("context: %w", err)`
- **Build**: CGO_ENABLED=0 — pure Go only. Uses `modernc.org/sqlite` (no C dependencies).
- **Router**: Chi v5 (`go-chi/chi/v5`), not gorilla/mux.
- **Naming**: Follow Go conventions — PascalCase exported, camelCase unexported.
- **Commit messages**: Follow **Conventional Commits**: `feat(scope):`, `fix(scope):`, `docs(scope):`
- **Pre-commit**: Run `make pre-commit-run` before every commit.

## Core Patterns

### Storage Adapter Pattern

```go
// factory.go dispatches by StorageType
func NewAdapter(storageType string, configJSON []byte) (Adapter, error) {
    switch storageType {
    case "local":
        var cfg LocalConfig
        json.Unmarshal(configJSON, &cfg)
        return NewLocalAdapter(cfg)
    // ... sftp, smb, nfs
    }
}
```

### REST Handler Pattern

```go
func (h *JobHandler) List(w http.ResponseWriter, r *http.Request) {
    jobs, err := h.db.ListJobs()
    if err != nil {
        respondError(w, http.StatusInternalServerError, err.Error())
        return
    }
    respondJSON(w, http.StatusOK, jobs)
}
```

Use `respondJSON()` and `respondError()` helpers for all responses.

### Route Registration

Routes registered in `internal/api/routes.go` using Chi's `r.Route()` grouping:

```go
r.Route("/api/v1", func(r chi.Router) {
    r.Route("/storage", func(r chi.Router) { /* CRUD */ })
    r.Route("/jobs", func(r chi.Router) { /* CRUD */ })
})
```

## Adding New Components

### Adding a Storage Adapter

1. Create `internal/storage/mybackend.go` implementing `Adapter` interface
2. Add config struct and `NewMyBackendAdapter()` constructor
3. Add case to `factory.go` `NewAdapter()` switch
4. Add storage type constant to `internal/config/types.go`
5. Write tests in `internal/storage/mybackend_test.go`

### Adding an API Endpoint

1. Add handler method in `internal/api/handlers/`
2. Register route in `internal/api/routes.go`
3. Write tests using `httptest`

### Adding an Engine Handler

1. Create handler implementing `engine.Handler` interface
2. Use build tags if platform-specific (`//go:build linux` + stub file)
3. Wire to backup/restore execution path
4. Write tests

## Testing Conventions

- **Table-driven tests** with subtests (`t.Run`)
- **httptest** for API handler testing
- **t.TempDir()** for file/storage tests (auto-cleanup)
- Tests located alongside source files (`*_test.go`)

```bash
make test                                    # All tests
make test-coverage                           # Coverage report
go test ./internal/db/... -run TestJobCreate -v  # Single test
```

## Recommended Post-Change Workflow

> Agents and developers should follow this workflow for changes intended for integration or release. It does not apply to documentation-only changes or local WIP commits.

### Steps (recommended order)

1. **Build & Test:** Run `make build` (Ansible: lint → test → web build → cross-compile). Fix any failures before proceeding.
2. **Deploy:** Ask the user for confirmation and verify deployment credentials before running `make deploy` (deploys binary + assets to Unraid, starts daemon).
3. **Verify API:** Run `make verify` (endpoint checks + folder/VM smoke tests against Unraid). Fix any failures before proceeding.
4. **Verify UI:** Use Playwright, browser MCP tools, or manual testing to navigate affected pages on `http://<unraid-server>:24085`. Take snapshots to confirm the UI renders correctly.
5. **Update CHANGELOG.md (NON-NEGOTIABLE):** Add entries under `## [Unreleased]` using [Keep a Changelog](https://keepachangelog.com/) format. `CHANGELOG.md` is consumed by THREE systems: the in-app View Changelog modal (Settings → About Vault, parser at `internal/release/changelog.go`), the `release.yml` GitHub-release notes extractor, and operator-facing upgrade diffs — a missing or malformed entry breaks all three. Required format:
   - Section headings (per version): `### Added`, `### Changed`, `### Fixed`, `### Removed`, `### Security` — any other `###` heading is silently dropped.
   - Bullets start with `-` (dash + space) at column 0 — the parser only recognizes that exact prefix. Inline markdown that renders in the modal: `**bold**`, `` `code` ``, `*italic*`. Nothing else is interpreted.
   - Be concise but descriptive — entries stand alone with no PR context. Reference issue numbers (e.g. `closes #123`) where applicable.
   - `[Unreleased]` is intentionally hidden from the modal. At release time, promote it to `## [vX.Y.Z] - YYYY-MM-DD` (heading must match the tag exactly) BEFORE pushing the `v*` tag.

**Shortcut:** `make build deploy verify` (build → deploy → verify) replaces steps 1–3 in one command without uninstalling the plugin, so current Vault settings on the Unraid host are preserved. Steps 4 and 5 are still recommended. Avoid `make redeploy` for routine iteration — it includes an uninstall step that wipes the plugin's configured state.

**Skip when:** Changes are limited to documentation files (`.md`), comments, or files that do not affect the built binary or web UI.

## Anti-Patterns (DO NOT)

- **DO NOT** use CGO — binary must be pure Go (CGO_ENABLED=0)
- **DO NOT** bypass the factory pattern in `storage/factory.go`
- **DO NOT** skip the `storage.Adapter` interface when adding storage backends
- **DO NOT** change SQLite journal mode from WAL
- **DO NOT** commit secrets, credentials, or `ansible/inventory.yml`
- **DO NOT** use `gorilla/mux` — this project uses Chi v5
- **DO NOT** add libvirt code without build tags (breaks macOS builds)
- **DO NOT** consider changes ready for integration without running the verification workflow
- **DO NOT** skip UI verification before marking changes as complete (use Playwright/browser tools or manual testing)

## Key Dependencies

| Package                              | Purpose                            |
| ------------------------------------ | ---------------------------------- |
| `github.com/go-chi/chi/v5`           | HTTP router                        |
| `github.com/spf13/cobra`             | CLI framework                      |
| `github.com/robfig/cron/v3`          | Cron scheduler                     |
| `modernc.org/sqlite`                 | Pure Go SQLite driver              |
| `github.com/docker/docker`           | Docker Engine SDK                  |
| `github.com/digitalocean/go-libvirt` | Pure Go VM management (Linux only) |
| `github.com/vmware/go-nfs-client`    | NFS storage adapter                |
| `github.com/cloudsoda/go-smb2`       | SMB storage adapter                |
| `github.com/pkg/sftp`                | SFTP storage adapter               |
| `github.com/coder/websocket`         | WebSocket server                   |

## API Structure

Base URL: `http://localhost:24085/api/v1`

- `GET /health` — Health check
- `GET/POST /storage` — List/create storage destinations
- `GET/PUT/DELETE /storage/{id}` — Storage CRUD
- `POST /storage/{id}/test` — Test storage connection
- `GET/POST /jobs` — List/create jobs
- `GET/PUT/DELETE /jobs/{id}` — Job CRUD
- `GET /jobs/{id}/history` — Job run history
- `GET /jobs/{id}/restore-points` — Restore points
- `GET /ws` — WebSocket real-time events

## Deployment

The daemon runs on Unraid at `/boot/config/plugins/vault/vault`. Plugin XML (`plugin/vault.plg`) defines install/remove lifecycle and the `rc.vault` service script.

**Ansible (preferred):**

```bash
cp ansible/inventory.yml.example ansible/inventory.yml  # Add server details
ansible-playbook -i ansible/inventory.yml ansible/ansible.yml --tags deploy
```

## Version

Date-based versioning in `VERSION` file (e.g., `2026.3.0`). Injected via ldflags at build time.
