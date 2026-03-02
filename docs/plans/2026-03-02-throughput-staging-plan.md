# Throughput Display & Staging Directory — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:executing-plans to implement this plan task-by-task.

**Goal:** Add backup speed metrics throughout the UI (average, per-run, live) and let users configure the staging directory from Settings.

**Architecture:** Throughput piggybacks on existing JobRun data with a computed SQL column. Staging config uses the settings key-value store and extends tempdir's cascade with an override. No schema migrations needed.

**Tech Stack:** Go 1.26 (Chi router, SQLite/modernc, syscall.Statfs), Svelte 5 ($state/$derived runes), WebSocket

---

## Task 1: Add `DurationSeconds` computed field to JobRun

**Files:**

- Modify: `internal/db/models.go:38-51`
- Modify: `internal/db/job_repo.go:185-206`
- Test: `internal/db/job_repo_test.go`

**Step 1: Write the failing test**

In `internal/db/job_repo_test.go`, add a test that creates a completed job run and asserts `DurationSeconds` is non-nil and positive.

```go
func TestGetJobRunsDurationSeconds(t *testing.T) {
 database := setupTestDB(t)
 jobID := createTestJob(t, database)

 // Create a run with known start/complete times.
 runID, err := database.CreateJobRun(db.JobRun{
  JobID: jobID, Status: "running", BackupType: "full", RunType: "backup",
 })
 if err != nil {
  t.Fatal(err)
 }
 // Simulate a 10-second backup.
 _, err = database.Exec(
  `UPDATE job_runs SET status='completed', started_at=datetime('now','-10 seconds'), completed_at=datetime('now'), size_bytes=10485760 WHERE id=?`,
  runID,
 )
 if err != nil {
  t.Fatal(err)
 }

 runs, err := database.GetJobRuns(jobID, 10)
 if err != nil {
  t.Fatal(err)
 }
 if len(runs) == 0 {
  t.Fatal("expected at least one run")
 }
 run := runs[0]
 if run.DurationSeconds == nil {
  t.Fatal("DurationSeconds should be non-nil for completed run")
 }
 if *run.DurationSeconds < 9 || *run.DurationSeconds > 12 {
  t.Errorf("DurationSeconds = %d, expected ~10", *run.DurationSeconds)
 }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/db/... -run TestGetJobRunsDurationSeconds -v`
Expected: FAIL — `DurationSeconds` field does not exist.

**Step 3: Add `DurationSeconds` field to `JobRun` struct**

In `internal/db/models.go`, add after `SizeBytes` (line 50):

```go
type JobRun struct {
 ID              int64      `json:"id"`
 JobID           int64      `json:"job_id"`
 Status          string     `json:"status"`
 BackupType      string     `json:"backup_type"`
 RunType         string     `json:"run_type"`
 StartedAt       time.Time  `json:"started_at"`
 CompletedAt     *time.Time `json:"completed_at"`
 Log             string     `json:"log"`
 ItemsTotal      int        `json:"items_total"`
 ItemsDone       int        `json:"items_done"`
 ItemsFailed     int        `json:"items_failed"`
 SizeBytes       int64      `json:"size_bytes"`
 DurationSeconds *int       `json:"duration_seconds"`
}
```

**Step 4: Update `GetJobRuns` query to compute duration**

In `internal/db/job_repo.go`, update the SQL query and scan (lines 185-206):

```go
func (d *DB) GetJobRuns(jobID int64, limit int) ([]JobRun, error) {
 rows, err := d.Query(
  `SELECT id, job_id, status, backup_type, COALESCE(run_type, 'backup'), started_at, completed_at, log,
  items_total, items_done, items_failed, size_bytes,
  CASE WHEN completed_at IS NOT NULL
   THEN CAST((julianday(completed_at) - julianday(started_at)) * 86400 AS INTEGER)
   ELSE NULL END
  FROM job_runs WHERE job_id = ? ORDER BY started_at DESC LIMIT ?`, jobID, limit,
 )
 if err != nil {
  return nil, err
 }
 defer rows.Close()
 var runs []JobRun
 for rows.Next() {
  var run JobRun
  if err := rows.Scan(&run.ID, &run.JobID, &run.Status, &run.BackupType,
   &run.RunType, &run.StartedAt, &run.CompletedAt, &run.Log, &run.ItemsTotal,
   &run.ItemsDone, &run.ItemsFailed, &run.SizeBytes, &run.DurationSeconds); err != nil {
   return nil, err
  }
  runs = append(runs, run)
 }
 return runs, rows.Err()
}
```

**Step 5: Run test to verify it passes**

Run: `go test ./internal/db/... -run TestGetJobRunsDurationSeconds -v`
Expected: PASS

**Step 6: Run full test suite**

Run: `go test ./... -short -v`
Expected: All existing tests still pass.

**Step 7: Commit**

```bash
git add internal/db/models.go internal/db/job_repo.go internal/db/job_repo_test.go
git commit -m "feat: add computed duration_seconds to JobRun API response"
```

---

## Task 2: Add `formatSpeed()` utility function

**Files:**

- Modify: `web/src/lib/utils.js:131` (append)

**Step 1: Add `formatSpeed` to utils.js**

Append to `web/src/lib/utils.js` after line 131:

```js
/** Format bytes/seconds into human-readable speed (e.g. "31.2 MB/s") */
export function formatSpeed(bytes, seconds) {
  if (!bytes || !seconds || seconds === 0) return null;
  const bps = bytes / seconds;
  const k = 1024;
  const units = ["B/s", "KB/s", "MB/s", "GB/s"];
  const i = Math.min(Math.floor(Math.log(bps) / Math.log(k)), units.length - 1);
  return parseFloat((bps / Math.pow(k, i)).toFixed(1)) + " " + units[i];
}
```

**Step 2: Verify the web build succeeds**

Run: `cd web && npm run build`
Expected: Build succeeds with no errors.

**Step 3: Commit**

```bash
git add web/src/lib/utils.js
git commit -m "feat: add formatSpeed utility for throughput display"
```

---

## Task 3: Add throughput chips to Dashboard activity timeline

**Files:**

- Modify: `web/src/pages/Dashboard.svelte`

**Step 1: Add average throughput stat below health gauge**

Import `formatSpeed` at the top of the `<script>` block alongside existing utils imports. Add a `$derived` value:

```js
import {
  formatBytes,
  relTime,
  statusBadge,
  formatSpeed,
} from "../lib/utils.js";

const avgSpeed = $derived.by(() => {
  const completed = recentRuns.filter(
    (r) =>
      (r.status === "completed" || r.status === "success") &&
      r.size_bytes &&
      r.duration_seconds,
  );
  if (!completed.length) return null;
  const totalBytes = completed.reduce((s, r) => s + r.size_bytes, 0);
  const totalSecs = completed.reduce((s, r) => s + r.duration_seconds, 0);
  return formatSpeed(totalBytes, totalSecs);
});
```

In the health gauge section (around line 245), add below the health summary text:

```svelte
{#if avgSpeed}
  <p class="text-xs text-text-muted mt-1">Avg. speed: {avgSpeed}</p>
{/if}
```

**Step 2: Add speed chip to each activity timeline run entry**

Find where each run entry shows duration, size, and item count (the metadata line in the Recent Activity section). Add the speed chip after the size:

```svelte
<span class="text-xs text-text-muted">{duration(run)}</span>
<span class="text-xs text-text-muted">{formatBytes(run.size_bytes)}</span>
{#if run.duration_seconds && run.size_bytes}
  <span class="text-xs text-text-muted">{formatSpeed(run.size_bytes, run.duration_seconds)}</span>
{/if}
<span class="text-xs text-text-muted">{run.items_done}/{run.items_total} items</span>
```

**Step 3: Verify web build succeeds**

Run: `cd web && npm run build`
Expected: Build succeeds.

**Step 4: Commit**

```bash
git add web/src/pages/Dashboard.svelte
git commit -m "feat: show avg throughput and per-run speed on Dashboard"
```

---

## Task 4: Add throughput chips to History page

**Files:**

- Modify: `web/src/pages/History.svelte`

**Step 1: Import `formatSpeed` and add speed chips**

Add `formatSpeed` to the import from utils.js. In each run entry's metadata line (around lines 275-280 where duration, items, and size are shown), add the speed chip:

```svelte
{#if run.duration_seconds && run.size_bytes}
  <span class="text-xs text-text-muted">{formatSpeed(run.size_bytes, run.duration_seconds)}</span>
{/if}
```

**Step 2: Verify web build succeeds**

Run: `cd web && npm run build`
Expected: Build succeeds.

**Step 3: Commit**

```bash
git add web/src/pages/History.svelte
git commit -m "feat: show throughput on History page run entries"
```

---

## Task 5: Add live throughput during active backups

**Files:**

- Modify: `web/src/pages/Dashboard.svelte`

**Step 1: Add live speed tracking state**

Add near the other state declarations (around line 15):

```js
let liveSpeed = $state(null);
let liveCumulativeBytes = $state(0);
let liveStartTime = $state(null);
```

**Step 2: Update WebSocket handler for `item_backup_done`**

In the `onWsMessage` callback (around line 50), add handling for live speed:

```js
onMount(() => {
  loadDashboard();
  const unsub = onWsMessage((msg) => {
    const jobNameResolver = (id) => jobs.find((j) => j.id === id)?.name;
    const handled = handleProgressMessage(msg, jobNameResolver);

    if (msg.type === "item_backup_done") {
      liveCumulativeBytes += msg.size_bytes || 0;
      if (!liveStartTime) liveStartTime = Date.now();
      const elapsed = (Date.now() - liveStartTime) / 1000;
      if (elapsed > 0) liveSpeed = formatSpeed(liveCumulativeBytes, elapsed);
    }

    if (msg.type === "job_run_completed") {
      liveSpeed = null;
      liveCumulativeBytes = 0;
      liveStartTime = null;
      loadDashboard();
    }
  });
  return unsub;
});
```

**Step 3: Display live speed in the running job indicator**

Find where `runningJob` or `progress` is displayed on the Dashboard. Add the live speed:

```svelte
{#if liveSpeed}
  <span class="text-xs text-info font-medium">{liveSpeed}</span>
{/if}
```

**Step 4: Verify web build succeeds**

Run: `cd web && npm run build`
Expected: Build succeeds.

**Step 5: Commit**

```bash
git add web/src/pages/Dashboard.svelte
git commit -m "feat: show live backup throughput during active backups"
```

---

## Task 6: Add `ResolveInfo()` to tempdir package

**Files:**

- Modify: `internal/tempdir/tempdir.go`
- Test: `internal/tempdir/tempdir_test.go`

**Step 1: Write the failing test**

Add to `internal/tempdir/tempdir_test.go`:

```go
func TestResolveInfo(t *testing.T) {
 localPath := t.TempDir()
 dests := []StorageConfig{
  {Type: "local", Config: `{"path":"` + localPath + `"}`},
 }

 info := ResolveInfo(dests, "")
 if info.ResolvedPath == "" {
  t.Fatal("ResolvedPath should not be empty")
 }
 if info.Source == "" {
  t.Fatal("Source should not be empty")
 }
 if info.DiskTotalBytes == 0 {
  t.Fatal("DiskTotalBytes should be non-zero")
 }
 if len(info.Cascade) == 0 {
  t.Fatal("Cascade should not be empty")
 }
}

func TestResolveInfoWithOverride(t *testing.T) {
 overridePath := t.TempDir()
 info := ResolveInfo(nil, overridePath)
 if info.Source != "override" {
  t.Errorf("Source = %q, want %q", info.Source, "override")
 }
 if info.ResolvedPath != overridePath {
  t.Errorf("ResolvedPath = %q, want %q", info.ResolvedPath, overridePath)
 }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/tempdir/... -run TestResolveInfo -v`
Expected: FAIL — `ResolveInfo` does not exist.

**Step 3: Implement `ResolveInfo` and `StagingInfo` struct**

Add to `internal/tempdir/tempdir.go` after the `StorageConfig` struct (around line 34):

```go
// StagingInfo contains information about the resolved staging directory.
type StagingInfo struct {
 ResolvedPath   string        `json:"resolved_path"`
 Source         string        `json:"source"`
 Override       string        `json:"override"`
 DiskFreeBytes  uint64        `json:"disk_free_bytes"`
 DiskTotalBytes uint64        `json:"disk_total_bytes"`
 Cascade        []CascadeItem `json:"cascade"`
}

// CascadeItem represents one level of the staging cascade.
type CascadeItem struct {
 Path      string `json:"path"`
 Available bool   `json:"available"`
 Source    string `json:"source"`
}

// ResolveInfo returns information about which staging directory would be used,
// without actually creating any directories. If override is non-empty and valid,
// it is used directly.
func ResolveInfo(destinations []StorageConfig, override string) StagingInfo {
 info := StagingInfo{Override: override}

 // Build cascade list.
 for _, base := range CachePaths {
  stagePath := filepath.Join(base, StageDirName)
  ci := CascadeItem{Path: stagePath, Source: "cache"}
  if fi, err := os.Stat(base); err == nil && fi.IsDir() {
   ci.Available = true
  }
  info.Cascade = append(info.Cascade, ci)
 }
 for _, dest := range destinations {
  if dest.Type != "local" {
   continue
  }
  var cfg struct {
   Path string `json:"path"`
  }
  if err := json.Unmarshal([]byte(dest.Config), &cfg); err == nil && cfg.Path != "" {
   stagePath := filepath.Join(cfg.Path, StageDirName)
   ci := CascadeItem{Path: stagePath, Source: "local-storage"}
   if fi, err := os.Stat(cfg.Path); err == nil && fi.IsDir() {
    ci.Available = true
   }
   info.Cascade = append(info.Cascade, ci)
  }
 }
 info.Cascade = append(info.Cascade, CascadeItem{Path: os.TempDir(), Available: true, Source: "system"})

 // Resolve which path wins.
 if override != "" {
  if fi, err := os.Stat(override); err == nil && fi.IsDir() {
   info.ResolvedPath = override
   info.Source = "override"
  }
 }
 if info.ResolvedPath == "" {
  for _, ci := range info.Cascade {
   if ci.Available {
    info.ResolvedPath = ci.Path
    info.Source = ci.Source
    break
   }
  }
 }

 // Get disk space.
 if info.ResolvedPath != "" {
  info.DiskFreeBytes, info.DiskTotalBytes = diskSpace(info.ResolvedPath)
 }

 return info
}
```

**Step 4: Add `diskSpace` helper (platform-aware)**

Add to `internal/tempdir/tempdir.go`:

```go
// diskSpace returns free and total bytes for the filesystem containing path.
func diskSpace(path string) (free, total uint64) {
 var stat syscall.Statfs_t
 if err := syscall.Statfs(path, &stat); err != nil {
  return 0, 0
 }
 total = stat.Blocks * uint64(stat.Bsize)
 free = stat.Bavail * uint64(stat.Bsize)
 return free, total
}
```

Add `"syscall"` to the imports.

**Step 5: Run test to verify it passes**

Run: `go test ./internal/tempdir/... -run TestResolveInfo -v`
Expected: PASS

**Step 6: Run full test suite**

Run: `go test ./... -short -v`
Expected: All pass.

**Step 7: Commit**

```bash
git add internal/tempdir/tempdir.go internal/tempdir/tempdir_test.go
git commit -m "feat: add ResolveInfo to tempdir for staging directory introspection"
```

---

## Task 7: Add staging override to tempdir cascade

**Files:**

- Modify: `internal/tempdir/tempdir.go:36-47` (CreateBackupDir, CreateRestoreDir signatures)
- Modify: `internal/tempdir/tempdir.go:49-95` (createDir)
- Modify: `internal/runner/runner.go:425,770` (callers)
- Test: `internal/tempdir/tempdir_test.go`

**Step 1: Write the failing test**

Add to `internal/tempdir/tempdir_test.go`:

```go
func TestCreateBackupDirWithOverride(t *testing.T) {
 overridePath := t.TempDir()
 dest := StorageConfig{Type: "sftp", Config: `{}`}

 dir, cleanup, err := CreateBackupDir(dest, overridePath)
 if err != nil {
  t.Fatalf("CreateBackupDir() error = %v", err)
 }
 defer cleanup()

 // Should be under the override path.
 stageBase := filepath.Join(overridePath, StageDirName)
 rel, err := filepath.Rel(stageBase, dir)
 if err != nil {
  t.Fatalf("dir %s not relative to %s: %v", dir, stageBase, err)
 }
 if filepath.IsAbs(rel) || (len(rel) >= 2 && rel[:2] == "..") {
  t.Errorf("expected dir under %s, got %s", stageBase, dir)
 }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/tempdir/... -run TestCreateBackupDirWithOverride -v`
Expected: FAIL — signature doesn't accept override parameter.

**Step 3: Add override parameter to `CreateBackupDir`, `CreateRestoreDir`, and `createDir`**

Update signatures in `internal/tempdir/tempdir.go`:

```go
// CreateBackupDir creates a temporary staging directory for a backup operation.
// If override is non-empty, it is tried before the cascade.
func CreateBackupDir(dest StorageConfig, override string) (string, func(), error) {
 return createDir(dest, "backup-*", override)
}

// CreateRestoreDir creates a temporary staging directory for a restore operation.
func CreateRestoreDir(dest StorageConfig, override string) (string, func(), error) {
 return createDir(dest, "restore-*", override)
}

func createDir(dest StorageConfig, pattern string, override string) (string, func(), error) {
 // Try override first.
 if override != "" {
  if info, err := os.Stat(override); err == nil && info.IsDir() {
   stageBase := filepath.Join(override, StageDirName)
   if err := os.MkdirAll(stageBase, 0750); err == nil {
    dir, err := os.MkdirTemp(stageBase, pattern)
    if err == nil {
     log.Printf("tempdir: using override staging dir %s", dir)
     return dir, cleanupFunc(dir, stageBase), nil
    }
   }
  }
  log.Printf("tempdir: override path %s unusable, falling back to cascade", override)
 }

 // (rest of existing cascade unchanged)
 // ...
}
```

**Step 4: Update existing tests to pass empty override**

Update all existing `CreateBackupDir` and `CreateRestoreDir` calls in tests to pass `""` as the second argument:

- `TestCreateBackupDir`: `CreateBackupDir(tt.dest, "")`
- `TestCreateRestoreDir`: `CreateRestoreDir(dest, "")`
- `TestCreateBackupDirLocalStaging`: `CreateBackupDir(dest, "")`

**Step 5: Update runner callers**

In `internal/runner/runner.go`, update line 425:

```go
// Read staging override from settings.
stageOverride, _ := r.db.GetSetting("staging_dir_override", "")
tmpDir, cleanup, err := tempdir.CreateBackupDir(tempdir.StorageConfig{Type: dest.Type, Config: dest.Config}, stageOverride)
```

And line 770:

```go
stageOverride, _ := r.db.GetSetting("staging_dir_override", "")
tmpDir, cleanup, err := tempdir.CreateRestoreDir(tempdir.StorageConfig{Type: dest.Type, Config: dest.Config}, stageOverride)
```

**Step 6: Run full test suite**

Run: `go test ./... -short -v`
Expected: All pass.

**Step 7: Commit**

```bash
git add internal/tempdir/tempdir.go internal/tempdir/tempdir_test.go internal/runner/runner.go
git commit -m "feat: add staging directory override to tempdir cascade"
```

---

## Task 8: Add staging API endpoints

**Files:**

- Modify: `internal/api/handlers/settings.go` (add GetStagingInfo, SetStagingOverride)
- Modify: `internal/api/routes.go:83-93` (add routes)
- Test: `internal/api/handlers/settings_test.go`

**Step 1: Write the failing test**

In `internal/api/handlers/settings_test.go`, add:

```go
func TestGetStagingInfo(t *testing.T) {
 database := setupTestDB(t)
 h := handlers.NewSettingsHandler(database, make([]byte, 32))

 req := httptest.NewRequest("GET", "/api/v1/settings/staging", nil)
 w := httptest.NewRecorder()
 h.GetStagingInfo(w, req)

 if w.Code != http.StatusOK {
  t.Fatalf("status = %d, want 200", w.Code)
 }
 var info map[string]any
 json.NewDecoder(w.Body).Decode(&info)
 if info["resolved_path"] == nil || info["resolved_path"] == "" {
  t.Error("resolved_path should be set")
 }
 if info["source"] == nil || info["source"] == "" {
  t.Error("source should be set")
 }
}

func TestSetStagingOverride(t *testing.T) {
 database := setupTestDB(t)
 h := handlers.NewSettingsHandler(database, make([]byte, 32))

 overridePath := t.TempDir()
 body := fmt.Sprintf(`{"override":"%s"}`, overridePath)
 req := httptest.NewRequest("PUT", "/api/v1/settings/staging", strings.NewReader(body))
 w := httptest.NewRecorder()
 h.SetStagingOverride(w, req)

 if w.Code != http.StatusOK {
  t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
 }

 // Verify override was stored.
 val, _ := database.GetSetting("staging_dir_override", "")
 if val != overridePath {
  t.Errorf("stored override = %q, want %q", val, overridePath)
 }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/api/handlers/... -run TestGetStagingInfo -v`
Expected: FAIL — method does not exist.

**Step 3: Implement `GetStagingInfo` and `SetStagingOverride`**

Add to `internal/api/handlers/settings.go`:

```go
// GetStagingInfo returns info about the current staging directory.
//
// GET /api/v1/settings/staging
func (h *SettingsHandler) GetStagingInfo(w http.ResponseWriter, r *http.Request) {
 override, _ := h.db.GetSetting("staging_dir_override", "")
 dests, err := h.db.ListStorageDestinations()
 if err != nil {
  respondError(w, http.StatusInternalServerError, err.Error())
  return
 }
 configs := make([]tempdir.StorageConfig, len(dests))
 for i, d := range dests {
  configs[i] = tempdir.StorageConfig{Type: d.Type, Config: d.Config}
 }
 info := tempdir.ResolveInfo(configs, override)
 respondJSON(w, http.StatusOK, info)
}

// SetStagingOverride sets or clears the staging directory override.
//
// PUT /api/v1/settings/staging
func (h *SettingsHandler) SetStagingOverride(w http.ResponseWriter, r *http.Request) {
 var req struct {
  Override string `json:"override"`
 }
 if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
  respondError(w, http.StatusBadRequest, "invalid JSON")
  return
 }

 // Validate the path if non-empty.
 if req.Override != "" {
  if !filepath.IsAbs(req.Override) {
   respondError(w, http.StatusBadRequest, "path must be absolute")
   return
  }
  if fi, err := os.Stat(req.Override); err != nil || !fi.IsDir() {
   respondError(w, http.StatusBadRequest, "path does not exist or is not a directory")
   return
  }
 }

 if err := h.db.SetSetting("staging_dir_override", req.Override); err != nil {
  respondError(w, http.StatusInternalServerError, err.Error())
  return
 }

 // Return updated staging info.
 h.GetStagingInfo(w, r)
}
```

Add imports: `"os"`, `"path/filepath"`, and `"github.com/ruaandeysel/vault/internal/tempdir"`.

**Step 4: Add routes**

In `internal/api/routes.go`, inside the settings route block (after line 92):

```go
r.Route("/settings", func(r chi.Router) {
 r.Get("/", settingsH.List)
 r.Put("/", settingsH.Update)
 r.Get("/encryption", settingsH.GetEncryptionStatus)
 r.Post("/encryption", settingsH.SetEncryption)
 r.Post("/encryption/verify", settingsH.VerifyEncryption)
 r.Get("/encryption/passphrase", settingsH.GetEncryptionPassphrase)
 r.Get("/api-key", settingsH.GetAPIKeyStatus)
 r.Post("/api-key/rotate", settingsH.RotateAPIKey)
 r.Delete("/api-key", settingsH.RevokeAPIKey)
 r.Get("/staging", settingsH.GetStagingInfo)
 r.Put("/staging", settingsH.SetStagingOverride)
})
```

**Step 5: Run tests**

Run: `go test ./internal/api/handlers/... -run TestGetStagingInfo -v && go test ./internal/api/handlers/... -run TestSetStagingOverride -v`
Expected: PASS

**Step 6: Run full test suite**

Run: `go test ./... -short -v`
Expected: All pass.

**Step 7: Commit**

```bash
git add internal/api/handlers/settings.go internal/api/handlers/settings_test.go internal/api/routes.go
git commit -m "feat: add staging directory info and override API endpoints"
```

---

## Task 9: Add staging API client methods

**Files:**

- Modify: `web/src/lib/api.js:63-75`

**Step 1: Add staging API methods**

In `web/src/lib/api.js`, add after the settings section (around line 69):

```js
// Staging
getStagingInfo: () => request('GET', '/settings/staging'),
setStagingOverride: (override) => request('PUT', '/settings/staging', { override }),
```

**Step 2: Verify web build**

Run: `cd web && npm run build`
Expected: Build succeeds.

**Step 3: Commit**

```bash
git add web/src/lib/api.js
git commit -m "feat: add staging API client methods"
```

---

## Task 10: Add staging directory section to Settings UI

**Files:**

- Modify: `web/src/pages/Settings.svelte`

**Step 1: Add staging state variables**

Add near the other state declarations at the top of `<script>`:

```js
let stagingInfo = $state(null);
let stagingOverrideInput = $state("");
let stagingSaving = $state(false);
let cascadeExpanded = $state(false);
```

**Step 2: Load staging info on mount**

In the `onMount` block, add `api.getStagingInfo()` to the `Promise.all`:

```js
const [h, s, enc, keyStatus, staging] = await Promise.all([
  api.health(),
  api.getSettings(),
  api.getEncryptionStatus(),
  api.getApiKeyStatus(),
  api.getStagingInfo().catch(() => null),
]);
// ... existing assignments ...
stagingInfo = staging;
stagingOverrideInput = staging?.override || "";
```

**Step 3: Add save/reset handlers**

```js
async function saveStagingOverride() {
  stagingSaving = true;
  try {
    stagingInfo = await api.setStagingOverride(stagingOverrideInput);
    stagingOverrideInput = stagingInfo?.override || "";
    showToast(
      stagingOverrideInput ? "Staging path updated" : "Staging reset to auto",
      "success",
    );
  } catch (e) {
    showToast(e.message, "error");
  } finally {
    stagingSaving = false;
  }
}

async function resetStagingOverride() {
  stagingOverrideInput = "";
  stagingSaving = true;
  try {
    stagingInfo = await api.setStagingOverride("");
    showToast("Staging reset to automatic cascade", "success");
  } catch (e) {
    showToast(e.message, "error");
  } finally {
    stagingSaving = false;
  }
}
```

**Step 4: Add the Staging Directory card in the General tab**

Between the Appearance section and Server Information section, add:

```svelte
<!-- Staging Directory -->
{#if activeTab === 'general' && stagingInfo}
  <div class="bg-surface border border-border rounded-xl overflow-hidden">
    <div class="px-5 py-4 border-b border-border">
      <h2 class="text-base font-semibold text-text">Staging Directory</h2>
    </div>
    <div class="p-5 space-y-4">
      <div>
        <p class="text-sm text-text font-mono">{stagingInfo.resolved_path}</p>
        <p class="text-xs text-text-muted mt-0.5">
          {stagingInfo.source === 'override' ? 'Custom override' :
           stagingInfo.source === 'cache' ? 'SSD Cache (automatic)' :
           stagingInfo.source === 'local-storage' ? 'Local storage fallback' :
           'System temp fallback'}
        </p>
      </div>

      <!-- Disk space bar -->
      {#if stagingInfo.disk_total_bytes > 0}
        {@const usedPct = ((stagingInfo.disk_total_bytes - stagingInfo.disk_free_bytes) / stagingInfo.disk_total_bytes) * 100}
        <div>
          <div class="h-2 rounded-full bg-surface-2 overflow-hidden">
            <div class="h-full rounded-full transition-all {usedPct > 90 ? 'bg-danger' : usedPct > 70 ? 'bg-warning' : 'bg-success'}"
                 style="width: {usedPct.toFixed(1)}%"></div>
          </div>
          <p class="text-xs text-text-muted mt-1">
            {formatBytes(stagingInfo.disk_free_bytes)} free of {formatBytes(stagingInfo.disk_total_bytes)}
          </p>
        </div>
      {/if}

      <!-- Custom path override -->
      <div>
        <label class="text-xs text-text-muted block mb-1">Custom Path (optional)</label>
        <div class="flex gap-2">
          <input
            type="text"
            bind:value={stagingOverrideInput}
            placeholder="/mnt/nvme"
            class="flex-1 px-3 py-1.5 text-sm bg-surface-2 border border-border rounded-lg text-text placeholder:text-text-muted/50"
          />
          <button
            onclick={saveStagingOverride}
            disabled={stagingSaving}
            class="btn btn-primary text-sm"
          >
            Set
          </button>
        </div>
      </div>

      <!-- Reset to auto (only shown when override is active) -->
      {#if stagingInfo.override}
        <button onclick={resetStagingOverride} disabled={stagingSaving} class="text-xs text-vault hover:underline">
          Reset to automatic
        </button>
      {/if}

      <!-- Cascade order (collapsible) -->
      <div>
        <button onclick={() => cascadeExpanded = !cascadeExpanded} class="text-xs text-text-muted hover:text-text flex items-center gap-1">
          <svg class="w-3 h-3 transition-transform {cascadeExpanded ? 'rotate-90' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/>
          </svg>
          Cascade order
        </button>
        {#if cascadeExpanded}
          <div class="mt-2 space-y-1 text-xs text-text-muted">
            {#each stagingInfo.cascade as item, i}
              <div class="flex items-center gap-2">
                <span>{i + 1}.</span>
                <span class="font-mono">{item.path}</span>
                <span class="text-text-muted">({item.source})</span>
                {#if item.available}
                  <svg class="w-3.5 h-3.5 text-success" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/>
                  </svg>
                {:else}
                  <svg class="w-3.5 h-3.5 text-text-muted" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/>
                  </svg>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
      </div>
    </div>
  </div>
{/if}
```

**Step 5: Verify web build succeeds**

Run: `cd web && npm run build`
Expected: Build succeeds.

**Step 6: Commit**

```bash
git add web/src/pages/Settings.svelte
git commit -m "feat: add staging directory configuration to Settings UI"
```

---

## Task 11: Final verification and build

**Files:**

- No files changed — verification only.

**Step 1: Run Go linter**

Run: `make lint`
Expected: No errors.

**Step 2: Run full Go test suite**

Run: `make test`
Expected: All pass.

**Step 3: Build the full project**

Run: `make build-local`
Expected: Binary compiles successfully.

**Step 4: Build web assets**

Run: `cd web && npm run build`
Expected: No errors.

**Step 5: Manual verification with Playwright**

Start the daemon and verify:

1. Dashboard shows avg speed below health gauge
2. Activity timeline entries show speed chips (e.g. "32.2 MB/s")
3. History page entries show speed chips
4. Settings > General tab shows Staging Directory section with:
   - Current path and source label
   - Disk space bar with free/total
   - Custom path input with Set button
   - Cascade order (collapsible)

**Step 6: Commit all remaining changes (if any)**

```bash
git add -A
git commit -m "chore: final verification - throughput display and staging config"
```
