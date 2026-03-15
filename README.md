# Vault

Backup and restore daemon for [Unraid](https://unraid.net/) servers.

Vault backs up Docker containers, libvirt VMs, folders, and plugins to pluggable storage destinations.
It ships with a REST API, a Streamable HTTP and stdio MCP server, WebSocket progress events, and an
integrated web UI.

## Manual Install

Install the plugin in Unraid by pasting this URL into Plugins -> Install Plugin:

```text
https://raw.githubusercontent.com/ruaan-deysel/vault/main/plugin/vault.plg
```

## Features

- Docker container backup and restore with image, config, and appdata handling
- VM backup and restore with snapshot and cold modes
- Folder and plugin backup support
- Full, incremental, and differential backup chains
- Local, SFTP, SMB, and NFS storage backends
- Cron-based scheduling with retention policies
- Web UI with Dashboard, Jobs, Restore, Storage, History, Replication, Recovery, and Settings
- WebSocket progress streaming and runner queue visibility
- MCP tools for AI assistants and automation

## Requirements

- Go 1.26 or newer to build from source
- Unraid 7.0 or newer for the target deployment platform
- Ansible for the automated deploy and verify workflow

## Quick Start

```bash
make deps
make build-local
./build/vault daemon --db=vault.db --addr=:24085
```

The REST API will be available at `http://localhost:24085/api/v1`.

## Support and Feedback

- Bug reports: [open a bug report](https://github.com/ruaan-deysel/vault/issues/new?template=01-bug-report.yml)
- Enhancement requests: [request an improvement](https://github.com/ruaan-deysel/vault/issues/new?template=02-enhancement-request.yml)
- Questions and support: [use the Unraid forum support thread](https://forums.unraid.net/topic/196675-plugin-unraid-management-agent)

Bug reports and enhancement requests are automatically labeled for triage when they are opened. Questions
and troubleshooting help belong in the support thread so the issue tracker stays focused on actionable product work.

## Build, Test, and Verify

```bash
make deps                # Download and tidy Go modules
make build-local         # Build for the current platform
make build               # Lint -> test -> web build -> Linux/amd64 binary
make test                # Run all unit tests
make test-short          # Run short tests only
make test-coverage       # Generate coverage.out and coverage.html
make lint                # Run golangci-lint
make security-check      # Run gosec, govulncheck, and go mod verify
make pre-commit-run      # Run the full local quality gate
make deploy              # Deploy to the configured Unraid server
make verify              # Verify live REST, WebSocket, and MCP after deploy
make redeploy            # Full lifecycle: uninstall -> build -> deploy -> verify
```

The production binary is built with `CGO_ENABLED=0` and uses
[modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite), so it stays pure Go.

## REST API

Base URL: `http://<host>:24085/api/v1`

### Core and Auth

| Method | Endpoint          | Description                                            |
| ------ | ----------------- | ------------------------------------------------------ |
| GET    | `/health`         | Basic health, version, and mode                        |
| GET    | `/health/summary` | Aggregated dashboard health metrics                    |
| GET    | `/auth/status`    | Whether external API clients require an API key        |
| GET    | `/ws`             | WebSocket event stream                                 |
| GET    | `/runner/status`  | Current backup or restore state, including queued jobs |

### Jobs

| Method | Endpoint                    | Description                                  |
| ------ | --------------------------- | -------------------------------------------- |
| GET    | `/jobs`                     | List jobs                                    |
| POST   | `/jobs`                     | Create job                                   |
| GET    | `/jobs/next-runs`           | Next scheduled run for every job             |
| GET    | `/jobs/{id}`                | Get a job and its items                      |
| PUT    | `/jobs/{id}`                | Update a job                                 |
| DELETE | `/jobs/{id}`                | Delete a job                                 |
| GET    | `/jobs/{id}/history`        | Job run history                              |
| GET    | `/jobs/{id}/restore-points` | Restore points with chain health annotations |
| POST   | `/jobs/{id}/run`            | Trigger an immediate backup                  |
| POST   | `/jobs/{id}/restore`        | Trigger a restore                            |
| GET    | `/jobs/{id}/next-run`       | Next scheduled run for one job               |

### Storage

| Method | Endpoint                   | Description                               |
| ------ | -------------------------- | ----------------------------------------- |
| GET    | `/storage`                 | List storage destinations                 |
| POST   | `/storage`                 | Create storage destination                |
| GET    | `/storage/{id}`            | Get storage destination                   |
| PUT    | `/storage/{id}`            | Update storage destination                |
| DELETE | `/storage/{id}`            | Delete storage destination                |
| POST   | `/storage/{id}/test`       | Test storage connection                   |
| POST   | `/storage/{id}/scan`       | Scan storage for importable backups       |
| POST   | `/storage/{id}/import`     | Import backups discovered during scan     |
| POST   | `/storage/{id}/restore-db` | Restore the Vault database from storage   |
| GET    | `/storage/{id}/jobs`       | Jobs that depend on a storage destination |
| GET    | `/storage/{id}/list`       | List files under a storage path           |
| GET    | `/storage/{id}/files`      | Download a file from storage              |

### Settings

| Method | Endpoint                          | Description                       |
| ------ | --------------------------------- | --------------------------------- |
| GET    | `/settings`                       | List settings                     |
| PUT    | `/settings`                       | Update settings                   |
| GET    | `/settings/encryption`            | Encryption status                 |
| POST   | `/settings/encryption`            | Set encryption passphrase         |
| POST   | `/settings/encryption/verify`     | Verify encryption passphrase      |
| GET    | `/settings/encryption/passphrase` | Read the configured passphrase    |
| GET    | `/settings/api-key`               | API key status                    |
| POST   | `/settings/api-key/generate`      | Generate the first API key        |
| POST   | `/settings/api-key/rotate`        | Rotate the API key                |
| DELETE | `/settings/api-key`               | Revoke the API key                |
| GET    | `/settings/staging`               | Staging directory info            |
| PUT    | `/settings/staging`               | Override the staging directory    |
| GET    | `/settings/database`              | Database snapshot settings        |
| PUT    | `/settings/database`              | Update database snapshot settings |
| POST   | `/settings/discord/test`          | Test the Discord webhook          |

### Discovery, Activity, Replication, and Recovery

| Method | Endpoint                 | Description                        |
| ------ | ------------------------ | ---------------------------------- |
| GET    | `/browse`                | Browse filesystem paths            |
| GET    | `/containers`            | Discover Docker containers         |
| GET    | `/vms`                   | Discover VMs                       |
| GET    | `/folders`               | Discover folder presets            |
| GET    | `/plugins`               | Discover plugins                   |
| GET    | `/activity`              | Activity log                       |
| GET    | `/replication`           | List replication sources           |
| POST   | `/replication`           | Create replication source          |
| POST   | `/replication/test-url`  | Test a replication URL and API key |
| GET    | `/replication/{id}`      | Get replication source             |
| PUT    | `/replication/{id}`      | Update replication source          |
| DELETE | `/replication/{id}`      | Delete replication source          |
| POST   | `/replication/{id}/test` | Test replication connection        |
| POST   | `/replication/{id}/sync` | Trigger replication immediately    |
| GET    | `/replication/{id}/jobs` | List replicated jobs               |
| GET    | `/recovery/plan`         | Recovery plan                      |

## MCP

Vault exposes MCP over two transports:

- Streamable HTTP at `http://<host>:24085/api/v1/mcp`
- Stdio via `vault mcp --db <path>`

The MCP surface is intentionally curated rather than a 1:1 mirror of every REST route.
It currently covers these tool groups:

- Jobs: `list_jobs`, `get_job`, `create_job`, `update_job`, `delete_job`, `run_job`, `get_job_history`
- Storage: `list_storage`, `get_storage`, `create_storage`, `update_storage`, `delete_storage`,
  `test_storage`, `list_storage_files`
- Discovery: `list_containers`, `list_vms`, `list_folders`, `list_plugins`
- Status: `get_health`, `get_health_summary`, `get_runner_status`, `get_activity_log`
- Restore: `list_restore_points`, `restore_item`
- Replication overview: `list_replication`, `get_replication`, `delete_replication`

REST remains the full management surface. Settings management, auth bootstrap, storage scan or import,
file downloads, replication create or sync flows, and the recovery plan endpoint are REST-only today.

## Architecture

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

## Deployment

### Unraid Plugin

Install via the Unraid Community Applications store, or use the manual install URL from the top of this README.

The daemon runs at `/usr/local/sbin/vault` with the database at
`/boot/config/plugins/vault/vault.db`.

Uninstall removes Vault-managed runtime and configuration traces, including the database,
while preserving backup data stored in configured storage destinations.

Service management:

```bash
/etc/rc.d/rc.vault start
/etc/rc.d/rc.vault stop
/etc/rc.d/rc.vault restart
/etc/rc.d/rc.vault status
```

### Ansible

```bash
cd ansible
cp inventory.yml.example inventory.yml
ansible-playbook -i inventory.yml ansible.yml --tags deploy
ansible-playbook -i inventory.yml ansible.yml --tags verify
```

See [ansible/README.md](ansible/README.md) for the full deployment workflow.

## Project Structure

```text
├── cmd/vault/           # CLI entry point
├── internal/
│   ├── api/             # HTTP server and REST handlers
│   ├── cli/             # Cobra CLI commands
│   ├── config/          # Enum constants and types
│   ├── db/              # SQLite database and repositories
│   ├── engine/          # Backup and restore logic
│   ├── mcp/             # MCP tools and transports
│   ├── notify/          # Unraid notifications
│   ├── replication/     # Remote Vault replication
│   ├── scheduler/       # Cron scheduler
│   ├── storage/         # Storage backend adapters
│   └── ws/              # WebSocket hub
├── plugin/              # Unraid plugin files (.plg, PHP, JS, CSS)
├── ansible/             # Deployment automation
├── scripts/             # Local development and verification helpers
├── web/                 # Svelte frontend
└── docs/                # Specs and reference docs
```

## Version

Vault uses date-based versioning in `VERSION` with the format `YYYY.MM.PATCH`.

## License

Vault is a third-party community plugin for Unraid OS.
