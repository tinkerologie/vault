# Vault Replica Container & appdata.backup Import — Design

## Goal

Two features: (1) a Docker container (`vault-replica`) that receives replicated backups from a Vault plugin for off-site disaster recovery on any Docker-capable device, and (2) a built-in import tool for migrating from Commifreak's `unraid-appdata.backup` plugin.

## Architecture

### Vault Replica Container

**Same binary, new mode.** A `vault replica` CLI command starts a minimal Vault daemon that:

- Runs the HTTP API server with the full Svelte SPA (read-only mode)
- Pulls backups from configured Vault sources on a cron schedule
- Stores replicated data to a local volume mount
- Skips all backup-creation functionality (Docker, libvirt, runner, Unraid-specific code)

**Pull-based replication.** The replica container uses the existing `internal/replication` syncer to pull from remote Vault instances. The main Vault plugin exposes its jobs, restore points, and storage files via API. The replica connects, lists what's new, and downloads missing files.

**Read-only enforcement.** Write endpoints (create/update/delete jobs, run backups, etc.) return `403 Forbidden` with a "read-only replica" message. The SPA detects this mode and hides create/edit/run controls.

### appdata.backup Import

**Built into Vault's Storage page.** Extends the existing scan/import infrastructure to recognize `ab_*` directories alongside Vault-native `manifest.json` files.

**Per-container restore points.** Each `.tar.gz` file in an `ab_*` directory becomes its own restore point under a dedicated job named after the container. Files are not moved or converted — Vault creates restore points pointing to the original paths.

## Tech Stack

- Go 1.26 (same binary, build tags for replica mode)
- Docker multi-arch build: `linux/amd64`, `linux/arm64`
- Alpine-based Docker image
- Published to Docker Hub (`ruaandeysel/vault-replica`) and GHCR (`ghcr.io/ruaandeysel/vault-replica`)
- Same Svelte 5 SPA with a `readOnly` flag

---

## Feature 1: Vault Replica Container

### CLI Command

```bash
vault replica --db=/data/vault.db --addr=:24085 --api-key=<key>
```

Flags:

- `--db` — SQLite database path (default: `/data/vault.db`)
- `--addr` — Listen address (default: `:24085`)
- `--api-key` — API key for authentication (or `VAULT_API_KEY` env var)
- `--tls-cert` / `--tls-key` — Optional TLS

### What It Starts

| Component             | Included | Notes                         |
| --------------------- | -------- | ----------------------------- |
| HTTP API (Chi)        | Yes      | Read-only mode flag           |
| Svelte SPA            | Yes      | Hides write controls          |
| WebSocket hub         | Yes      | Sync progress streaming       |
| SQLite database       | Yes      | Standard mode (no hybrid RAM) |
| Replication syncer    | Yes      | Pull from configured sources  |
| Replication scheduler | Yes      | Cron-based sync schedule      |
| Crypto (API key seal) | Yes      | Same server key mechanism     |
| Activity log          | Yes      | Track sync events             |

### What It Skips

| Component              | Reason                     |
| ---------------------- | -------------------------- |
| Docker SDK             | No container management    |
| libvirt                | No VM management           |
| Runner (backup engine) | No backup creation         |
| Container/VM discovery | No hosts to discover       |
| Backup scheduler       | No backup jobs to schedule |
| Unraid notifications   | Not Unraid-specific        |
| Hybrid RAM database    | No cache drive detection   |
| Pre/post scripts       | No backup execution        |
| MCP server             | Not needed                 |

### Daemon Startup Sequence

```go
func runReplica(cmd *cobra.Command, args []string) error {
    // 1. Open SQLite database (standard mode, no hybrid)
    // 2. Run migrations
    // 3. Load/create server key
    // 4. Create API server with readOnly=true
    // 5. Create replication syncer
    // 6. Create scheduler (replication schedules only)
    // 7. Start server
    // 8. Listen for SIGTERM for graceful shutdown
}
```

### API Read-Only Enforcement

The `Server` gains a `readOnly bool` field. When true, a middleware wraps all write routes:

```go
func ReadOnlyGuard(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodGet && r.Method != http.MethodHead {
            respondJSON(w, http.StatusForbidden, map[string]string{
                "error": "this is a read-only replica",
            })
            return
        }
        next.ServeHTTP(w, r)
    })
}
```

Exceptions: replication CRUD (must be writable so users can add sources), settings API key management, and the WebSocket endpoint.

### SPA Read-Only Mode

The `/api/v1/health` endpoint gains a `"mode": "replica"` field. The SPA checks this on load:

```js
const health = await api.get("/health");
const isReplica = health.mode === "replica";
```

When `isReplica` is true:

- Hide "Run Now", "Create Job", "Edit Job", "Delete Job" buttons
- Hide backup-related pages (or show them read-only)
- Show a "Replica" badge in the sidebar
- Navigation shows: Dashboard, Replication, History, Settings
- Dashboard shows replicated jobs and last sync status

### Docker Image

```dockerfile
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY vault /usr/local/bin/vault
EXPOSE 24085
VOLUME /data
ENTRYPOINT ["vault", "replica"]
CMD ["--db=/data/vault.db", "--addr=:24085"]
```

### Docker Compose Example

```yaml
services:
  vault-replica:
    image: ruaandeysel/vault-replica:latest
    container_name: vault-replica
    ports:
      - "24085:24085"
    volumes:
      - ./vault-data:/data
    environment:
      - VAULT_API_KEY=my-secret-key
    restart: unless-stopped
```

### CI/CD

New GitHub Actions workflow: `.github/workflows/docker.yml`

- Trigger: on tag push (`v*`)
- Build: multi-arch (`linux/amd64`, `linux/arm64`) using `docker/build-push-action`
- Push to Docker Hub and GHCR
- Uses the same `vault` binary built by the existing `build.yml`

### User Flow

1. User installs `vault-replica` Docker container on their NAS/server
2. Sets an API key via environment variable
3. Opens the web UI at `http://nas-ip:24085`
4. Goes to Replication page, adds their Unraid Vault server as a source
5. Clicks "Sync Now" or waits for the scheduled sync
6. Backups are pulled from Vault and stored locally
7. The dashboard shows all replicated jobs and restore points

---

## Feature 2: appdata.backup Import

### Background

[Commifreak/unraid-appdata.backup](https://github.com/Commifreak/unraid-appdata.backup) is a popular Unraid backup plugin now in feature freeze. Many users have existing backups in `ab_*` directories. Vault should import these seamlessly.

### Backup Format Analysis

Each `ab_YYYYMMDD_HHMMSS/` directory contains:

| File                 | Description                                             |
| -------------------- | ------------------------------------------------------- |
| `config.json`        | Backup settings: container list, schedules, exclusions  |
| `{container}.tar.gz` | Compressed tar of the container's appdata volume        |
| `my-{container}.xml` | Docker container XML configuration (Unraid format)      |
| `backup.log`         | Execution log with timestamps and per-container results |
| `cube-*.zip`         | Optional Unraid flash backup                            |

Example from the live system:

```
ab_20260304_040001/
├── config.json              (5.7 KB)
├── backup.log               (12.6 KB)
├── homeassistant.tar.gz     (1.1 GB)
├── plex.tar.gz              (4.8 GB)
├── code-server.tar.gz       (182 MB)
├── jackett.tar.gz           (1.8 MB)
├── mosquitto.tar.gz         (13.6 KB)
├── my-homeassistant.xml     (2.1 KB)
├── my-plex.xml              (3.6 KB)
├── ... (14 containers total)
└── cube-v7.2.4-flash-backup-20260304-0409.zip (1.0 GB)
```

### Import Design

#### Scanner Extension

Extend `Runner.ScanStorageManifests()` to also scan for `ab_*` directories. New method:

```go
func (r *Runner) ScanAppdataBackups(adapter storage.Adapter, basePath string) ([]map[string]any, error)
```

This method:

1. Lists entries at `basePath` matching the `ab_` prefix
2. For each `ab_*` directory, reads `config.json` and lists `.tar.gz` files
3. Returns one manifest entry per container per backup run

#### Manifest Structure

Each discovered container backup produces a manifest:

```json
{
  "source": "appdata.backup",
  "job_name": "homeassistant",
  "storage_path": "ab_20260304_040001/homeassistant.tar.gz",
  "backup_type": "full",
  "compression": "gzip",
  "size_bytes": 1187689750,
  "created_at": "2026-03-04T04:02:04Z",
  "metadata": {
    "imported_from": "appdata.backup",
    "container_xml": "my-homeassistant.xml",
    "backup_log": "backup.log",
    "original_path": "/mnt/user/backups/ab_20260304_040001"
  }
}
```

#### Import Flow

1. User adds a storage destination pointing to `/mnt/user/backups` (or wherever `ab_*` folders live)
2. Clicks "Scan" on the storage page
3. Vault scans for both Vault manifests and `ab_*` directories
4. UI shows discovered backups with a "[appdata.backup]" tag
5. User clicks "Import" to create jobs and restore points

#### Job Creation

Each unique container name creates a disabled Vault job:

```go
job := db.Job{
    Name:            containerName,         // e.g., "homeassistant"
    Description:     "Imported from appdata.backup",
    Enabled:         false,
    BackupTypeChain: "full",
    Compression:     "gzip",
    ContainerMode:   "one_by_one",
    RetentionCount:  7,
    RetentionDays:   30,
    StorageDestID:   storageDestID,
}
```

#### Restore Point Creation

Each `{container}.tar.gz` in each `ab_*` folder creates a restore point:

```go
rp := db.RestorePoint{
    JobID:       jobID,
    BackupType:  "full",
    StoragePath: "ab_20260304_040001/homeassistant.tar.gz",
    SizeBytes:   fileSize,
    Metadata:    metadataJSON,  // includes container XML, import source
}
```

#### Flash Backup Handling

`cube-*.zip` files are imported under a special job named "flash-backup":

```go
job := db.Job{
    Name:        "flash-backup",
    Description: "Imported Unraid flash backup from appdata.backup",
    // ...
}
```

#### Timestamp Extraction

The backup timestamp comes from:

1. **Primary:** Parse the `ab_` directory name: `ab_20260304_040001` → `2026-03-04T04:00:01`
2. **Fallback:** Parse `backup.log` for per-container completion times

#### Deduplication

Import uses the same deduplication as existing imports: check `storage_path` against existing restore points. Re-importing the same `ab_*` directories is safe and idempotent.

### UI Changes

The Storage page's scan results include an `"appdata.backup"` source tag. The import confirmation dialog groups results by source:

```
Vault Backups (12 found)
  └── my-containers: 6 restore points
  └── vm-backups: 6 restore points

appdata.backup Imports (98 found)
  └── homeassistant: 7 backups (7.8 GB)
  └── plex: 7 backups (33.6 GB)
  └── code-server: 7 backups (1.3 GB)
  └── ... 11 more containers
```

---

## Implementation Order

1. **appdata.backup import** — Smaller scope, immediate value for existing users migrating to Vault
2. **`vault replica` command** — Core replica daemon mode
3. **Read-only SPA mode** — UI changes for replica mode
4. **Dockerfile + CI/CD** — Multi-arch build and publish
5. **Documentation** — README, Docker Hub description, migration guide
