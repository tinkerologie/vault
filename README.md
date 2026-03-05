# Vault

Backup and restore daemon for [Unraid](https://unraid.net/) servers. Backs up Docker containers and libvirt VMs
to pluggable storage destinations with a REST API, WebSocket real-time progress, and an integrated web UI.

## Features

- **Docker Container Backup & Restore** — Full image, config, and appdata volume backup via Docker SDK
- **VM Backup & Restore** — Live snapshot and cold backup via libvirt
- **Pluggable Storage** — Local, SFTP, SMB, and NFS backends
- **Full, Incremental, and Differential** backup types with retention policies
- **Cron-based Scheduling** — Flexible job scheduling with history tracking
- **REST API** — Complete CRUD for jobs and storage destinations
- **WebSocket** — Real-time progress streaming during backup/restore operations
- **Unraid Web UI** — Dashboard, Jobs, Restore, Storage, History, and Settings pages

## Requirements

- **Go 1.26+** (for building from source)
- **Unraid 7.0+** (target deployment platform)
- **Ansible** (optional, for automated deployment)

## Quick Start

```bash
# Install dependencies
make deps

# Build for current platform (development)
make build-local

# Run the daemon
./build/vault daemon --db=vault.db --addr=:24085
```

The API will be available at `http://localhost:24085/api/v1/`.

## Build

```bash
make deps          # Download and tidy Go modules
make build         # Cross-compile for Linux/amd64 (CGO_ENABLED=0)
make build-local   # Build for current platform
make package       # Build and display binary info
make clean         # Remove build artifacts
```

The production binary is built with `CGO_ENABLED=0` for a fully static, pure-Go binary using
[modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) (no C dependencies).

## Testing

```bash
make test            # Run all tests
make test-short      # Run short tests only
make test-coverage   # Generate HTML coverage report

# Run a single test
go test ./internal/db/... -run TestJobCreate -v
```

## Linting & Security

```bash
make lint              # Run golangci-lint
make security-check    # Run gosec, govulncheck, go mod verify
make pre-commit-run    # Run all pre-commit checks
make pre-commit-install  # Install pre-commit hooks
```

## API

Base URL: `http://<host>:24085/api/v1`

| Method | Endpoint                    | Description                |
| ------ | --------------------------- | -------------------------- |
| GET    | `/health`                   | Health check               |
| GET    | `/storage`                  | List storage destinations  |
| POST   | `/storage`                  | Create storage destination |
| GET    | `/storage/{id}`             | Get storage destination    |
| PUT    | `/storage/{id}`             | Update storage destination |
| DELETE | `/storage/{id}`             | Delete storage destination |
| POST   | `/storage/{id}/test`        | Test storage connection    |
| GET    | `/jobs`                     | List jobs                  |
| POST   | `/jobs`                     | Create job                 |
| GET    | `/jobs/{id}`                | Get job                    |
| PUT    | `/jobs/{id}`                | Update job                 |
| DELETE | `/jobs/{id}`                | Delete job                 |
| GET    | `/jobs/{id}/history`        | Job run history            |
| GET    | `/jobs/{id}/restore-points` | Restore points             |
| GET    | `/ws`                       | WebSocket connection       |

## Architecture

```text
CLI (Cobra) → API Server (Chi + WebSocket Hub) → Handlers → DB / Storage / Engine
```

| Layer     | Package               | Description                                      |
| --------- | --------------------- | ------------------------------------------------ |
| CLI       | `internal/cli/`       | Cobra commands (`vault daemon`)                  |
| API       | `internal/api/`       | Chi router, REST handlers, WebSocket integration |
| Database  | `internal/db/`        | SQLite with WAL mode (pure Go)                   |
| Storage   | `internal/storage/`   | Pluggable adapters (Local, SFTP, SMB, NFS)       |
| Engine    | `internal/engine/`    | Backup/restore logic (Docker, libvirt)           |
| Scheduler | `internal/scheduler/` | Cron-based job scheduling                        |
| WebSocket | `internal/ws/`        | Pub/sub hub for real-time events                 |
| Notify    | `internal/notify/`    | Unraid notification integration                  |

### Storage Backends

| Backend | Config Key | Description                |
| ------- | ---------- | -------------------------- |
| Local   | `local`    | Local filesystem path      |
| SFTP    | `sftp`     | SSH File Transfer Protocol |
| SMB     | `smb`      | Windows/Samba file shares  |
| NFS     | `nfs`      | Network File System shares |

Storage adapters implement the `Adapter` interface and are instantiated via a factory pattern
in `internal/storage/factory.go`.

### Build Tags

- `//go:build linux` — Real libvirt VM handler (`internal/engine/vm.go`)
- `//go:build !linux` — Stub for macOS/Windows development (`internal/engine/vm_stub.go`)

## Deployment

### Unraid Plugin

Install via the Unraid Community Applications or manually with the plugin URL:

```text
https://raw.githubusercontent.com/ruaandeysel/vault/main/plugin/vault.plg
```

The daemon runs at `/usr/local/sbin/vault` with the database at
`/boot/config/plugins/vault/vault.db`. Manage the service with:

```bash
/etc/rc.d/rc.vault start|stop|restart|status
```

### Ansible (Automated)

```bash
cd ansible
cp inventory.yml.example inventory.yml   # Add your Unraid server details
ansible-playbook -i inventory.yml ansible.yml --tags deploy    # Deploy only
ansible-playbook -i inventory.yml ansible.yml --tags redeploy  # Full lifecycle
ansible-playbook -i inventory.yml ansible.yml --tags verify    # Run tests
```

See [ansible/README.md](ansible/README.md) for full usage options.

## Project Structure

```text
├── cmd/vault/           # CLI entry point
├── internal/
│   ├── api/            # HTTP server and REST handlers
│   ├── cli/            # Cobra CLI commands
│   ├── config/         # Enum constants and types
│   ├── db/             # SQLite database and repositories
│   ├── engine/         # Backup/restore logic
│   ├── notify/         # Unraid notifications
│   ├── scheduler/      # Cron job scheduler
│   ├── storage/        # Storage backend adapters
│   └── ws/             # WebSocket hub
├── plugin/             # Unraid plugin files (.plg, PHP, JS, CSS)
├── ansible/            # Deployment automation
└── docs/plans/         # Design documents
```

## Version

Date-based versioning (`YYYY.M.D`) tracked in the `VERSION` file and injected via ldflags at
build time.

## License

This project is a third-party community plugin for Unraid OS.
