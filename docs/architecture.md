# Architecture

Vault is a single Go binary that runs as a daemon on Unraid servers. It provides backup and restore for Docker containers, libvirt VMs, folders, and plugins.

## Layered Design

```text
CLI (Cobra) -> API Server (Chi + WebSocket Hub) -> Handlers -> DB / Storage / Engine
```

| Layer     | Package               | Description                                             |
| --------- | --------------------- | ------------------------------------------------------- |
| CLI       | `internal/cli/`       | Cobra commands including `vault daemon` and `vault mcp` |
| API       | `internal/api/`       | Chi router, REST handlers, WebSocket integration        |
| MCP       | `internal/mcp/`       | Model Context Protocol tools over HTTP and stdio        |
| Database  | `internal/db/`        | SQLite with WAL mode                                    |
| Storage   | `internal/storage/`   | Local, SFTP, SMB, and NFS adapters                      |
| Engine    | `internal/engine/`    | Backup and restore logic                                |
| Scheduler | `internal/scheduler/` | Cron-based scheduling                                   |
| WebSocket | `internal/ws/`        | Real-time event hub                                     |
| Notify    | `internal/notify/`    | Unraid notifications                                    |

## Key Interfaces

### storage.Adapter

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

### engine.Handler

```go
type Handler interface {
    Backup(item BackupItem, dest string, progress ProgressFunc) (*BackupResult, error)
    Restore(item BackupItem, source string, progress ProgressFunc) error
    ListItems() ([]BackupItem, error)
}
```

## Build Tags

- `vm.go` and `fileutil.go`: `//go:build linux` — real libvirt RPC and file operations
- `vm_stub.go`: `//go:build !linux` — stubs for macOS/Windows development
- Tests and local builds work on macOS without libvirt installed

## Database

SQLite with WAL mode and busy timeout. Pure Go driver via `modernc.org/sqlite` (no CGO).

```go
sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
```

Schema applied inline at open time via `CREATE TABLE IF NOT EXISTS` (no versioned migrations). Five tables: `storage_destinations`, `jobs`, `job_items`, `job_runs`, `restore_points`.

## Project Structure

```text
├── cmd/vault/           # CLI entry point
├── internal/
│   ├── api/             # HTTP server and REST handlers
│   │   ├── server.go    # Server struct, ListenAndServe
│   │   ├── routes.go    # Route definitions
│   │   └── handlers/    # Job and Storage CRUD handlers
│   ├── cli/             # Cobra CLI commands
│   ├── config/          # Enum constants and types
│   ├── db/              # SQLite database and repositories
│   │   ├── db.go        # Open, ping, WAL mode, schema
│   │   ├── migrations.go# Inline schema
│   │   ├── models.go    # Data models
│   │   ├── job_repo.go  # Job CRUD
│   │   └── storage_repo.go # Storage CRUD
│   ├── engine/          # Backup and restore logic
│   │   ├── types.go     # BackupItem, BackupResult, Handler interface
│   │   ├── container.go # Docker SDK backup/restore
│   │   ├── vm.go        # libvirt backup/restore (Linux only)
│   │   └── vm_stub.go   # Stub for non-Linux builds
│   ├── mcp/             # MCP tools and transports
│   ├── notify/          # Unraid notifications
│   ├── replication/     # Remote Vault replication
│   ├── scheduler/       # Cron scheduler
│   ├── storage/         # Storage backend adapters
│   │   ├── adapter.go   # Adapter interface
│   │   ├── factory.go   # NewAdapter() dispatch
│   │   ├── local.go     # Local filesystem
│   │   ├── sftp.go      # SFTP
│   │   ├── smb.go       # SMB
│   │   └── nfs.go       # NFS
│   └── ws/              # WebSocket hub
├── plugin/              # Unraid plugin files (.plg, PHP, JS, CSS)
├── ansible/             # Deployment automation
├── scripts/             # Development and verification helpers
├── web/                 # Svelte 5 frontend
└── docs/                # Documentation
```

## Build Commands

### Plugin Lifecycle (Ansible-driven)

```bash
make build               # Lint -> test -> web build -> cross-compile Linux/amd64
make deploy              # Deploy binary + assets to Unraid, start daemon
make verify              # Run endpoint checks plus smoke tests against Unraid
make redeploy            # Full lifecycle: uninstall -> build -> deploy -> verify
```

### Local Development

```bash
make deps                # Download and tidy Go modules
make build-local         # Build for the current platform
make test                # Run all unit tests
make test-short          # Run short tests only
make test-coverage       # Generate coverage.out and coverage.html
make lint                # Run golangci-lint
make security-check      # Run gosec, govulncheck, and go mod verify
make pre-commit-install  # Install pre-commit hooks
make pre-commit-run      # Run the full local quality gate
make clean               # Remove build artifacts
```

### Running the Daemon

```bash
./build/vault daemon --db=vault.db --addr=:24085
```

The production binary is built with `CGO_ENABLED=0` using `modernc.org/sqlite`, keeping it pure Go.

## Deployment

### Unraid Plugin

Install via the Unraid Community Applications store, or paste the plugin URL into Plugins > Install Plugin:

```text
https://raw.githubusercontent.com/ruaan-deysel/vault/main/plugin/vault.plg
```

The daemon runs at `/usr/local/sbin/vault` with the database at `/boot/config/plugins/vault/vault.db`.

Service management:

```bash
/etc/rc.d/rc.vault start
/etc/rc.d/rc.vault stop
/etc/rc.d/rc.vault restart
/etc/rc.d/rc.vault status
```

Uninstall removes runtime and configuration traces (including the database) while preserving backup data in configured storage destinations.

### Ansible

```bash
cd ansible
cp inventory.yml.example inventory.yml   # Add server details
ansible-playbook -i inventory.yml ansible.yml --tags deploy
ansible-playbook -i inventory.yml ansible.yml --tags verify
```

See [ansible/README.md](../ansible/README.md) for the full deployment workflow.

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

## Version

Vault uses date-based versioning in the `VERSION` file with the format `YYYY.MM.PATCH`. Version is injected via ldflags at build time (`-X main.version`, `-X main.buildDate`, `-X main.commit`).
