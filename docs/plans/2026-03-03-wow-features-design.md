# Vault "Wow Factor" Features Design

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:executing-plans to implement this plan task-by-task.

**Goal:** Make Vault the best backup tool for Unraid servers by adding features no competitor has — proving backups work, adding intelligence, and making migration effortless.

**Architecture:** Three phased releases building on Vault's existing infrastructure: Phase 5 adds trust & notifications (Discord, health checks, 3-2-1 score, DR page), Phase 6 adds intelligence (size predictions, retention forecasting, calendar view, bandwidth throttling), Phase 7 adds migration & polish (legacy import, smart scheduling).

**Tech Stack:** Go backend (Chi router, SQLite, Docker SDK), Svelte 5 frontend, Discord webhook API, golang.org/x/time/rate for throttling.

---

## Phase 5: Trust & Notifications

### Feature 1: Discord Webhook Notifications

**Problem:** Unraid's native notification system is limited. Users want push notifications on their phones via Discord.

**Design:**

Settings storage:

- `discord_webhook_url` — stored in the `settings` table
- `discord_notify_on` — `always` | `failure` | `never` (default: `always`)

New backend file: `internal/notify/discord.go`

- `SendDiscordNotification(webhookURL string, embed DiscordEmbed) error`
- POST to webhook URL with JSON payload containing a rich embed
- Embed fields: job name, status (color-coded: green success, red failure), duration, size, speed, item count, timestamp
- Failed backups include error message and failed item names
- Replication sync events also trigger notifications
- Timeout: 10 seconds, non-blocking (fire and forget with error logging)

Embed structure:

```json
{
  "embeds": [
    {
      "title": "✅ Backup Completed",
      "description": "Full Backup Test",
      "color": 5763719,
      "fields": [
        { "name": "Duration", "value": "11m 23s", "inline": true },
        { "name": "Size", "value": "18 GB", "inline": true },
        { "name": "Speed", "value": "27 MB/s", "inline": true },
        { "name": "Items", "value": "15/15 succeeded", "inline": true }
      ],
      "timestamp": "2026-03-03T08:30:00Z",
      "footer": { "text": "Vault Backup Manager" }
    }
  ]
}
```

UI changes in Settings > Notifications tab:

- Discord webhook URL input field
- "Send Test" button to verify the webhook
- Dropdown for notification level (always/failure/never)

Integration point: `internal/runner/runner.go` — after sending Unraid notification, also send Discord notification if configured.

### Feature 2: Container Health Checks Post-Backup

**Problem:** "Started" doesn't mean "healthy." Containers can be in restart loops or unhealthy after backup.

**Design:**

New function in `internal/engine/container.go`:

```go
func VerifyContainerHealth(ctx context.Context, cli *client.Client, containerID string, timeout time.Duration) (*HealthCheckResult, error)
```

Health check sequence (60-second timeout per container):

1. Poll Docker container state every 2 seconds
2. If container has a HEALTHCHECK defined → wait for `healthy` status
3. If no HEALTHCHECK → verify state is `running` (not `restarting`, `exited`, `dead`)
4. If container exposes ports → attempt TCP connection to first exposed port

Result struct:

```go
type HealthCheckResult struct {
    ContainerName string
    Status        string // "healthy", "running", "unhealthy", "slow_recovery"
    Duration      time.Duration
    Message       string
}
```

Runner integration:

- After `StartContainers()` in `RunJob()`, call `VerifyContainerHealth()` for each restarted container
- Results stored in the job run log
- WebSocket event: `container_health_check` with per-container status
- Does NOT fail the backup — purely informational
- Included in Discord notification if any containers are slow/unhealthy

Dashboard display:

- After backup completes, show health summary: "15/15 containers healthy" or "14/15 healthy, 1 slow recovery: plex"
- Warning badge on items with slow recovery

### Feature 3: 3-2-1 Compliance Score

**Problem:** Everyone knows the 3-2-1 rule but no tool actually tracks compliance.

**Design:**

Purely frontend computation — no new backend endpoints needed. Uses existing `api.listStorage()`, `api.listJobs()`, and replication data.

Scoring logic:

**3 Copies (max 3 points):**

- Count distinct storage destinations that have at least one enabled job
- Add replication destinations (each remote source = +1 copy)
- Score: min(copies, 3) / 3

**2 Media Types (max 2 points):**

- Categorize: `local` = "disk", `sftp`/`smb`/`nfs` = "network"
- Score: min(unique_types, 2) / 2

**1 Offsite (max 1 point):**

- Any non-local storage destination with an enabled job = offsite
- Any replication source = offsite
- Score: has_offsite ? 1 : 0

Overall: `(copies_score + media_score + offsite_score) / 3` → percentage

UI: Dashboard widget below the Health Gauge

- Three checkmarks/crosses for each rule
- Expandable detail showing which destinations satisfy which rules
- Actionable suggestions: "Add a remote storage destination to complete your 3-2-1 strategy" with link to Storage page
- Color: green (3/3), yellow (2/3), red (0-1/3)

### Feature 4: Disaster Recovery Interactive Guide

**Problem:** Users have backups but no plan for what to do when the server dies.

**Design:**

New sidebar page: **"Recovery"** (icon: shield with arrow)

New backend endpoint: `GET /api/v1/recovery/plan`

Response compiles a recovery plan from existing DB data:

```json
{
  "server_info": {
    "vault_version": "2026.03.00",
    "total_protected_items": 15,
    "total_unprotected_items": 4
  },
  "flash_backup": {
    "available": true,
    "last_backup": "2026-03-03T08:30:00Z",
    "storage_name": "Local Backups",
    "storage_path": "/mnt/user/backups/vault/...",
    "size_bytes": 52428800
  },
  "steps": [
    {
      "step": 1,
      "title": "Restore Flash Drive",
      "description": "Download and restore your Unraid flash drive configuration",
      "status": "ready",
      "details": {...}
    },
    {
      "step": 2,
      "title": "Install Vault",
      "description": "Install the Vault plugin and restore the database",
      "status": "ready",
      "details": {...}
    },
    {
      "step": 3,
      "title": "Restore Containers (14)",
      "description": "Restore all Docker containers from backup",
      "status": "ready",
      "items": [...],
      "estimated_time": "15m",
      "total_size": "18 GB"
    },
    {
      "step": 4,
      "title": "Restore VMs (1)",
      "description": "Restore virtual machines from backup",
      "status": "ready",
      "items": [...],
      "estimated_time": "5m",
      "total_size": "50 GB"
    }
  ],
  "warnings": [
    "3 VMs are unprotected: Windows-Server-2016, Windows_Server_2016, WindowsServer2016",
    "Flash Drive folder is not in any backup job"
  ],
  "last_updated": "2026-03-03T08:30:00Z"
}
```

UI layout:

- Hero section: "Your Recovery Plan" with overall readiness status
- Step-by-step cards, each expandable with detailed instructions
- Per-step: last backup time, storage location, download button where applicable
- Warnings section at top for unprotected items or stale backups (>7 days old)
- "Recovery Readiness" percentage based on how many items are protected and recently backed up

---

## Phase 6: Intelligence

### Feature 5: Backup Size Predictions

**Design:**

New backend endpoint: `GET /api/v1/jobs/{id}/estimate`

Response:

```json
{
  "estimated_size_bytes": 19327352832,
  "confidence": "high",
  "based_on_runs": 5,
  "storage_free_bytes": 465661751296,
  "runs_remaining": 24,
  "message": "Estimated ~18 GB. You have 433 GB free (24 more runs possible)."
}
```

Calculation:

- Weighted average of last 5 run sizes (most recent = weight 5, oldest = weight 1)
- If fewer than 3 runs: confidence = "low", estimate from Docker inspect volume sizes + `du` for folders
- If no history at all: scan source items for initial estimate

UI integration:

- Jobs page: small text under each job card: "Est. next: ~18 GB"
- "Run Now" confirmation dialog: "This will use approximately 18 GB. You have 433 GB free."
- Warning if estimated size > 80% of remaining free space

### Feature 6: Retention Forecasting

**Design:**

Enhanced storage info with forecasting.

New fields on `GET /api/v1/storage/{id}`:

```json
{
  "usage_bytes": 194323456,
  "free_bytes": 465661751296,
  "total_bytes": 476940001280,
  "forecast_days_until_full": 45,
  "daily_growth_bytes": 10345678901
}
```

Storage adapter enhancement — new optional interface:

```go
type SpaceReporter interface {
    Space() (used, free, total uint64, err error)
}
```

- Local: `syscall.Statfs`
- SFTP: `sftp.StatVFS`
- SMB: query share info
- NFS: `syscall.Statfs` on mounted path

Forecast calculation:

- Average daily backup size from last 30 days of run history for all jobs targeting this destination
- `days_until_full = free_bytes / daily_growth_bytes`
- Factor in retention: if retention deletes old backups, net growth may be lower

UI on Storage page:

- Per destination: disk usage bar + "~45 days until full" or "Stable (retention keeps up)"
- Color-coded: green (>30 days), yellow (7-30 days), red (<7 days)
- Warning toast if any destination is <7 days from full

### Feature 7: Calendar View

**Design:**

New component: `web/src/components/BackupCalendar.svelte`

Accessible from Dashboard (expandable section) or as a tab on the History page.

Visual: Month grid showing 4-6 weeks

- Each day cell shows colored dots:
  - Green dot: all runs that day succeeded
  - Red dot: at least one run failed
  - Gray dot: scheduled but didn't run (missed)
  - No dot: no backup scheduled
- Future dates show faded markers for scheduled runs
- Clicking a date opens a popover with that day's run details
- Month navigation (prev/next arrows)

Data sources:

- Past data: `api.getActivity()` with date filtering (already exists)
- Future data: `api.getNextRuns()` projected forward by cron expression

Implementation: Lightweight CSS grid, no external dependencies. ~200 lines of Svelte.

### Feature 8: Bandwidth Throttling

**Design:**

Per-storage-destination config field: `max_speed_mbps` (integer, 0 = unlimited)

New file: `internal/storage/throttle.go`

```go
type ThrottledWriter struct {
    w       io.Writer
    limiter *rate.Limiter
}

func NewThrottledWriter(w io.Writer, mbps int) io.Writer {
    if mbps <= 0 { return w }
    bytesPerSec := mbps * 1024 * 1024
    limiter := rate.NewLimiter(rate.Limit(bytesPerSec), bytesPerSec)
    return &ThrottledWriter{w: w, limiter: limiter}
}
```

Integration:

- `factory.go` wraps the adapter's Write method with throttling when `max_speed_mbps > 0`
- Only applies to remote types (SFTP, SMB, NFS) — local is always unlimited
- Token bucket algorithm from `golang.org/x/time/rate` — smooth, no bursts

UI: Number input on storage form for remote types: "Max Transfer Speed (MB/s)" with "0 = unlimited" hint. Hidden for local storage.

---

## Phase 7: Migration & Polish

### Feature 9: CA Backup / Appdata Backup Migration Assistant

**Design:**

New backend endpoints:

- `GET /api/v1/migrate/detect` — scans for legacy config files
- `POST /api/v1/migrate/import` — creates Vault configuration from legacy data

Detection paths:

- CA Backup v2: `/boot/config/plugins/ca.backup2/BackupOptions.cfg`
- Appdata Backup: `/boot/config/plugins/appdata.backup/config.json` or `config.cfg`

Config mapping:

| Legacy Setting               | Vault Equivalent                  |
| ---------------------------- | --------------------------------- |
| Backup source (appdata path) | Container items (matched by name) |
| Backup destination path      | Local storage destination         |
| Schedule (cron)              | Job schedule                      |
| Compression: gzip            | compression: gzip                 |
| Compression: zstdmt          | compression: zstd                 |
| Flash backup: yes            | Add flash drive folder item       |
| VM meta backup: yes          | Add VM items                      |
| Excluded containers          | Skip those items                  |

UI: One-time wizard in Settings or Welcome screen

- Step 1: "We detected Appdata Backup configuration. Import it?"
- Step 2: Preview what will be created (job name, items, schedule, destination)
- Step 3: "Import complete! 1 job created with 12 containers."
- If no legacy config found, the button is hidden

### Feature 10: Smart Scheduling

**Design:**

New backend endpoint: `GET /api/v1/jobs/suggest-schedule`

Response:

```json
{
  "suggested_time": "03:00",
  "reason": "Lowest average CPU usage across all containers (2.3% avg between 3:00-4:00 AM)",
  "activity_profile": [
    {"hour": 0, "cpu_avg": 5.2},
    {"hour": 1, "cpu_avg": 4.8},
    ...
    {"hour": 23, "cpu_avg": 12.1}
  ],
  "stagger_plan": [
    {"job_id": 1, "suggested_time": "03:00"},
    {"job_id": 2, "suggested_time": "03:30"}
  ]
}
```

Implementation:

- Sample Docker container stats (`docker stats --no-stream`) every 15 minutes for 24 hours
- Store hourly averages in a `system_stats` table
- Find the 2-hour window with lowest average CPU
- For multiple jobs: stagger by 30 minutes within the window

UI: On the Schedule step of job creation/edit:

- "Suggest optimal time" button next to the time picker
- Shows: "Based on your container activity, we recommend 3:00 AM (lowest usage period)"
- User can accept or ignore the suggestion

---

## Feature Summary

| Phase | Feature                 | Impact    | Effort |
| ----- | ----------------------- | --------- | ------ |
| 5     | Discord Notifications   | High      | Low    |
| 5     | Container Health Checks | High      | Medium |
| 5     | 3-2-1 Compliance Score  | High      | Low    |
| 5     | Disaster Recovery Page  | Very High | Medium |
| 6     | Backup Size Predictions | Medium    | Medium |
| 6     | Retention Forecasting   | Medium    | Medium |
| 6     | Calendar View           | Medium    | Low    |
| 6     | Bandwidth Throttling    | Low       | Low    |
| 7     | Migration Assistant     | High      | Medium |
| 7     | Smart Scheduling        | Medium    | High   |

---

## Competitive Advantage

After all phases, Vault will be the only Unraid backup tool that:

- **Proves backups work** (health checks + verification)
- **Sends beautiful Discord notifications** with rich status embeds
- **Tracks 3-2-1 compliance** as an actionable score
- **Provides a disaster recovery plan** specific to the user's server
- **Predicts backup sizes** and warns before storage runs out
- **Shows backup coverage** on a visual calendar
- **Throttles bandwidth** to avoid network saturation
- **Imports existing configurations** from competing plugins
- **Suggests optimal backup times** based on actual container activity

No other Unraid backup tool — CA Backup, Appdata Backup, VM Backup, or Duplicati — offers any of these features.

---

## Quick Fixes (Pre-Phase 5)

These are small improvements to ship immediately alongside the wow features.

### Fix 1: Restore Activity in Dashboard Timeline

**Problem:** The Dashboard "Recent Activity" shows all job runs but doesn't visually distinguish restore runs from backup runs.

**Fix:**

- In `ActivityTimeline.svelte`, check `run.run_type` field
- If `run_type === 'restore'`, show a blue "restored" badge instead of green "completed"
- Add a restore icon (rotate-ccw) next to the job name for restore runs
- The data is already included — `GetJobRuns` returns all run types

### Fix 2: Locale-Aware Date/Time Formatting

**Problem:** `formatDate()` in `utils.js` and date labels in `ActivityTimeline.svelte` are hardcoded to `'en-US'` locale, showing US-style dates regardless of the user's region.

**Fix:**

- In `web/src/lib/utils.js`, change `formatDate()` to use `undefined` locale instead of `'en-US'`:

  ```js
  d.toLocaleDateString(undefined, { ... })
  ```

  This makes the browser use the system's locale (e.g., "3 Mar 2026 2:25 pm" in Australia).

- In `ActivityTimeline.svelte`, change the date group label to also use `undefined` locale.
- This single-line change respects whatever region the user's browser is set to.

### Fix 3: Auto-Purge Old Failed Runs

**Problem:** Failed backup runs clutter the History page forever.

**Fix:**

- Add a new setting: `failed_run_retention_days` (default: 30)
- On daemon startup (in `daemon.go`), after pruning activity logs, also prune old failed job runs:

  ```sql
  DELETE FROM job_runs WHERE status IN ('failed', 'error')
    AND completed_at < datetime('now', '-30 days')
  ```

- Add corresponding `DeleteOldFailedRuns(keepDays int)` method to `job_repo.go`
- Successful runs are kept forever (governed by the job's retention policy)
- UI: Add the setting to Settings > General as "Auto-remove failed runs after N days"

### Fix 4: Memory & Logging Safeguards

**Investigation result:** Vault uses only **44 MB RSS** on the Unraid server. The "System: 9.57 GiB" shown in Unraid's dashboard is Linux kernel page cache and buffers (normal behavior — Linux aggressively caches disk I/O in free RAM). The actual available memory is 19 GiB. Vault is not the memory culprit.

**Safeguards to add anyway:**

- **Log rotation:** The daemon already uses Go's `log` package which writes to stdout/stderr. The RC script redirects to `/var/log/vault.log`. Add `logrotate`-style truncation: on startup, if vault.log > 10 MB, truncate to last 5000 lines.
- **Activity log cap:** Already prunes entries older than 90 days on startup. Add a row count cap: if > 10,000 entries, delete oldest entries beyond that limit.
- **Job run cap:** After retention enforcement per job, also enforce a global cap: if any job has > 500 runs, delete the oldest beyond that limit.
- **SQLite VACUUM:** Run `VACUUM` after large deletions (startup cleanup) to reclaim disk space.
