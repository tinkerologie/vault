# Changelog

All notable changes to the Vault plugin will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project uses date-based versioning (`YYYY.MM.PATCH`).

## [Unreleased]

### Added

- Purge activity logs: `DELETE /api/v1/activity` endpoint and "Purge" button on the Logs page with confirmation dialog to permanently delete all activity log entries (closes #32)
- Purge job run history: `DELETE /api/v1/history` endpoint and "Purge" button on the History page with confirmation dialog to permanently delete all job run records (closes #32)
- `PurgeJobRuns()` database method for bulk deletion of job run history; activity log purge reuses `DeleteOldActivityLogs(0)` to clear all entries
- Job run history purge actions are logged in the activity log with the count of deleted records
- Cancel API endpoint `POST /api/v1/jobs/{id}/cancel` to abort a running backup job (closes #28)
- Cancellable context propagated through the entire backup pipeline: Runner → engine handlers → tar/copy I/O operations
- 4-hour job timeout with automatic cancellation via `context.WithTimeout`
- Stall detection: warns after 30 minutes of no progress, auto-cancels after 2 hours of inactivity
- `cancelling` field added to runner status for real-time UI feedback
- `job_cancelling` WebSocket event broadcast when cancellation is requested
- "cancelled" job run status with descriptive log messages (user-initiated vs timeout)
- Context-aware `contextCopy` helper that checks for cancellation every 32 KiB during file I/O
- `ctx.Err()` checks in `filepath.Walk` callbacks to abort directory traversal on cancellation
- Backup target category toggles in Settings → General: independently enable/disable tracking for Containers, Virtual Machines, and Flash Drive; disabled categories are excluded from protection status on the Dashboard and readiness metrics on the Recovery page (closes #20)
- Three new settings keys (`container_backup_enabled`, `vm_backup_enabled`, `flash_backup_enabled`) with `"true"` defaults in the settings API
- Monthly and yearly scheduling now support "First day of month" and "Last day of month" options in the schedule builder UI; last-day jobs use a daily-check pattern on the backend with an `isLastDayOfMonth()` guard so they fire correctly on months of any length (closes #15)
- Unraid display time format is now detected from `dynamix.cfg` and injected into the runtime config, allowing the UI to honour the user's 12-hour or 24-hour preference
- Go daemon (direct-access mode) now injects `window.__VAULT_RUNTIME_CONFIG__` into the SPA HTML, ensuring time format detection works when accessing Vault directly on port 24085 without the PHP proxy
- `getTimeFormat()` and `getHour12()` helpers added to `runtime-config.js` for locale-aware time rendering
- `formatDate()` utility now used consistently for all date/time display in the Storage and Settings pages

### Fixed

- Container path exclusion presets now load correctly when Vault runs behind the Unraid web proxy; `fetchContainerPresets()` uses `buildApiRequest()` instead of raw `fetch()` to route through the authenticated proxy endpoint (closes #11)
- Stuck backup jobs can no longer run indefinitely — timeout and stall detection ensure jobs are always bounded (closes #28)
- Time format detection now falls back to `[notify][time]` in `dynamix.cfg` when `[display][time]` is absent, fixing detection on Unraid 7.x where the time format preference is stored in the notification settings section
- Unraid Settings/Vault page was blank due to duplicated PHP code in `api.php` causing a syntax error; removed the corrupted duplicate block to restore the service control panel, Web UI button, and port/binding configuration
- SMB and SFTP storage adapters now honour the "Path" field: frontend forms send `base_path` matching the backend struct, and adapters accept the legacy `path` JSON key as a fallback for backward compatibility (closes #25)
- Job deletion with "Delete Backup Files" now properly removes empty directories after deleting their contents, fixing the issue where backup files and directories were left on Local and SMB storage (closes #26)
- SMB adapter `Write()` now propagates `MkdirAll` errors instead of silently ignoring them
- `ItemPicker` selected-items map wrapped in `$state()` to ensure Svelte 5 reactive tracking (closes #22)
- Items deleted from Unraid (containers, VMs, folders, plugins) can now be removed from backup jobs via the new remove button in the Backup Order list; stale items that no longer exist on the system are visually flagged with a "Not found" warning indicator (closes #24)
- Storage form "Save" button now guards against double-submission with a `saving` flag and shows a "Saving…" state while the request is in flight
- Container volume backups now skip Unix sockets, character/block devices, and named pipes instead of failing with "sockets not supported" errors; affected containers (e.g. those mounting `/var/run/docker.sock`) will complete successfully with a log entry for each skipped special file (closes #5)
- Monthly schedule day picker now shows all 31 days instead of only days 1–28; previously `Array(27)` omitted days 29, 30, and 31 (closes #9)

### Removed

- API Access feature completely removed from Security settings — API key generation, rotation, revocation, and status endpoints are no longer available
- `X-API-Key` authentication header and `APIKeyAuth` middleware removed; the daemon no longer requires or accepts API keys
- API key field removed from Replication targets — remote sources connect without authentication
- `--api-key` CLI flag and `VAULT_API_KEY` environment variable removed from `daemon` and `replica` commands
- `/auth/status`, `/settings/api-key/generate`, `/settings/api-key/rotate`, `/settings/api-key/revoke`, `/settings/api-key` endpoints removed
- `api_key` column removed from `replication_sources` database schema
- `LoginPrompt.svelte` component deleted (unused)

### Changed

- `engine.Handler` interface now accepts `context.Context` as the first parameter for `Backup()` and `Restore()`
- All engine handlers (Container, VM, Folder, Plugin) updated to accept and propagate context
- `Runner.backupItem()` now receives and passes context to engine handlers

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
