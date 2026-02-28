# Vault - Unraid Backup & Restore Plugin Design

## Overview

Vault is a Veeam-style backup and restore plugin for Unraid 7+ servers. It provides unified backup management for Docker containers and virtual machines with support for local and remote storage destinations.

## Architecture

### Tech Stack

- **Backend:** Go (single binary, CLI + daemon hybrid)
- **Frontend:** PHP `.page` files + JavaScript (Unraid web UI integration)
- **Database:** SQLite (config and state, stored on USB flash drive)
- **Communication:** REST API + WebSocket (Go daemon to PHP frontend)

### Binary Modes

- `vault daemon` — REST API server, WebSocket server, job scheduler
- `vault <command>` — CLI commands for manual operations and scripting

### Architecture Diagram

```
+--------------------------------------------------+
|                  vault binary                     |
|                                                   |
|  +----------+  +----------+  +----------------+  |
|  | REST API |  |WebSocket |  |  CLI Handler   |  |
|  | Server   |  | Server   |  |                |  |
|  +----+-----+  +----+-----+  +-------+--------+  |
|       |              |                |           |
|  +----v--------------v----------------v--------+  |
|  |              Job Manager                     |  |
|  |  (create, schedule, execute, monitor jobs)   |  |
|  +-------------------+-------------------------+  |
|                      |                            |
|  +-------------------v-------------------------+  |
|  |            Backup Engine                     |  |
|  |  +----------+  +----------+  +----------+   |  |
|  |  |Container |  |    VM    |  |  Flash   |   |  |
|  |  | Handler  |  | Handler  |  | Handler  |   |  |
|  |  +----------+  +----------+  +----------+   |  |
|  +-------------------+-------------------------+  |
|                      |                            |
|  +-------------------v-------------------------+  |
|  |           Storage Adapters                   |  |
|  |  +-----+ +-----+ +-----+ +-----+ +-----+   |  |
|  |  |Local| | SMB | | NFS | |SFTP | | S3  |   |  |
|  |  +-----+ +-----+ +-----+ +-----+ +-----+   |  |
|  +----------------------------------------------+  |
|                                                   |
|  +----------------------------------------------+  |
|  |           SQLite (config & state)             |  |
|  +----------------------------------------------+  |
+--------------------------------------------------+
```

## Container Backup & Restore

### What Gets Backed Up

1. **Container image** — via Docker SDK `ImageSave` API
2. **Container configuration** — via Docker SDK `ContainerInspect` API (env vars, ports, volumes, networks, labels, restart policy)
3. **Appdata / volumes** — all bind mounts and named volumes, tar'd with optional compression

### Backup Process

1. Stop container (or skip if marked "don't stop") via Docker SDK
2. Save image via `ImageSave` API
3. Capture config via `ContainerInspect` API as JSON
4. Tar appdata directories with chosen compression (none/gzip/zstd)
5. Start container back up via Docker SDK
6. Two modes: "stop all then backup all" or "stop-backup-start one at a time"
7. Container ordering: configurable stop/start order via groups

### Restore Process

1. Select restore point from UI or CLI
2. Choose which containers to restore
3. Load image via Docker SDK
4. Recreate container from saved config (or update existing)
5. Restore appdata from tar archive
6. Start the container

### API Usage

All container operations use the Docker Engine SDK for Go (via `/var/run/docker.sock`) — no CLI commands.

## VM Backup & Restore

### What Gets Backed Up

1. **Virtual disks (vdisks)** — qcow2/raw/img files
2. **VM XML configuration** — domain XML via libvirt API
3. **NVRAM/UEFI vars** — OVMF firmware state files
4. **TPM state** — if present

### Backup Types

- **Full:** Complete copy of all vdisk files
- **Incremental:** QEMU dirty bitmap tracking (changed blocks since last backup)
- **Differential:** Changed blocks since last full backup

### Backup Process

1. Per-VM choice: live snapshot or cold backup
2. **Live snapshot:** Create QEMU external snapshot via libvirt API, copy backing files (read-only), commit/delete snapshot — VM stays running
3. **Cold backup:** Graceful shutdown via libvirt API, copy vdisk files, restart VM
4. Extract domain XML via libvirt API
5. Copy NVRAM/TPM state files
6. ISOs and passthrough devices excluded by default

### Restore Process

1. Select restore point
2. For incremental/differential: reconstruct full image from backup chain
3. Define/create VM from saved XML via libvirt API
4. Write vdisk files to target location
5. Restore NVRAM/TPM state
6. Optionally start the VM

### API Usage

All VM operations use go-libvirt bindings — no virsh CLI commands.

## Storage Adapters

### Interface

```go
type StorageAdapter interface {
    Write(path string, reader io.Reader) error
    Read(path string) (io.ReadCloser, error)
    Delete(path string) error
    List(prefix string) ([]FileInfo, error)
    Stat(path string) (FileInfo, error)
    TestConnection() error
}
```

### Adapters

| Adapter  | Library                     | Notes                               |
| -------- | --------------------------- | ----------------------------------- |
| Local    | `os` package                | Array shares, unassigned disks      |
| SMB      | `go-smb2`                   | Windows shares, other NAS           |
| NFS      | OS mount + local            | Mounted then treated as local       |
| SFTP/SSH | `x/crypto/ssh` + `pkg/sftp` | Remote Linux servers                |
| S3       | `aws-sdk-go-v2`             | AWS S3, Backblaze B2, MinIO, Wasabi |

Multiple named destinations configurable per job.

## Job System (Veeam-style)

### Backup Job Properties

- Name and description
- Item selection: containers and/or VMs with per-item settings
- Storage destination(s)
- Backup type chain: e.g., "Full every Sunday, incremental daily"
- Schedule: presets (daily, weekly, monthly) + custom cron
- Retention policy: keep N restore points and/or delete after N days
- Pre/post scripts
- Notification preferences (success/failure/warning via Unraid notifications)

### Execution Flow

1. Scheduler triggers job (or manual trigger)
2. Determine backup type from chain rules
3. Execute pre-job script
4. Process each item via appropriate handler
5. Write to configured storage destination(s)
6. Execute post-job script
7. Apply retention policy
8. Send notification
9. Record in job history (SQLite)

### Restore Points

Each successful job creates a restore point with metadata: items backed up, timestamp, backup type, storage location.

## Web UI

Custom tab in the Unraid web interface with these pages:

1. **Dashboard** — Job overview, recent activity, next scheduled runs, storage usage, alerts
2. **Jobs** — Wizard-style job creation:
   - Step 1: Name the job
   - Step 2: Select containers/VMs
   - Step 3: Choose storage destination
   - Step 4: Configure schedule and backup type chain
   - Step 5: Set retention policy
   - Step 6: Review and save
3. **Restore** — Browse restore points, select items, restore options
4. **Storage** — Configure and test storage destinations
5. **History** — Job execution logs, filterable by job/status/date
6. **Settings** — Global plugin settings

Frontend calls Go daemon's REST API. WebSocket for live backup/restore progress.

## Plugin Packaging

### File Structure

```
/boot/config/plugins/vault/
  vault.plg              # Plugin installer
  vault                  # Go binary (amd64)
  vault.db               # SQLite database

/usr/local/emhttp/plugins/vault/
  Vault.page             # Dashboard
  Vault.Jobs.page        # Jobs
  Vault.Restore.page     # Restore
  Vault.Storage.page     # Storage
  Vault.History.page     # History
  Vault.Settings.page    # Settings
  include/
    api.php              # PHP helper for REST API calls
  assets/
    js/                  # JavaScript
    css/                 # Stylesheets

/etc/rc.d/rc.vault       # Daemon start/stop script
```

### Lifecycle

- **Install:** Download Go binary, place files, start daemon
- **Boot:** Re-install from USB, start daemon
- **Update:** Download new binary, restart daemon
- **Remove:** Stop daemon, clean up files and cron entries

## Error Handling

- Failed items don't abort entire job — continues with remaining items, reports partial success
- Configurable retry count for transient failures
- All errors logged to SQLite with context
- Unraid notifications on failure/warning

## Testing Strategy

- Unit tests for core logic (storage adapters, handlers, scheduling)
- Integration tests with Docker/libvirt
- Mock adapters for storage testing
- CI pipeline building amd64 Linux binary

## Target Platform

- Unraid 7+ only
- Go binary compiled for linux/amd64

## Implementation Tasks

| Task ID | Task                                           | Blocked By      |
| ------- | ---------------------------------------------- | --------------- |
| #7      | Core architecture (daemon + CLI + job manager) | —               |
| #8      | Container backup handler (Docker SDK)          | #7              |
| #9      | VM backup handler (libvirt API)                | #7              |
| #10     | Storage adapters                               | #7              |
| #11     | Job system and scheduler                       | #7, #8, #9, #10 |
| #12     | PHP web UI                                     | #7, #11         |
| #13     | Plugin packaging (.plg)                        | #7, #12         |
