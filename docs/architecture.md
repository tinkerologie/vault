# Architecture

This page is for developers and contributors — it describes Vault's internal design. If you just want to use Vault, start with [Getting Started](getting-started.md).

Vault is a single Go binary that runs as a daemon on Unraid servers. It provides backup and restore for Docker containers, libvirt VMs, ZFS datasets, folders, and plugins.

Domain terms live in the [glossary](../GLOSSARY.md); source-linked trade-offs live in the [ADR index](adr/README.md).

## Layered Design

```text
CLI (Cobra) -> API Server (Chi + WebSocket Hub) -> Handlers -> DB / Storage / Engine / Runner
```

| Layer       | Package                 | Description                                                                                      |
| ----------- | ----------------------- | ------------------------------------------------------------------------------------------------ |
| CLI         | `internal/cli/`         | Cobra commands: `vault daemon`, `vault replica`, `vault dedup`, `vault recover`, `vault api-key` |
| API         | `internal/api/`         | Chi router, REST handlers, WebSocket integration                                                 |
| MCP         | `internal/mcp/`         | Model Context Protocol tools over streamable HTTP                                                |
| Job Intake  | `internal/jobs/`        | Applies the same policy to REST and MCP job changes. Imports use a separate path                 |
| Database    | `internal/db/`          | SQLite (WAL, pure-Go driver) with hybrid snapshot + USB shadow                                   |
| Storage     | `internal/storage/`     | Local, SFTP, SMB, NFS, WebDAV, and S3 adapters (factory-dispatched)                              |
| Engine      | `internal/engine/`      | Per-type backup/restore handlers (container, VM, ZFS, folder, plugin)                            |
| Runner      | `internal/runner/`      | Job orchestration, retention, verification, compression                                          |
| Dedup       | `internal/dedup/`       | Keyed-FastCDC chunker, per-destination dedup repo, GC                                            |
| Crypto      | `internal/crypto/`      | AES-256-GCM, server key, passphrase-derived data keys                                            |
| Replication | `internal/replication/` | Pull-mode replication client + syncer                                                            |
| Scheduler   | `internal/scheduler/`   | Cron-based scheduling                                                                            |
| WebSocket   | `internal/ws/`          | Real-time event hub for backup progress and config changes                                       |
| Notify      | `internal/notify/`      | Unraid notifications + Discord webhooks                                                          |
| Diagnostics | `internal/diagnostics/` | Redacted ZIP bundle (system info, schema, runs, scheduler, daemon log)                           |
| Logbuf      | `internal/logbuf/`      | In-memory ring buffer that captures every `log.*` line for diagnostics                           |

## Key Interfaces

### storage.Adapter

The [storage contract](../internal/storage/adapter.go) owns the current interface.
`WriteFrom` lets the retry layer start a new stream for a failed upload.
`ReadRange` reads part of a pack for deduplication.
`GetCapacity` supplies data for capacity reports.

Adapters that hold persistent resources (SFTP, SMB) implement `io.Closer` and
are released by `storage.CloseAdapter`. Bandwidth throttling is layered on
top via a `RateLimit` config field for every remote type. Adapters are
constructed via `storage.NewAdapter(type, configJSON)`.

### engine.Handler

```go
type Handler interface {
    Backup(ctx context.Context, item BackupItem, dest string, progress ProgressFunc) (*BackupResult, error)
    Restore(ctx context.Context, item BackupItem, source string, progress ProgressFunc) error
    ListItems() ([]BackupItem, error)
}
```

Handlers that support content-defined chunking additionally implement
`engine.ChunkedHandler` (`BackupChunked` / `RestoreChunked`) so the runner
can route them through the dedup repo on dedup-enabled destinations.

## Build Tags

- `vm.go`, `vm_restore.go`, and other libvirt-touching files use `//go:build linux` — real libvirt RPC against `/var/run/libvirt/libvirt-sock`
- `vm_stub.go`: `//go:build !linux` — stubs for macOS / Windows so the rest of the daemon still builds and tests run
- ZFS ([`internal/engine/zfs.go`](../internal/engine/zfs.go)) calls out to the host `zfs` binary and is Linux-only
- Local builds and the full test suite work on macOS without libvirt installed

## Database

SQLite with WAL mode and busy timeout. Pure Go driver via `modernc.org/sqlite` (no CGO).

```go
sql.Open("sqlite", path+"?_txlock=immediate&_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=journal_size_limit(67108864)")
```

Database opening applies the base schema and idempotent column/data migrations. See [database opening](../internal/db/db.go) and [migrations](../internal/db/migrations.go) for the current schema and upgrade behavior.

### Hybrid snapshot layout

To survive Unraid's USB-backed boot (where writing every commit to flash would wear the drive), the daemon runs in _hybrid_ mode by default:

1. **Working DB** — `/var/local/vault/vault.db` (in tmpfs. Vault does not write it directly to flash)
2. **Primary snapshot** — `<discovered cache pool>/.vault/vault.db` (primary copy on disk)
3. **USB shadow** — `/boot/config/plugins/vault/vault.db.backup` (refreshed after config changes via the `SetConfigChangeHook` pathway, so the flash copy stays current without per-row writes)

At startup, Vault selects the valid database copy with the newest modification time. It examines the primary snapshot, default cache snapshot, rotated copies, USB-direct database, and USB shadow. If no copy passes its checks, Vault creates a new database.

The modification time prevents an older cache snapshot from replacing a newer USB-direct database (issue #241). On array stop or reboot, `event/stopping_svcs` stops the daemon so the final flush can complete. When the array starts, `event/started` starts the daemon if necessary.

### Vault directories on Unraid storage

- **`<pool>/.vault/`** — the primary database snapshot (`vault.db`) plus timestamped copies under `rotated/`. Unraid exposes every top-level pool directory as a user share, so this same directory is also visible as **`/mnt/user/.vault`**. Use **Settings** → **General** → **Database Location** to move this directory.
- **`<pool>/.vault-stage/`** — temporary staging for backups and restores, emptied as each run finishes.
- **`/mnt/addons/vault-fuse/mount-<id>/`** holds a read-only FUSE mount. Vault creates the directory when the mount starts and removes it when the mount ends. Unraid reserves `/mnt/addons` for plugin and remote mounts. If the directory does not exist, Vault uses `<tmp>/vault-fuse`. Use `fuse_mount_base_dir` to change the root. Releases up to v2026.09.01 used `/mnt/vault-fuse`. Fix Common Problems flags this path. Vault removes the old directory at startup if it is empty.

Do not delete `.vault` or `.vault-stage` while Vault is running. Pool discovery never treats `addons` or `vault-fuse` as pools.

## Project Structure

```text
├── cmd/vault/             # CLI entry point (main + version ldflags)
├── internal/
│   ├── api/               # HTTP server and REST handlers
│   │   ├── server.go      # Server struct, StartWithContext
│   │   ├── routes.go      # Route registration
│   │   └── handlers/      # Job, Storage, Replication, Settings, Browse, …
│   ├── cli/               # Cobra subcommands (daemon, replica, dedup, recover)
│   ├── config/            # Enum constants and shared types
│   ├── crypto/            # Server key, AES-256-GCM, passphrase derivation
│   ├── db/                # SQLite, repos, hybrid snapshot manager
│   ├── dedup/             # Keyed-FastCDC chunker, repo, index, GC
│   ├── diagnostics/       # Redacted ZIP bundle for support
│   ├── engine/            # Per-type handlers (container, VM, ZFS, folder, plugin)
│   │   ├── container.go   # Docker SDK
│   │   ├── vm.go          # libvirt (Linux only)
│   │   ├── vm_stub.go     # Non-Linux stub
│   │   └── zfs.go         # zfs send/receive
│   ├── fsstat/            # Portable filesystem capacity (statfs / GetDiskFreeSpaceEx)
│   ├── logbuf/            # In-memory ring buffer for daemon-log capture
│   ├── mcp/               # MCP tools over streamable HTTP
│   ├── notify/            # Unraid notifications + Discord webhook
│   ├── recovery/          # Server-less, read-only backup recovery (vault recover)
│   ├── replication/       # Pull-mode client + syncer
│   ├── runner/            # Job orchestration, compression, retention, verify
│   ├── scheduler/         # Cron scheduler
│   ├── storage/           # Adapter interface + factory
│   │   ├── local.go       # Local filesystem
│   │   ├── sftp.go        # SFTP
│   │   ├── smb.go         # SMB
│   │   ├── nfs.go         # NFS
│   │   ├── webdav.go      # WebDAV (with chunked upload + manifest)
│   │   └── s3.go          # AWS SDK Go v2 (S3-compatible safe)
│   ├── unraid/            # Pool discovery, /mnt scanning helpers
│   └── ws/                # WebSocket hub
├── plugin/                # Unraid plugin files (.plg, PHP, RC script)
├── ansible/               # Build / deploy / verify automation
├── scripts/               # Local dev helpers (stress, fixtures)
├── web/                   # Svelte 5 frontend (Vite build embedded via go:embed)
└── docs/                  # Documentation
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
make build-local         # Build Linux/amd64 by default; GOOS/GOARCH can override
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
# On Linux, using the default build target:
./build/vault-linux-amd64 daemon --db=vault.db --addr=:24085
```

The production binary is built with `CGO_ENABLED=0` using `modernc.org/sqlite`, keeping it pure Go.

## Deployment

### Unraid Plugin

Install via the Unraid Community Applications store, or paste the plugin URL into Plugins > Install Plugin:

```text
https://raw.githubusercontent.com/tinkerologie/vault/main/plugin/vault.plg
```

The daemon runs at `/usr/local/sbin/vault` with the database at `/boot/config/plugins/vault/vault.db`.

The plugin installs `Vault.page`, `Backups.page`, `include/`, `ui/`, `event/` and `README.md` under `/usr/local/emhttp/plugins/vault`. The post-install step in `plugin/vault.plg` also deletes the removed legacy PHP pages and their `assets/` directory, during upgrades.

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

| Package                                  | Purpose                                         |
| ---------------------------------------- | ----------------------------------------------- |
| `github.com/go-chi/chi/v5`               | HTTP router                                     |
| `github.com/spf13/cobra`                 | CLI framework                                   |
| `github.com/robfig/cron/v3`              | Cron scheduler                                  |
| `modernc.org/sqlite`                     | Pure Go SQLite driver (no CGO)                  |
| `github.com/docker/docker`               | Docker Engine SDK                               |
| `github.com/digitalocean/go-libvirt`     | Pure Go libvirt RPC (Linux only)                |
| `github.com/vmware/go-nfs-client`        | NFS storage adapter                             |
| `github.com/cloudsoda/go-smb2`           | SMB storage adapter                             |
| `github.com/pkg/sftp`                    | SFTP storage adapter                            |
| `github.com/studio-b12/gowebdav`         | WebDAV storage adapter                          |
| `github.com/aws/aws-sdk-go-v2/...`       | S3 / S3-compatible storage adapter              |
| `github.com/PlakarKorp/go-cdc-chunkers`  | Keyed-FastCDC chunker for content-defined dedup |
| `github.com/coder/websocket`             | WebSocket server                                |
| `github.com/modelcontextprotocol/go-sdk` | MCP streamable-HTTP / stdio transport           |
| `filippo.io/age`                         | Optional age-encrypted archive layer            |

## Known Limitations

- **Garbage collection.** Vault deletes packs without live chunks. If compaction is on, Vault moves live chunks from eligible packs to new packs before it deletes the old packs. The Storage card shows **Reclaimable** space. The database records the results in `dedup_gc_runs`.

## Version

Vault uses date-based versioning in the `VERSION` file with the format `YYYY.MM.PATCH`. Version is injected via ldflags at build time (`-X main.version`, `-X main.buildDate`, `-X main.commit`).

## Top-navigation quick link (`Backups.page`)

`plugin/pages/Backups.page` adds an optional link to the Unraid navigation. It uses `Menu="Tasks"` and `Type="xmenu"`. Its `Cond` header reads `NAV_LINK` in `vault.cfg`. The Vault settings page changes that setting through the `navlink` action in `control.php`.

The page loads `include/app.php` in an iframe with `?embed=1`. In this mode, a row of links replaces the sidebar, header, and bottom bar. The frame does not have its own scroll area. A frame from a different origin or a top-level page with `?embed=1` uses the standalone view.

The Logs console uses the height of its render window. For follow, jump, and anchoring, it reads the visible part of the frame. It requests page scrolls through `web/src/lib/consoleport.js`. The `include/embed-host.js` script uses same-origin `postMessage` messages to set the frame height. It checks the origin, source, and format of each message. It reports the visible part of the frame so dialogs and messages stay on screen.

The `app.php` script reads `[display] theme` in `dynamix.cfg`. The values `black` and `gray` select dark mode. The values `white` and `azure` select light mode. Other values give no default. A saved Vault choice, including System, takes precedence.

The `app.php` script gives the SPA a CSRF token each time it renders. If the tab was hidden for more than 10 minutes, the frame reloads when the tab becomes visible. This lets the SPA get a new token for write requests. In proxy mode, the SPA polls for updates, as it does on the Utilities page.
