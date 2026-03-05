# Hybrid RAM Database Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:executing-plans to implement this plan task-by-task.

**Goal:** Eliminate USB flash drive writes by running the working SQLite database on tmpfs (RAM) and persisting snapshots to the cache drive via SQLite's online backup API.

**Architecture:** Three auto-detected operating modes: Hybrid (tmpfs + cache drive snapshots), Direct SSD (user-specified path), and Legacy USB (fallback). The SnapshotManager wraps SQLite's backup API to copy between tmpfs and cache drive at startup, after job completion, on settings changes, and on graceful shutdown.

**Tech Stack:** Go, `modernc.org/sqlite` v1.46.1 (backup API via `NewBackup`/`NewRestore`), SQLite WAL mode, tmpfs at `/var/local/vault/`, cache drive at `/mnt/cache/.vault/`.

**Design Doc:** `docs/plans/2026-03-04-hybrid-ram-database-design.md`

---

### Task 1: SnapshotManager Core

**Files:**

- Create: `internal/db/snapshot.go`
- Create: `internal/db/snapshot_test.go`

**Step 1: Write the failing test**

Create `internal/db/snapshot_test.go`:

```go
package db

import (
 "os"
 "path/filepath"
 "testing"
)

func TestSnapshotRoundTrip(t *testing.T) {
 // Create two temp dirs: one for "working" DB, one for "snapshot" location.
 workDir := t.TempDir()
 snapDir := t.TempDir()

 workPath := filepath.Join(workDir, "vault.db")
 snapPath := filepath.Join(snapDir, "vault.db")

 // Open a working database and insert test data.
 database, err := Open(workPath)
 if err != nil {
  t.Fatal(err)
 }

 // Create a storage destination as test data.
 sd := StorageDestination{Name: "snap-test", Type: "local", Config: `{"path":"/tmp"}`}
 id, err := database.CreateStorageDestination(sd)
 if err != nil {
  t.Fatal(err)
 }

 // Create snapshot manager and save a snapshot.
 sm := NewSnapshotManager(database, snapPath)
 if err := sm.SaveSnapshot(); err != nil {
  t.Fatal("SaveSnapshot failed:", err)
 }

 // Verify snapshot file exists.
 if _, err := os.Stat(snapPath); err != nil {
  t.Fatal("snapshot file not created:", err)
 }

 // Close original DB.
 database.Close()

 // Open a fresh working DB (simulating reboot — tmpfs lost).
 workPath2 := filepath.Join(t.TempDir(), "vault.db")
 database2, err := Open(workPath2)
 if err != nil {
  t.Fatal(err)
 }
 defer database2.Close()

 // Restore from snapshot into the fresh DB.
 sm2 := NewSnapshotManager(database2, snapPath)
 if err := sm2.RestoreFromSnapshot(); err != nil {
  t.Fatal("RestoreFromSnapshot failed:", err)
 }

 // Close and reopen to pick up restored data (backup API writes to file, need fresh conn).
 database2.Close()
 database2, err = Open(workPath2)
 if err != nil {
  t.Fatal(err)
 }

 // Verify data survived the roundtrip.
 got, err := database2.GetStorageDestination(id)
 if err != nil {
  t.Fatal("data lost after restore:", err)
 }
 if got.Name != "snap-test" {
  t.Errorf("expected name 'snap-test', got %q", got.Name)
 }
}

func TestSnapshotManagerNoSnapshotFile(t *testing.T) {
 workPath := filepath.Join(t.TempDir(), "vault.db")
 database, err := Open(workPath)
 if err != nil {
  t.Fatal(err)
 }
 defer database.Close()

 sm := NewSnapshotManager(database, "/nonexistent/path/vault.db")
 err = sm.RestoreFromSnapshot()
 if err != nil {
  t.Fatal("RestoreFromSnapshot should be no-op when snapshot doesn't exist, got:", err)
 }
}

func TestSnapshotManagerLastSnapshot(t *testing.T) {
 workPath := filepath.Join(t.TempDir(), "vault.db")
 database, err := Open(workPath)
 if err != nil {
  t.Fatal(err)
 }
 defer database.Close()

 sm := NewSnapshotManager(database, filepath.Join(t.TempDir(), "vault.db"))
 if !sm.LastSnapshot().IsZero() {
  t.Error("LastSnapshot should be zero before first save")
 }

 if err := sm.SaveSnapshot(); err != nil {
  t.Fatal(err)
 }

 if sm.LastSnapshot().IsZero() {
  t.Error("LastSnapshot should be non-zero after save")
 }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/db/... -run TestSnapshot -v`
Expected: FAIL — `NewSnapshotManager` undefined

**Step 3: Write the implementation**

Create `internal/db/snapshot.go`:

```go
package db

import (
 "context"
 "fmt"
 "log"
 "os"
 "path/filepath"
 "sync"
 "time"

 "modernc.org/sqlite"
)

// SnapshotManager handles periodic snapshots of the in-memory (tmpfs) database
// to a persistent location on the cache drive using SQLite's online backup API.
type SnapshotManager struct {
 db           *DB
 snapshotPath string
 lastSnapshot time.Time
 mu           sync.Mutex
}

// NewSnapshotManager creates a SnapshotManager that saves snapshots to snapshotPath.
func NewSnapshotManager(database *DB, snapshotPath string) *SnapshotManager {
 return &SnapshotManager{
  db:           database,
  snapshotPath: snapshotPath,
 }
}

// SnapshotPath returns the persistent snapshot location.
func (sm *SnapshotManager) SnapshotPath() string {
 return sm.snapshotPath
}

// LastSnapshot returns the time of the last successful snapshot.
func (sm *SnapshotManager) LastSnapshot() time.Time {
 sm.mu.Lock()
 defer sm.mu.Unlock()
 return sm.lastSnapshot
}

// SaveSnapshot copies the working database to the snapshot location using
// SQLite's backup API. Reads can continue during the snapshot.
func (sm *SnapshotManager) SaveSnapshot() error {
 sm.mu.Lock()
 defer sm.mu.Unlock()

 if err := os.MkdirAll(filepath.Dir(sm.snapshotPath), 0o755); err != nil {
  return fmt.Errorf("creating snapshot directory: %w", err)
 }

 conn, err := sm.db.DB.Conn(context.Background())
 if err != nil {
  return fmt.Errorf("acquiring connection: %w", err)
 }
 defer conn.Close()

 err = conn.Raw(func(driverConn any) error {
  type backuper interface {
   NewBackup(string) (*sqlite.Backup, error)
  }
  bck, err := driverConn.(backuper).NewBackup(sm.snapshotPath)
  if err != nil {
   return fmt.Errorf("starting backup: %w", err)
  }
  for more := true; more; {
   more, err = bck.Step(-1)
   if err != nil {
    return fmt.Errorf("backup step: %w", err)
   }
  }
  return bck.Finish()
 })
 if err != nil {
  return err
 }

 sm.lastSnapshot = time.Now()
 log.Printf("snapshot: saved to %s", sm.snapshotPath)
 return nil
}

// RestoreFromSnapshot copies the snapshot database into the working database
// using SQLite's backup API. This is called at startup to populate tmpfs.
// If the snapshot file does not exist, this is a no-op (first run).
func (sm *SnapshotManager) RestoreFromSnapshot() error {
 sm.mu.Lock()
 defer sm.mu.Unlock()

 if _, err := os.Stat(sm.snapshotPath); os.IsNotExist(err) {
  log.Printf("snapshot: no snapshot at %s, starting fresh", sm.snapshotPath)
  return nil
 }

 conn, err := sm.db.DB.Conn(context.Background())
 if err != nil {
  return fmt.Errorf("acquiring connection: %w", err)
 }
 defer conn.Close()

 err = conn.Raw(func(driverConn any) error {
  type restorer interface {
   NewRestore(string) (*sqlite.Backup, error)
  }
  bck, err := driverConn.(restorer).NewRestore(sm.snapshotPath)
  if err != nil {
   return fmt.Errorf("starting restore: %w", err)
  }
  for more := true; more; {
   more, err = bck.Step(-1)
   if err != nil {
    return fmt.Errorf("restore step: %w", err)
   }
  }
  return bck.Finish()
 })
 if err != nil {
  return err
 }

 sm.lastSnapshot = time.Now()
 log.Printf("snapshot: restored from %s", sm.snapshotPath)
 return nil
}

// Close performs a final snapshot save.
func (sm *SnapshotManager) Close() error {
 log.Println("snapshot: performing final save before shutdown...")
 return sm.SaveSnapshot()
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/db/... -run TestSnapshot -v`
Expected: 3 PASS

**Step 5: Commit**

```bash
git add internal/db/snapshot.go internal/db/snapshot_test.go
git commit -m "feat: add SnapshotManager with SQLite backup API"
```

---

### Task 2: Daemon Startup Sequence — Hybrid Mode Detection & tmpfs Setup

**Files:**

- Modify: `internal/cli/daemon.go`

This task modifies the daemon command to:

1. Detect if cache drive exists (`/mnt/cache`)
2. If yes: create tmpfs working dir, set snapshot path, restore from snapshot, open DB on tmpfs
3. If no: fall back to `--db` flag path (legacy USB)
4. Handle first-run migration from USB → cache drive

**Step 1: Write the failing test**

No unit test for this task — it's CLI wiring. Verified via `make redeploy` in Task 6.

**Step 2: Modify daemon.go**

Replace the `RunE` function body in `internal/cli/daemon.go`. The key changes are:

1. After parsing flags, add hybrid mode detection:

```go
// --- Hybrid RAM database mode ---
// Detect if cache drive exists for hybrid tmpfs + snapshot mode.
var snapshotMgr *db.SnapshotManager
actualDBPath := dbPath

cacheDir := "/mnt/cache"
if fi, err := os.Stat(cacheDir); err == nil && fi.IsDir() {
    // Cache drive available — use hybrid mode.
    workingDir := "/var/local/vault"
    if err := os.MkdirAll(workingDir, 0o755); err != nil {
        return fmt.Errorf("creating tmpfs working directory: %w", err)
    }

    workingPath := filepath.Join(workingDir, "vault.db")
    snapshotPath := filepath.Join(cacheDir, ".vault", "vault.db")

    // First-run migration: if USB DB exists and snapshot doesn't, migrate.
    if _, err := os.Stat(snapshotPath); os.IsNotExist(err) {
        if _, err := os.Stat(dbPath); err == nil {
            log.Printf("Migrating database from USB (%s) to cache drive (%s)...", dbPath, snapshotPath)
            migDB, err := db.Open(dbPath)
            if err == nil {
                migSM := db.NewSnapshotManager(migDB, snapshotPath)
                if err := migSM.SaveSnapshot(); err != nil {
                    log.Printf("Warning: migration failed: %v (falling back to USB)", err)
                } else {
                    migrated := dbPath + ".migrated"
                    if err := os.Rename(dbPath, migrated); err != nil {
                        log.Printf("Warning: could not rename USB DB: %v", err)
                    } else {
                        log.Printf("USB database migrated. Original saved as %s", migrated)
                    }
                }
                migDB.Close()
            }
        }
    }

    actualDBPath = workingPath
    log.Printf("Hybrid mode: working DB at %s, snapshots at %s", workingPath, snapshotPath)
} else {
    log.Printf("No cache drive detected — using database at %s (USB wear warning)", dbPath)
}
```

1. After `database, err := db.Open(actualDBPath)`, if hybrid mode, create SnapshotManager and restore:

```go
database, err := db.Open(actualDBPath)
if err != nil {
    return err
}
defer database.Close()

// If hybrid mode, restore from snapshot and set up manager.
if actualDBPath != dbPath {
    snapshotPath := filepath.Join(cacheDir, ".vault", "vault.db")
    snapshotMgr = db.NewSnapshotManager(database, snapshotPath)
    if err := snapshotMgr.RestoreFromSnapshot(); err != nil {
        log.Printf("Warning: snapshot restore failed: %v", err)
    }
    // Re-run migrations on restored data (in case schema was updated).
    database.Close()
    database, err = db.Open(actualDBPath)
    if err != nil {
        return err
    }
}
```

1. After creating the server (`srv := api.NewServer(...)`), pass the snapshot manager to the runner:

```go
if snapshotMgr != nil {
    srv.Runner().SetSnapshotManager(snapshotMgr)
}
```

1. Before `return nil` in the graceful shutdown path, add:

```go
if snapshotMgr != nil {
    if err := snapshotMgr.Close(); err != nil {
        log.Printf("Warning: final snapshot save failed: %v", err)
    }
}
```

**Step 3: Commit**

```bash
git add internal/cli/daemon.go
git commit -m "feat: add hybrid RAM database mode to daemon startup"
```

---

### Task 3: Runner Integration — SaveSnapshot After Job Completion

**Files:**

- Modify: `internal/runner/runner.go`

Add a `snapshotManager` field to `Runner` with a setter, and call `SaveSnapshot()` after restore point creation in `RunJob()`.

**Step 1: Add field and setter to Runner struct**

In `internal/runner/runner.go`, add to the `Runner` struct (line ~30):

```go
type Runner struct {
 db              *db.DB
 hub             *ws.Hub
 serverKey       []byte
 snapshotManager *db.SnapshotManager
 mu              sync.Mutex
}
```

Add setter method after `New()` (line ~44):

```go
// SetSnapshotManager sets the snapshot manager for post-job persistence.
func (r *Runner) SetSnapshotManager(sm *db.SnapshotManager) {
 r.snapshotManager = sm
}
```

**Step 2: Call SaveSnapshot after job completion**

In `RunJob()`, after the restore point creation block (after line ~435 `r.backupDatabase(dest, basePath)`), add:

```go
// Persist database to cache drive after successful backup.
if r.snapshotManager != nil {
    if err := r.snapshotManager.SaveSnapshot(); err != nil {
        log.Printf("runner: snapshot save error: %v", err)
    }
}
```

**Step 3: Commit**

```bash
git add internal/runner/runner.go
git commit -m "feat: save database snapshot after backup job completion"
```

---

### Task 4: Settings API Endpoint — Database Info

**Files:**

- Modify: `internal/api/handlers/settings.go`
- Modify: `internal/api/routes.go`
- Modify: `internal/api/server.go`

Add a `GET /api/v1/settings/database` endpoint that returns the database mode, paths, last snapshot time, and snapshot size.

**Step 1: Add SnapshotManager accessor to SettingsHandler**

In `internal/api/handlers/settings.go`, add a field and setter:

```go
type SettingsHandler struct {
 db              *db.DB
 serverKey       []byte
 onKeyChange     func()
 snapshotManager interface {
  SnapshotPath() string
  LastSnapshot() time.Time
 }
}

// SetSnapshotManager sets the snapshot manager for database info reporting.
func (h *SettingsHandler) SetSnapshotManager(sm interface {
 SnapshotPath() string
 LastSnapshot() time.Time
}) {
 h.snapshotManager = sm
}
```

**Step 2: Add GetDatabaseInfo handler**

Add at the end of `internal/api/handlers/settings.go`:

```go
// GetDatabaseInfo returns information about the database mode and location.
//
// GET /api/v1/settings/database
func (h *SettingsHandler) GetDatabaseInfo(w http.ResponseWriter, _ *http.Request) {
 info := map[string]any{
  "mode":         "legacy_usb",
  "working_path": h.db.Path(),
 }

 if h.snapshotManager != nil {
  snapPath := h.snapshotManager.SnapshotPath()
  info["mode"] = "hybrid"
  info["snapshot_path"] = snapPath
  info["last_snapshot"] = h.snapshotManager.LastSnapshot()

  if fi, err := os.Stat(snapPath); err == nil {
   info["snapshot_size_bytes"] = fi.Size()
  }
 }

 respondJSON(w, http.StatusOK, info)
}
```

**Step 3: Register the route**

In `internal/api/routes.go`, inside the `/settings` route block (around line 95), add:

```go
r.Get("/database", settingsH.GetDatabaseInfo)
```

**Step 4: Wire snapshot manager to settings handler in server.go**

In `internal/api/server.go`, add a method:

```go
// SetSnapshotManager passes the snapshot manager to handlers that need it.
func (s *Server) SetSnapshotManager(sm *db.SnapshotManager) {
 // Store for handlers that need it.
 s.snapshotManager = sm
}
```

Add `snapshotManager *db.SnapshotManager` field to the `Server` struct (but since the handler is created in `setupRoutes()` before we have the snapshot manager, we need a different approach).

**Alternative approach:** Since `settingsH` is created inside `setupRoutes()`, add a `SetSnapshotManager` on `Server` that stores it, and have the handler fetch it lazily. The simplest approach: store the snapshot manager on the Server struct, and pass it to `settingsH` via a setter after route setup.

Actually, the cleanest approach: modify `setupRoutes()` to store `settingsH` as a field on Server, then call `SetSnapshotManager` from daemon.go.

In `server.go`, add field:

```go
type Server struct {
 // ... existing fields ...
 settingsHandler *handlers.SettingsHandler
}
```

In `setupRoutes()`, store the handler:

```go
settingsH := handlers.NewSettingsHandler(s.db, s.config.ServerKey)
settingsH.SetOnKeyChange(s.InvalidateKeyCache)
s.settingsHandler = settingsH
```

Add accessor:

```go
func (s *Server) SettingsHandler() *handlers.SettingsHandler {
 return s.settingsHandler
}
```

In `daemon.go`, after setting up the snapshot manager on the runner:

```go
if snapshotMgr != nil {
    srv.Runner().SetSnapshotManager(snapshotMgr)
    srv.SettingsHandler().SetSnapshotManager(snapshotMgr)
}
```

**Step 5: Commit**

```bash
git add internal/api/handlers/settings.go internal/api/routes.go internal/api/server.go internal/cli/daemon.go
git commit -m "feat: add GET /api/v1/settings/database endpoint"
```

---

### Task 5: Settings UI — Database Info Card

**Files:**

- Modify: `web/src/pages/Settings.svelte`
- Modify: `web/src/lib/api.js`

Add a read-only info card to the Settings → General tab showing database mode, paths, and last snapshot time.

**Step 1: Add API method**

In `web/src/lib/api.js`, add to the `api` object:

```js
getDatabaseInfo: () => request('GET', '/settings/database'),
```

**Step 2: Add state and load data**

In `web/src/pages/Settings.svelte`, add state variable:

```js
let databaseInfo = $state(null);
```

In the `onMount` Promise.all, add:

```js
api.getDatabaseInfo().catch(() => null);
```

And destructure:

```js
const [h, s, enc, keyStatus, staging, dbInfo] = await Promise.all([...])
databaseInfo = dbInfo
```

**Step 3: Add the info card to the General tab**

Place it after the Staging Directory card and before the About card. The card shows:

- Mode label (Hybrid / Direct SSD / Legacy USB)
- Working path
- Snapshot path (if hybrid)
- Last snapshot time (if hybrid)
- Snapshot size (if hybrid)

```svelte
{#if databaseInfo}
<div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
  <div class="px-5 py-4 border-b border-border">
    <h2 class="text-base font-semibold text-text">Database Location</h2>
    <p class="text-xs text-text-muted mt-0.5">Where Vault stores its database and how it protects the USB flash drive.</p>
  </div>
  <div class="divide-y divide-border">
    <div class="px-5 py-3 flex items-center justify-between">
      <span class="text-sm text-text-muted">Mode</span>
      <span class="text-sm font-medium text-text">
        {#if databaseInfo.mode === 'hybrid'}
          Hybrid (RAM + SSD snapshots)
        {:else if databaseInfo.mode === 'direct'}
          Direct SSD
        {:else}
          Legacy USB
        {/if}
      </span>
    </div>
    <div class="px-5 py-3 flex items-center justify-between">
      <span class="text-sm text-text-muted">Working</span>
      <span class="text-sm font-mono text-text">{databaseInfo.working_path}</span>
    </div>
    {#if databaseInfo.snapshot_path}
    <div class="px-5 py-3 flex items-center justify-between">
      <span class="text-sm text-text-muted">Snapshot</span>
      <span class="text-sm font-mono text-text">{databaseInfo.snapshot_path}</span>
    </div>
    {/if}
    {#if databaseInfo.last_snapshot}
    <div class="px-5 py-3 flex items-center justify-between">
      <span class="text-sm text-text-muted">Last snapshot</span>
      <span class="text-sm text-text">{new Date(databaseInfo.last_snapshot).toLocaleString()}</span>
    </div>
    {/if}
    {#if databaseInfo.snapshot_size_bytes}
    <div class="px-5 py-3 flex items-center justify-between">
      <span class="text-sm text-text-muted">Snapshot size</span>
      <span class="text-sm text-text">{formatBytes(databaseInfo.snapshot_size_bytes)}</span>
    </div>
    {/if}
  </div>
  {#if databaseInfo.mode === 'legacy_usb'}
  <div class="px-5 py-3 bg-amber-500/10 border-t border-amber-500/20">
    <p class="text-xs text-amber-400">No cache drive detected. Database writes go directly to the USB flash drive, which may reduce its lifespan.</p>
  </div>
  {/if}
</div>
{/if}
```

**Step 4: Commit**

```bash
git add web/src/pages/Settings.svelte web/src/lib/api.js
git commit -m "feat: add Database Location info card to Settings"
```

---

### Task 6: Build, Deploy, and Verify

**Files:** None (pipeline task)

**Step 1: Lint and test locally**

Run: `make lint`
Expected: PASS

Run: `make test`
Expected: All tests pass, including new snapshot tests

**Step 2: Build and deploy**

Run: `make redeploy`
Expected: Build succeeds, deploys to Unraid, verification tests pass

**Step 3: Verify UI with Playwright**

Navigate to `http://192.168.20.21:24085` and verify:

1. **Settings → General tab:** Database Location card is visible, shows mode and paths
2. **All pages load correctly:** Dashboard, Jobs, Storage, History, Restore, Logs, Replication, Recovery, Settings

Take snapshots of the Settings page to confirm the Database Location card renders correctly.

**Step 4: Commit any fixes**

If linter or test failures occur, fix and re-run `make redeploy`.

---

## Notes

- **macOS development:** Hybrid mode auto-detects. Since `/mnt/cache` doesn't exist on macOS, the daemon uses the `--db` flag path directly (legacy mode). No changes needed for dev workflow.
- **modernc.org/sqlite v1.46.1:** Already in `go.mod`. The `NewBackup(destPath)` and `NewRestore(srcPath)` methods are available on the driver connection via `conn.Raw()`.
- **Data loss window:** At most one backup job's worth of data (restore point, activity logs, progress updates). The job re-runs on next schedule, and `CleanupStaleRuns()` marks interrupted runs as failed.
- **No periodic flush timer:** Snapshots are event-driven only (job completion, settings changes, shutdown). This keeps the code simple and the cache drive write count low.
