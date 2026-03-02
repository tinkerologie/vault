# Backup Throughput Display & Staging Directory Configuration — Design

**Goal:** Add visible backup speed metrics throughout the UI and let users configure where staging files are stored.

**Architecture:** Both features are additive — throughput piggybacks on existing JobRun data with a computed field, staging config uses the existing settings key-value store. No schema migrations required.

**Tech Stack:** Go backend (Chi router, SQLite, syscall.Statfs), Svelte 5 frontend, WebSocket for live speed.

---

## Feature 1: Backup Throughput Display

### Problem

Users have no visibility into how fast their backups run. The data exists (size_bytes, started_at, completed_at) but no speed metric is calculated or shown. During active backups, users see progress counts but not transfer speed.

### Design

#### 1.1 Backend: `duration_seconds` in JobRun Response

Add a computed `duration_seconds` field to the JobRun JSON response. This avoids timezone parsing issues on the frontend.

**File:** `internal/db/models.go`

```go
type JobRun struct {
    // ... existing fields ...
    DurationSeconds *int `json:"duration_seconds"`
}
```

**File:** `internal/db/job_repo.go` — `GetJobRuns` query adds:

```sql
CASE WHEN completed_at IS NOT NULL
  THEN CAST((julianday(completed_at) - julianday(started_at)) * 86400 AS INTEGER)
  ELSE NULL END AS duration_seconds
```

Scan the new column in the row scan. No new endpoints needed.

#### 1.2 Frontend: `formatSpeed()` Utility

**File:** `web/src/lib/utils.js`

```js
export function formatSpeed(bytes, seconds) {
  if (!bytes || !seconds || seconds === 0) return null;
  const bps = bytes / seconds;
  const k = 1024;
  const units = ["B/s", "KB/s", "MB/s", "GB/s"];
  const i = Math.min(Math.floor(Math.log(bps) / Math.log(k)), units.length - 1);
  return parseFloat((bps / Math.pow(k, i)).toFixed(1)) + " " + units[i];
}
```

#### 1.3 Dashboard: Average Throughput Stat

Below the health gauge ("59% — 4 recent failures"), add a secondary line:

```
Avg. speed: 31.2 MB/s
```

Calculated from: `sum(size_bytes) / sum(duration_seconds)` for recent completed runs. Only shown when at least one completed run has both values.

**File:** `web/src/pages/Dashboard.svelte` — new `$derived` value:

```js
const avgSpeed = $derived.by(() => {
  const completed = recentRuns.filter(
    (r) => r.status === "completed" && r.size_bytes && r.duration_seconds,
  );
  if (!completed.length) return null;
  const totalBytes = completed.reduce((s, r) => s + r.size_bytes, 0);
  const totalSecs = completed.reduce((s, r) => s + r.duration_seconds, 0);
  return formatSpeed(totalBytes, totalSecs);
});
```

#### 1.4 Throughput Chip on Run Entries

Each completed run entry in the Dashboard activity timeline and History page adds a speed chip after the size:

```
9m 33s  ·  18 GB  ·  32.2 MB/s  ·  15/15 items
```

Uses: `formatSpeed(run.size_bytes, run.duration_seconds)`. Only shown when non-null.

#### 1.5 Live Throughput During Active Backups

When a backup is running, the WebSocket broadcasts `item_backup_done` events with `size_bytes` per item. The frontend:

1. Tracks cumulative bytes from `item_backup_done` events for the active run
2. Records the timestamp of the first `item_backup_done` event
3. Calculates `cumulativeBytes / elapsedSeconds` on each event
4. Displays on the Dashboard: **"Running — 245 MB/s"**
5. Resets when `job_run_completed` fires

No backend changes needed — uses existing WebSocket data.

**File:** `web/src/pages/Dashboard.svelte` — new state for live tracking:

```js
let liveSpeed = $state(null);
let liveCumulativeBytes = $state(0);
let liveStartTime = $state(null);
```

WebSocket handler for `item_backup_done`:

```js
liveCumulativeBytes += msg.size_bytes;
if (!liveStartTime) liveStartTime = Date.now();
const elapsed = (Date.now() - liveStartTime) / 1000;
liveSpeed = formatSpeed(liveCumulativeBytes, elapsed);
```

WebSocket handler for `job_run_completed`:

```js
liveSpeed = null;
liveCumulativeBytes = 0;
liveStartTime = null;
```

---

## Feature 2: Staging Directory Configuration

### Problem

The staging directory (`/mnt/cache/.vault-stage`) uses a hardcoded cascade strategy. Users with non-standard setups (no cache drive, dedicated NVMe, space constraints) cannot control where temporary backup files are staged. They also have no visibility into which path is being used or how much space is available.

### Design

#### 2.1 Backend: Staging Info Endpoint

**New endpoint:** `GET /api/v1/settings/staging`

Response:

```json
{
  "resolved_path": "/mnt/cache/.vault-stage",
  "source": "cache",
  "override": "",
  "disk_free_bytes": 107374182400,
  "disk_total_bytes": 214748364800,
  "cascade": [
    { "path": "/mnt/cache/.vault-stage", "available": true, "source": "cache" },
    {
      "path": "/mnt/user/backups/.vault-stage",
      "available": true,
      "source": "local-storage"
    },
    { "path": "/tmp", "available": true, "source": "system" }
  ]
}
```

**Fields:**

- `resolved_path`: The path that would actually be used right now
- `source`: Which cascade level resolved (`cache`, `local-storage`, `system`, `override`)
- `override`: The user's custom path (empty string = auto cascade)
- `disk_free_bytes` / `disk_total_bytes`: Via `syscall.Statfs` on the resolved path
- `cascade`: All cascade levels with availability status

**File:** `internal/api/handlers/settings.go` — new `GetStagingInfo` method
**File:** `internal/tempdir/tempdir.go` — new `ResolveInfo()` function that returns staging path info without creating directories

#### 2.2 Backend: Staging Override Endpoint

**New endpoint:** `PUT /api/v1/settings/staging`

Request:

```json
{ "override": "/mnt/nvme/.vault-stage" }
```

Validation:

1. Path must be absolute
2. Parent directory must exist and be writable
3. Returns updated staging info (including disk space for the new path)

Stores as setting key `staging_dir_override` in the settings table.

To clear: send `{"override": ""}`.

**File:** `internal/api/handlers/settings.go` — new `SetStagingOverride` method
**File:** `internal/db/settings_repo.go` — uses existing `SetSetting()`

#### 2.3 Backend: Tempdir Override Integration

Modify the cascade in `tempdir.CreateBackupDir()` / `CreateRestoreDir()`:

```go
func createDir(dest StorageConfig, pattern string, override string) (string, func(), error) {
    // 0. Check override first
    if override != "" {
        stageBase := filepath.Join(override, StageDirName)
        if dir, err := os.MkdirTemp(stageBase, pattern); err == nil {
            return dir, cleanupFunc(dir, stageBase), nil
        }
        // Fall through to cascade if override fails
    }
    // 1. Cache paths...
    // 2. Local storage...
    // 3. System temp...
}
```

The runner reads the override from DB settings before each backup/restore and passes it to `CreateBackupDir()`.

**File:** `internal/tempdir/tempdir.go` — add override parameter
**File:** `internal/runner/runner.go` — read `staging_dir_override` setting before creating temp dirs

#### 2.4 Frontend: Settings > General Tab

Add a "Staging Directory" card between "Appearance" and "Server Information".

```
┌─ Staging Directory ──────────────────────────────────┐
│                                                       │
│  Current path: /mnt/cache/.vault-stage               │
│  Source: SSD Cache (automatic)                        │
│                                                       │
│  ████████████░░░░░░░░  50.2 GB free of 100 GB        │
│                                                       │
│  ┌─ Custom Path (optional) ────────────────────────┐ │
│  │                                           [Set] │ │
│  └─────────────────────────────────────────────────┘ │
│                                                       │
│  ▸ Cascade order                                      │
│    1. /mnt/cache (SSD cache) ✓                       │
│    2. /mnt/user/backups (local storage) ✓            │
│    3. System temp ✓                                   │
└───────────────────────────────────────────────────────┘
```

**Elements:**

- **Current path** (read-only): Resolved staging path
- **Source label**: "SSD Cache (automatic)" / "Custom override" / "Local storage fallback" / "System temp fallback"
- **Disk space bar**: Visual progress bar with `formatBytes(free)` of `formatBytes(total)`. Color coding: green (>30% free), yellow (10-30%), red (<10%)
- **Custom path input**: Text input with "Set" button. Empty = auto cascade
- **Reset to Auto**: Only shown when override is active. Clears the override
- **Cascade order** (collapsed by default, expandable): Shows all three levels with checkmarks for availability

**File:** `web/src/pages/Settings.svelte` — new section in General tab
**File:** `web/src/lib/api.js` — add `getStagingInfo()` and `setStagingOverride()`

#### 2.5 Disk Space Bar Component

Reusable progress bar component:

```svelte
<div class="h-2 rounded-full bg-surface overflow-hidden">
  <div class="h-full rounded-full {pct > 70 ? 'bg-danger' : pct > 30 ? 'bg-warning' : 'bg-success'}"
       style="width: {usedPct}%"></div>
</div>
<p class="text-xs text-text-muted mt-1">
  {formatBytes(free)} free of {formatBytes(total)}
</p>
```

Where `usedPct = ((total - free) / total) * 100`.

---

## Files Changed Summary

### Feature 1 (Throughput)

| File                             | Change                                                           |
| -------------------------------- | ---------------------------------------------------------------- |
| `internal/db/models.go`          | Add `DurationSeconds` field to `JobRun`                          |
| `internal/db/job_repo.go`        | Add computed column to `GetJobRuns` query                        |
| `web/src/lib/utils.js`           | Add `formatSpeed()` function                                     |
| `web/src/pages/Dashboard.svelte` | Add avg speed stat, speed chips on timeline, live speed tracking |
| `web/src/pages/History.svelte`   | Add speed chips on run entries                                   |

### Feature 2 (Staging)

| File                                | Change                                                   |
| ----------------------------------- | -------------------------------------------------------- |
| `internal/api/handlers/settings.go` | Add `GetStagingInfo`, `SetStagingOverride` handlers      |
| `internal/api/routes.go`            | Add `/settings/staging` routes                           |
| `internal/tempdir/tempdir.go`       | Add `ResolveInfo()`, override parameter to `createDir()` |
| `internal/runner/runner.go`         | Read staging override setting before creating temp dirs  |
| `web/src/lib/api.js`                | Add `getStagingInfo()`, `setStagingOverride()`           |
| `web/src/pages/Settings.svelte`     | Add staging directory section to General tab             |
