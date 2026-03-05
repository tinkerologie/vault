# Hybrid RAM Database Design

## Problem

Vault's SQLite database lives at `/boot/config/plugins/vault/vault.db` — directly on the Unraid USB flash drive. Every backup job generates 15-20+ write transactions (progress updates per item, job run record, restore point, activity logs). With WAL mode, each 4KB page write can trigger a 128KB+ flash erase-rewrite cycle due to write amplification. Consumer USB drives use TLC/QLC NAND with 1,000-5,000 P/E cycles and minimal wear leveling, making hotspot writes (like a frequently-updated SQLite file) a durability concern.

Additionally, cheap USB drives often lie about `fsync` completion — a known cause of SQLite corruption on Unraid.

## Solution

Run the working SQLite database on tmpfs (RAM-backed filesystem) and persist snapshots to the cache drive (SSD/NVMe) using SQLite's online backup API. This eliminates USB flash writes entirely while maintaining data durability through periodic snapshots.

## Architecture

```
Boot:     [cache:/vault.db] --backup API--> [tmpfs:/vault.db]
                                                 |
Run:                                        [daemon operates here]
                                                 |
Flush:    [tmpfs:/vault.db] --backup API--> [cache:/vault.db]
                                                 |
Shutdown:                                   [final flush + cleanup]
```

### Operating Modes (auto-detected)

| Mode                 | Working DB                            | Snapshot Location            | When                             |
| -------------------- | ------------------------------------- | ---------------------------- | -------------------------------- |
| **Hybrid** (default) | `/var/local/vault/vault.db` (tmpfs)   | `/mnt/cache/.vault/vault.db` | Cache drive exists               |
| **Direct SSD**       | User-specified path                   | N/A                          | User sets `--db` to non-USB path |
| **Legacy USB**       | `/boot/config/plugins/vault/vault.db` | N/A                          | No cache drive, fallback         |

### Flush Triggers

Snapshots are saved (RAM → cache drive) at these points:

1. **After each completed backup job** — in `runner.RunJob()` after restore point creation
2. **On graceful shutdown** — SIGTERM/SIGINT signal handler calls final flush
3. **On settings changes** — after `updateSettings()` to persist configuration

This means worst-case data loss on power failure is one in-progress backup run. The job re-runs on next schedule, and `CleanupStaleRuns()` marks interrupted runs as failed on next startup.

## Components

### 1. `internal/db/snapshot.go` (new)

```go
type SnapshotManager struct {
    db           *DB
    snapshotPath string  // persistent location on cache drive
    workingPath  string  // tmpfs location
    mu           sync.Mutex
}
```

**Methods:**

- `NewSnapshotManager(db *DB, snapshotPath, workingPath string) *SnapshotManager`
- `RestoreFromSnapshot() error` — backup API copy: snapshot → working DB at startup
- `SaveSnapshot() error` — backup API copy: working DB → snapshot after job completion
- `Close() error` — final SaveSnapshot + remove tmpfs WAL/SHM files

**Backup API pattern** (using `modernc.org/sqlite` v1.24.0+):

```go
type backuper interface {
    NewBackup(string) (*sqlite.Backup, error)
    NewRestore(string) (*sqlite.Backup, error)
}

func (sm *SnapshotManager) SaveSnapshot() error {
    sm.mu.Lock()
    defer sm.mu.Unlock()

    conn, _ := sm.db.sqlDB.Conn(context.Background())
    defer conn.Close()

    return conn.Raw(func(driverConn any) error {
        bck, err := driverConn.(backuper).NewBackup(sm.snapshotPath)
        if err != nil { return err }
        for more := true; more; {
            more, err = bck.Step(-1)
            if err != nil { return err }
        }
        return bck.Finish()
    })
}
```

### 2. `internal/cli/daemon.go` (modified)

**Startup sequence:**

1. Check if `/mnt/cache` exists and is a directory (cache drive available)
2. If cache drive available:
   - Set `workingPath = /var/local/vault/vault.db`
   - Set `snapshotPath = /mnt/cache/.vault/vault.db`
   - If first run and USB DB exists: migrate USB DB → snapshot path
   - If snapshot exists: restore snapshot → tmpfs via backup API
   - Open DB at tmpfs path
3. If no cache drive:
   - Open DB at `--db` flag path (default: USB)
   - Log warning about USB wear

**Shutdown:** Signal handler calls `snapshotManager.Close()` before exit.

### 3. `internal/runner/runner.go` (modified, ~5 lines)

After job completion (after restore point creation):

```go
if r.snapshotManager != nil {
    if err := r.snapshotManager.SaveSnapshot(); err != nil {
        log.Printf("runner: snapshot save error: %v", err)
    }
}
```

### 4. First-Run Migration

When hybrid mode is first enabled and the USB database has data:

1. Create `/mnt/cache/.vault/` directory
2. Copy USB DB → cache snapshot path using backup API (not file copy — ensures clean state)
3. Rename USB DB to `vault.db.migrated` (safety backup)
4. Log migration event

### 5. Settings UI Info Card

Read-only card in Settings → General tab:

```
Database Location
├── Working: /var/local/vault/vault.db (RAM)
├── Snapshot: /mnt/cache/.vault/vault.db (SSD)
├── Last snapshot: 2 minutes ago
└── Mode: Hybrid (RAM + SSD snapshots)
```

### 6. API Endpoint

```
GET /api/v1/settings/database
→ {
    "mode": "hybrid",           // "hybrid" | "direct" | "legacy_usb"
    "working_path": "/var/local/vault/vault.db",
    "snapshot_path": "/mnt/cache/.vault/vault.db",
    "last_snapshot": "2026-03-04T10:30:00Z",
    "snapshot_size_bytes": 4096000
  }
```

## Error Handling

### Power loss / crash

- tmpfs DB is lost
- Next boot: restore from last snapshot on cache drive
- Data loss: changes since last `SaveSnapshot()` (at most one backup job)
- `CleanupStaleRuns()` marks interrupted runs as failed on startup

### Cache drive failure

- `SaveSnapshot()` returns error — logged, daemon continues operating from tmpfs
- On next boot: if cache drive recovers, restores from (possibly stale) snapshot
- If cache drive permanently gone: falls back to legacy USB mode

### No cache drive

- Auto-detected at startup
- Operates in legacy mode on USB
- Settings page shows warning

### tmpfs full

- Unlikely — DB is typically <5MB, Unraid tmpfs is ~50% of RAM
- SQLite returns "disk full" errors, daemon logs but stays running

### Concurrent access

- `SnapshotManager.mu` mutex prevents concurrent snapshots
- `MaxOpenConns(1)` prevents concurrent SQLite writes
- Backup API acquires shared lock — reads continue during snapshot

## Non-Goals

- **Not configurable by users** — cache drive detection is automatic
- **No periodic timer-based flushes** — event-driven only (job completion, shutdown, settings changes)
- **No WAL mode changes** — WAL works on tmpfs since it's a real filesystem
- **No schema changes** — all existing tables and queries work unchanged

## Testing Strategy

- Unit test `SnapshotManager` with temp directories (backup/restore roundtrip)
- Unit test migration path (USB → cache)
- Integration test: open on tmpfs, write data, snapshot, kill process, restore, verify data
- Verify on actual Unraid server via `make redeploy`
- macOS/dev: skip hybrid mode (no tmpfs), operate directly on `--db` path
