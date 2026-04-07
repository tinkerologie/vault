# Changelog

All notable changes to the Vault plugin will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project uses date-based versioning (`YYYY.MM.PATCH`).

## [Unreleased]

### Added

- ZFS dataset backup and restore engine using pure-Go `gzfs` library — supports full and incremental ZFS send/receive streams with progress tracking (Refs #4)
- ZFS dataset discovery endpoint `GET /api/v1/zfs` lists ZFS filesystems and volumes available for backup
- ZFS Datasets tab in the job creation item picker with dataset type badges (Filesystem/Volume), mountpoint, and used-space indicators
- ZFS dataset count displayed in the job summary card alongside containers, VMs, folders, and plugins
- `zfs_meta.json` sidecar written alongside each ZFS send stream to record dataset name, snapshot, pool, and backup type for reliable restore
- Automatic cleanup of old vault-created ZFS snapshots after successful backup, keeping only the latest
- Snapshot cleanup on send failure to prevent orphaned ZFS snapshots
- Cloud replication targets — Google Drive and OneDrive now available as replication target types alongside Remote Vault Server
- Push-based cloud sync engine (`syncCloudPush`) that uploads local backup restore points to cloud storage adapters
- Type-aware replication target creation and editing with conditional forms for each target type
- `type` and `config` columns on `replication_sources` table to support different target types (remote_vault, gdrive, onedrive)
- Redesigned Create Backup Job wizard from 3 steps to a guided 6-step flow: Type → Items → Schedule → Details → Advanced → Review (closes #36)
- New `TypePicker` component for Step 1: card-based multi-select grid with automatic item discovery, per-type color coding, and availability detection for Containers, VMs, Folders, Flash Drive, Plugins, and ZFS Datasets
- `ItemPicker` now accepts an `allowedTypes` prop to filter visible tabs based on the types selected in Step 1; single-type selection hides the tab bar entirely
- Auto-generated job name suggestions based on selected backup types (e.g. "Containers + VMs Backup")
- Advanced settings (Step 5) grouped into collapsible accordion sections: Retention, Scripts, Verification, VM Restore Verify, and Container Exclusions — contextually shown based on selected types
- Contextual tooltips across Settings, Jobs, Storage, and Replication pages — reusable `Tooltip.svelte` component with hover/click-to-toggle, viewport-aware positioning, keyboard dismissal, and full ARIA accessibility (Refs #34)
- Enriched activity logs with contextual details for troubleshooting: backup started/completed and restore completed entries now include job name, backup type, storage destination, duration, and size; per-item container health check results are logged individually under a new "health" category; stop_all health check summary includes aggregate counts (containers checked/healthy/unhealthy) (Refs #30)
- "Health" category filter on the Logs page to isolate container health check entries
- Smart formatting for activity log detail badges: backup types are capitalised, durations show unit suffixes, byte sizes are human-readable (e.g. 2.2 GB), and null values are hidden
- Diagnostic bundle download: `GET /api/v1/settings/diagnostics` endpoint and "Download diagnostics bundle" button on the Settings page generates a ZIP containing system info, database details, storage destinations, job configurations, recent run history, and activity logs with a unique correlation ID for support workflows (Refs #29)
- `internal/diagnostics` package with collector, ZIP packager, and comprehensive redaction for sensitive data (passwords, API keys, tokens, webhook secrets, inline URL credentials)
- `ListRecentRuns(limit)` database method for fetching recent job runs across all jobs
- Purge activity logs: `DELETE /api/v1/activity` endpoint and "Purge" button on the Logs page with confirmation dialog to permanently delete all activity log entries (Refs #32)
- Purge job run history: `DELETE /api/v1/history` endpoint and "Purge" button on the History page with confirmation dialog to permanently delete all job run records (Refs #32)
- `PurgeJobRuns()` database method for bulk deletion of job run history; activity log purge reuses `DeleteOldActivityLogs(0)` to clear all entries
- Job run history purge actions are logged in the activity log with the count of deleted records
- Cancel API endpoint `POST /api/v1/jobs/{id}/cancel` to abort a running backup job (Refs #28)
- Cancellable context propagated through the entire backup pipeline: Runner → engine handlers → tar/copy I/O operations
- 4-hour job timeout with automatic cancellation via `context.WithTimeout`
- Stall detection: warns after 30 minutes of no progress, auto-cancels after 2 hours of inactivity
- `cancelling` field added to runner status for real-time UI feedback
- `job_cancelling` WebSocket event broadcast when cancellation is requested
- "cancelled" job run status with descriptive log messages (user-initiated vs timeout)
- Context-aware `contextCopy` helper that checks for cancellation every 32 KiB during file I/O
- `ctx.Err()` checks in `filepath.Walk` callbacks to abort directory traversal on cancellation
- Backup target category toggles in Settings → General: independently enable/disable tracking for Containers, Virtual Machines, and Flash Drive; disabled categories are excluded from protection status on the Dashboard and readiness metrics on the Recovery page (Refs #20)
- Three new settings keys (`container_backup_enabled`, `vm_backup_enabled`, `flash_backup_enabled`) with `"true"` defaults in the settings API
- Monthly and yearly scheduling now support "First day of month" and "Last day of month" options in the schedule builder UI; last-day jobs use a daily-check pattern on the backend with an `isLastDayOfMonth()` guard so they fire correctly on months of any length (Refs #15)
- Unraid display time format is now detected from `dynamix.cfg` and injected into the runtime config, allowing the UI to honour the user's 12-hour or 24-hour preference (Refs #12)
- Go daemon (direct-access mode) now injects `window.__VAULT_RUNTIME_CONFIG__` into the SPA HTML, ensuring time format detection works when accessing Vault directly on port 24085 without the PHP proxy
- `getTimeFormat()` and `getHour12()` helpers added to `runtime-config.js` for locale-aware time rendering
- `formatDate()` utility now used consistently for all date/time display in the Storage and Settings pages

### Changed

- Moved Google Drive and OneDrive from the Storage page to the Replication page — Storage now only handles local/network destinations (Local Path, SFTP, SMB, NFS)
- OAuth endpoints for Google Drive and OneDrive moved from `/api/v1/storage/` to `/api/v1/replication/` (e.g. `/api/v1/replication/gdrive/status`)
- OAuth handlers changed from `StorageHandler` to `ReplicationHandler` receiver
- Replication "Add Target" modal now offers three target types: Remote Vault Server, Google Drive, and OneDrive
- Removed "Local Storage Destination" field from Remote Vault replication targets — remote server uses its own configured storage path automatically
- Replication target cards now display type-appropriate icons and labels (Google Drive, OneDrive, or server URL)
- Renamed "Staging Directory" section to "Temporary Work Area" with descriptive subtitle explaining its purpose (Refs #13)
- Replaced "SSD Cache (automatic)" label with "Using SSD cache for fast backup processing" and "Custom override" with "Custom location"
- Renamed "Custom Path (optional)" to "Custom Location" with description: "Override the automatic location. Use this if you want backups to be assembled on a specific drive."
- Renamed "Cascade order" to "Fallback locations" with description: "Vault tries each location in order and uses the first available one."
- Updated Database Location subtitle to explain that Vault's database tracks jobs, schedules, and restore points
- Replaced "Hybrid (RAM + SSD snapshots)" with "Hybrid — runs in memory for speed, saves to SSD periodically"
- Renamed "Working" to "Active database" with tooltip explaining hybrid mode operates from RAM
- Renamed "Snapshot" to "Saved copy", "Last snapshot" to "Last saved", and "Snapshot size" to "Saved copy size"
- Renamed "Custom Snapshot Path (optional)" to "Custom save location" with description: "Choose where the persistent database copy is stored. Defaults to SSD cache."
- Enhanced USB warning to suggest adding a cache drive or setting a custom save location
- Simplified Backup Targets subtitle to "Select what Vault should monitor. Disabled items won't show as unprotected on Dashboard or Recovery."
- `engine.Handler` interface now accepts `context.Context` as the first parameter for `Backup()` and `Restore()`
- All engine handlers (Container, VM, Folder, Plugin) updated to accept and propagate context
- `Runner.backupItem()` now receives and passes context to engine handlers

### Fixed

- Tooltip clipping when positioned near viewport edges — switched from `position: absolute` to `position: fixed` with JS-calculated viewport coordinates and horizontal clamping
- Container path exclusion presets now load correctly when Vault runs behind the Unraid web proxy; `fetchContainerPresets()` uses `buildApiRequest()` instead of raw `fetch()` to route through the authenticated proxy endpoint (Refs #11)
- Stuck backup jobs can no longer run indefinitely — timeout and stall detection ensure jobs are always bounded (Refs #28)
- Time format detection now falls back to `[notify][time]` in `dynamix.cfg` when `[display][time]` is absent, fixing detection on Unraid 7.x where the time format preference is stored in the notification settings section
- Unraid Settings/Vault page was blank due to duplicated PHP code in `api.php` causing a syntax error; removed the corrupted duplicate block to restore the service control panel, Web UI button, and port/binding configuration
- SMB and SFTP storage adapters now honour the "Path" field: frontend forms send `base_path` matching the backend struct, and adapters accept the legacy `path` JSON key as a fallback for backward compatibility (Refs #25)
- Job deletion with "Delete Backup Files" now properly removes empty directories after deleting their contents, fixing the issue where backup files and directories were left on Local and SMB storage (Refs #26)
- SMB adapter `Write()` now propagates `MkdirAll` errors instead of silently ignoring them
- `ItemPicker` selected-items map wrapped in `$state()` to ensure Svelte 5 reactive tracking (Refs #22)
- Items deleted from Unraid (containers, VMs, folders, plugins) can now be removed from backup jobs via the new remove button in the Backup Order list; stale items that no longer exist on the system are visually flagged with a "Not found" warning indicator (Refs #24)
- Container volume backups now skip Unix sockets, character/block devices, and named pipes instead of failing with "sockets not supported" errors; affected containers (e.g. those mounting `/var/run/docker.sock`) will complete successfully with a log entry for each skipped special file (Refs #5)
- Monthly schedule day picker now shows all 31 days instead of only days 1–28; previously `Array(27)` omitted days 29, 30, and 31 (Refs #9)
- Storage destination save dialog now has a `saving` guard to prevent double-click duplicate submissions, matching the pattern already used in Jobs (Refs #10)

### Removed

- API Access feature completely removed from Security settings — API key generation, rotation, revocation, and status endpoints are no longer available
- `X-API-Key` authentication header and `APIKeyAuth` middleware removed; the daemon no longer requires or accepts API keys
- API key field removed from Replication targets — remote sources connect without authentication
- `--api-key` CLI flag and `VAULT_API_KEY` environment variable removed from `daemon` and `replica` commands
- `/auth/status`, `/settings/api-key/generate`, `/settings/api-key/rotate`, `/settings/api-key/revoke`, `/settings/api-key` endpoints removed
- `api_key` column removed from `replication_sources` database schema
- `LoginPrompt.svelte` component deleted (unused)

## [2026.03.02] - 2026-03-19

### Added

- Cleaned up documentation.

### Fixed

- Tailscale enabled containers backups failing.
- UI/UX fixes and polish.

## [2026.03.01] - 2026-03-18

### Added

- MCP tools for plugin discovery and runner status
- Restore-point chain health annotations in the API and MCP restore-point listings

### Changed

- MCP health output now includes version and mode, aligned with the REST `/health` response
- README refreshed to document the current REST API, MCP transports, and tool coverage
- `make verify` now exercises MCP streamable HTTP via the official Go SDK client
- Release packaging now targets `.txz` bundles with SHA256 verification and release automation updates the PLG checksum accordingly
- The PLG now advertises `project` and `readme` metadata and prunes stale cached plugin bundles during install

### Fixed

- Restore-point docs now reflect chain health and retention-preserved parents
- Repository URLs now point at `ruaan-deysel/vault`
- UI Fixes

## [2026.03.00] - 2026-03-02

### Added in 2026.03.00

- Full backup/restore engine for Docker containers, libvirt VMs, and folders
- Storage backends: Local, SMB, NFS, SFTP, NFS
- Cron-based job scheduling with retention policies
- Svelte 5 web UI with real-time WebSocket progress
- API key authentication and TLS support
- Encrypted storage credentials (AES-256-GCM sealed passphrase)
- Replication: sync restore points to remote Vault instances
- Job duplication with one-click copy
- Mobile-responsive bottom navigation bar
- Pull-to-refresh on Dashboard and History pages
- Theme cycle keyboard shortcut (Ctrl+Shift+L)
- Aria-labels on all icon-only action buttons
- Backup size trend chart (filters to completed runs only)
- Ansible-driven build/deploy/verify pipeline
- Proper Unraid plugin bundle pattern with MD5 verification
- GitHub Actions release workflow with automatic PLG MD5 update

### Fixed in 2026.03.00

- Backup trend chart no longer includes failed runs with partial sizes

## [0.1.0] - 2025-01-01

### Added in 0.1.0

- Initial release
- Docker container backup and restore (full image + config + appdata)
- VM backup and restore (live snapshot + cold backup)
- Storage destinations: Local, SMB, NFS, SFTP, NFS
- Full, incremental, and differential backup types
- Job scheduling with retention policies
- Web UI with Dashboard, Jobs, Restore, Storage, History, Settings
