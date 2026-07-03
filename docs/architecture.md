# Architecture

Vault is a single Go binary that runs as a daemon on Unraid servers. It provides backup and restore for Docker containers, libvirt VMs, ZFS datasets, folders, and plugins.

## Layered Design

```text
CLI (Cobra) -> API Server (Chi + WebSocket Hub) -> Handlers -> DB / Storage / Engine / Runner
```

| Layer       | Package                 | Description                                                                 |
| ----------- | ----------------------- | --------------------------------------------------------------------------- |
| CLI         | `internal/cli/`         | Cobra commands: `vault daemon`, `vault replica`, `vault mcp`, `vault dedup` |
| API         | `internal/api/`         | Chi router, REST handlers, WebSocket integration                            |
| MCP         | `internal/mcp/`         | Model Context Protocol tools over streamable HTTP and stdio                 |
| Database    | `internal/db/`          | SQLite (WAL, pure-Go driver) with hybrid snapshot + USB shadow              |
| Storage     | `internal/storage/`     | Local, SFTP, SMB, NFS, WebDAV, and S3 adapters (factory-dispatched)         |
| Engine      | `internal/engine/`      | Per-type backup/restore handlers (container, VM, ZFS, folder, plugin)       |
| Runner      | `internal/runner/`      | Job orchestration, retention, verification, compression                     |
| Dedup       | `internal/dedup/`       | Keyed-FastCDC chunker, per-destination dedup repo, GC                       |
| Crypto      | `internal/crypto/`      | AES-256-GCM, server key, passphrase-derived data keys                       |
| Replication | `internal/replication/` | Pull-mode replication client + syncer                                       |
| Scheduler   | `internal/scheduler/`   | Cron-based scheduling                                                       |
| WebSocket   | `internal/ws/`          | Real-time event hub for backup progress and config changes                  |
| Notify      | `internal/notify/`      | Unraid notifications + Discord webhooks                                     |
| Diagnostics | `internal/diagnostics/` | Redacted ZIP bundle (system info, schema, runs, scheduler, daemon log)      |
| Logbuf      | `internal/logbuf/`      | In-memory ring buffer that captures every `log.*` line for diagnostics      |

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

Adapters that hold persistent resources (SFTP, SMB) implement `io.Closer` and
are released by `storage.CloseAdapter`. Bandwidth throttling is layered on
top via a `RateLimit` config field for every remote type. Adapters are
constructed via `storage.NewAdapter(type, configJSON)`.

### engine.Handler

```go
type Handler interface {
    Backup(item BackupItem, dest string, progress ProgressFunc) (*BackupResult, error)
    Restore(item BackupItem, source string, progress ProgressFunc) error
    ListItems() ([]BackupItem, error)
}
```

Handlers that support content-defined chunking additionally implement
`engine.ChunkedHandler` (`BackupChunked` / `RestoreChunked`) so the runner
can route them through the dedup repo on dedup-enabled destinations.

## Build Tags

- `vm.go`, `vm_restore.go`, and other libvirt-touching files use `//go:build linux` — real libvirt RPC against `/var/run/libvirt/libvirt-sock`
- `vm_stub.go`: `//go:build !linux` — stubs for macOS / Windows so the rest of the daemon still builds and tests run
- ZFS (`engine/zfs.go`) calls out to the host `zfs` binary; the code itself is platform-neutral
- Local builds and the full test suite work on macOS without libvirt installed

## Database

SQLite with WAL mode and busy timeout. Pure Go driver via `modernc.org/sqlite` (no CGO).

```go
sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
```

Schema is applied inline at open time via `CREATE TABLE IF NOT EXISTS` (no versioned migrations). Twelve tables: `storage_destinations`, `jobs`, `job_items`, `job_runs`, `restore_points`, `settings`, `activity_log`, `verify_runs`, `replication_sources`, `dedup_packs`, `dedup_chunks`, `dedup_gc_runs`.

### Hybrid snapshot layout

To survive Unraid's USB-backed boot (where writing every commit to flash would wear the drive), the daemon runs in _hybrid_ mode by default:

1. **Working DB** — `/var/local/vault/vault.db` (tmpfs-backed; fast, never written to flash directly)
2. **Primary snapshot** — `<discovered cache pool>/.vault/vault.db` (periodic copies; the authoritative on-disk source)
3. **USB shadow** — `/boot/config/plugins/vault/vault.db.backup` (refreshed after config changes via the `SetConfigChangeHook` pathway, so the flash copy stays current without per-row writes)

On startup, `internal/db/snapshot.go` restores from the primary snapshot if present, otherwise from the USB shadow — falling back to a fresh DB only if neither exists.

## Project Structure

```text
├── cmd/vault/             # CLI entry point (main + version ldflags)
├── internal/
│   ├── api/               # HTTP server and REST handlers
│   │   ├── server.go      # Server struct, StartWithContext
│   │   ├── routes.go      # Route registration
│   │   └── handlers/      # Job, Storage, Replication, Settings, Browse, …
│   ├── cli/               # Cobra subcommands (daemon, replica, mcp, dedup)
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
│   ├── logbuf/            # In-memory ring buffer for daemon-log capture
│   ├── mcp/               # MCP tools + streamable HTTP / stdio transport
│   ├── notify/            # Unraid notifications + Discord webhook
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

- **Dedup GC is delete-only.** Garbage collection removes packs whose every chunk is
  unreferenced. A pack with even one live chunk is kept; its dead bytes are reported as
  **Reclaimable** ("rewritable bytes") on the Storage card and persisted per GC run in
  `dedup_gc_runs`, but they are not yet physically reclaimed. Compaction/repacking is
  tracked in #103. This matches early borg/restic behaviour and is a deliberate v1 choice —
  small (24 MiB) packs bound the stranded space.

## Version

Vault uses date-based versioning in the `VERSION` file with the format `YYYY.MM.PATCH`. Version is injected via ldflags at build time (`-X main.version`, `-X main.buildDate`, `-X main.commit`).

## Top-navigation quick link (`Backups.page`)

`plugin/pages/Backups.page` is an optional top-nav entry (`Menu="Tasks"`, `Type="xmenu"` — the same extension point Community Applications uses) gated by `NAV_LINK` in `vault.cfg` via its `Cond` header, toggled from the Vault settings page through `control.php`'s `navlink` action. It embeds the SPA's proxy entrypoint (`include/app.php`) in an iframe sized to the viewport. Because `app.php` injects Unraid's CSRF token once per render and the SPA has no in-app refresh, the frame reloads itself when the tab regains visibility after >10 minutes hidden so write actions keep working across token rotations. In proxy mode the SPA polls; WebSocket-only live events do not stream here (matching the Utilities page).
