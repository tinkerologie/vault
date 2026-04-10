# Changelog

All notable changes to the Vault plugin will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project uses date-based versioning (`YYYY.MM.PATCH`).

## [Unreleased]

### Fixed

- API key authentication middleware with loopback exemption: non-localhost requests require a valid `X-API-Key` header when an API key is configured; localhost and Unraid PHP proxy connections are always exempt
- API key middleware now fails closed on database errors instead of allowing unauthenticated access
- OAuth callback routes (`/gdrive/callback`, `/onedrive/callback`) are exempt from API key authentication since browser redirects cannot carry custom headers
- `ReplicationSource.APIKey` is no longer exposed in JSON API responses (`json:"-"`)
- API key sealed value is written before the hash to prevent partial-failure lockout; hash write failure triggers rollback of the sealed value
- `api_key_hash` and `api_key_sealed` are redacted from `GET /settings` responses alongside encryption settings
- `GET /settings/api-key/reveal` response includes `Cache-Control: no-store` to prevent sensitive key caching
- "Server key not configured" error responses on API key handlers now use generic 500 via `respondInternalError` instead of leaking internal state
- Fixed `sync.Mutex` deadlock in runner: `RunJob` already holds `r.mu`, removed redundant lock around `snapshotManager` access in snapshot save
- Clipboard copy in Settings UI now handles errors with an error toast instead of silently failing
- Middleware test `SetSetting` calls now check for errors to prevent misleading test results
- API error responses no longer leak internal error details to clients; all 500 responses now return a generic "internal server error" message while the real error is logged server-side (OWASP A09)
- SMB storage adapter now enforces a 30-second dial timeout via `context.WithTimeout` to prevent indefinite connection hangs
- SFTP adapter logs a warning when falling back to `InsecureIgnoreHostKey` due to missing host key verification configuration
- OAuth callback templates (Google Drive, OneDrive) restrict `postMessage` target origin from wildcard `*` to `window.location.origin`
- Runner `SetSnapshotManager` write is now protected by mutex to prevent a data race with concurrent job execution
- SMB `smbReadCloser.Close()` now uses `errors.Join` to surface file/share/session close failures instead of silently discarding them
- NFS adapter `unmount()` now logs errors from `umount` and temp directory removal instead of silently discarding them

### Added

- API key management: generate, reveal, rotate, copy, and revoke a shared API key from Settings > Security for authenticating external integrations (Home Assistant, replication) — key is stored sealed (AES-256-GCM) and verified via bcrypt
- Settings > Security > API Access card showing key status, reveal/copy, rotate, and revoke controls with confirmation dialog
- `X-API-Key` header support in the replication client for authenticated cross-server sync
- `api_key` column on `replication_sources` table for storing per-source API keys
- ZFS zpool support for database location: the path browser now includes ZFS pool mountpoints when browsing for custom database snapshot locations via `include_zfs` query parameter (closes #50)
- ZFS zpool support for temporary work area: NVMe-backed ZFS zpools are automatically detected at daemon startup and prepended to the staging cascade, giving them the highest priority for backup assembly (closes #51)
- `ListNVMePools()` method on `ZFSHandler` to discover zpools composed entirely of NVMe devices
- `ListZFSMountpoints()` method on `ZFSHandler` to enumerate all accessible ZFS dataset mountpoints
- `PrependCachePaths()` function in `tempdir` package to inject high-priority staging paths at runtime
- `BrowseHandler.SetZFSLister()` for pluggable ZFS mountpoint discovery in the browse API
- Updated Settings page text to mention ZFS zpools as available locations for database and staging
- `internal/unraid` package with `DiscoverPools()`, `PreferredPool()`, and `IsMountedPool()` for dynamic Unraid pool detection — replaces hardcoded `/mnt/cache` references across the codebase (closes #49)
- Contextual tooltips across Settings, Jobs, Storage, and Replication pages — reusable `Tooltip.svelte` component with hover/click-to-toggle, viewport-aware positioning, keyboard dismissal, and full ARIA accessibility (closes #34)
- Enriched activity logs with contextual details for troubleshooting: backup started/completed and restore completed entries now include job name, backup type, storage destination, duration, and size; per-item container health check results are logged individually under a new "health" category; stop_all health check summary includes aggregate counts (containers checked/healthy/unhealthy) (closes #30)
- "Health" category filter on the Logs page to isolate container health check entries
- Smart formatting for activity log detail badges: backup types are capitalised, durations show unit suffixes, byte sizes are human-readable (e.g. 2.2 GB), and null values are hidden
- Diagnostic bundle download: `GET /api/v1/settings/diagnostics` endpoint and "Download diagnostics bundle" button on the Settings page generates a ZIP containing system info, database details, storage destinations, job configurations, recent run history, and activity logs with a unique correlation ID for support workflows (closes #29)
- `internal/diagnostics` package with collector, ZIP packager, and comprehensive redaction for sensitive data (passwords, API keys, tokens, webhook secrets, inline URL credentials)
- `ListRecentRuns(limit)` database method for fetching recent job runs across all jobs
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

- Mirrored SSD cache pools (e.g. `/mnt/cache2`, `/mnt/cache3`) not detected under Settings → Database Location and Temporary Work Area — pool discovery now scans `/mnt/` at runtime using exclusion-based filtering (closes #49)
- Browse handler filesystem roots now dynamically discover all pool drives instead of relying on a hardcoded "Cache" entry
- Path traversal vulnerability (CWE-22) in `SnapshotManager` — added `validateSnapshotPath` defense-in-depth validation to `SaveSnapshot`, `SetSnapshotPath`, `RestoreFromSnapshot`, `RestoreFromPath`, `SetUSBBackupPath`, and `saveUSBBackup` with `..` component rejection before `filepath.Clean` + `filepath.Abs` normalisation; uses `filepath.ToSlash` for cross-platform traversal detection (closes #27, closes #28)
- Data race in `SaveSnapshot` reading `snapshotPath` without mutex protection — now reads the field under lock consistently with other accessors
- Diagnostics collector hybrid-mode detection now checks that the preferred pool is mounted (matching daemon startup behaviour) instead of only checking directory existence
- CSRF token validation added to `control.php` for state-changing actions (start, stop, restart, reset-config) — token sourced exclusively from POST
- IPv6 loopback (`::1`) bind address now connects via `[::1]` instead of `127.0.0.1` in the PHP proxy, fixing connectivity when the daemon binds exclusively to IPv6
- Bind-address validation in `apply.sh` and `rc.vault` now uses `grep -F` (fixed-string) to prevent regex wildcard matching of IPv4 dots, and `apply.sh` accepts IPv6 loopback/wildcard (`::1`, `::`)
- `apply.sh` no longer sources the config file directly — safely extracts only the `BIND_ADDRESS` key via grep/sed to prevent arbitrary code execution from user-editable config
- `apply.sh` now checks `sed -i` exit status and aborts with an error if the config update fails
- INI sanitisation in `control.php` now strips backslashes in addition to quotes and newlines, preventing backslash-escape attacks on INI quoting
- Tooltip clipping when positioned near viewport edges — switched from `position: absolute` to `position: fixed` with JS-calculated viewport coordinates and horizontal clamping
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

### Changed

- Renamed "Staging Directory" section to "Temporary Work Area" with descriptive subtitle explaining its purpose (closes #13)
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
