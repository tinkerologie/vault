# Changelog

All notable changes to the Vault plugin will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project uses date-based versioning (`YYYY.MM.PATCH`).

## [Unreleased]

## [2026.05.01] - 2026-05-10

### Fixed

- **VM checkpoint creation now succeeds for cold (shut-off) backups.** Previously the engine called `DomainCheckpointCreateXML` against the persistent domain handle *after* `restoreDomainState()` had already destroyed the transient paused session — libvirt rejected the call with "Operation not supported: cannot create checkpoint for inactive domain", so every cold incremental/differential ran without ever recording a parent checkpoint and silently fell back to producing a full-size qcow2. Checkpoints are now created against the live (paused) `backupDom` while the transient backup session is still active, and only after the bitmap is persisted to qcow2 does the runner tear the session down. End-to-end verified against a cold Fedora VM: full = 2.3 GB, incremental and differential = 192 KB on a no-change cycle.
- **Engine no longer reports "incremental" / "differential" when no parent checkpoint is recorded.** When the runner had no checkpoint metadata to pass (first run after a chain reset), the engine still produced a full-size backup but labelled the result `vm_backup_type: incremental`, which corrupted the chain bookkeeping. The fall-back logic now also covers the empty-parent case and downgrades `actualType` to `"full"` with a clear log entry before writing `vm_meta.json` and `result.Meta`.
- **VM incremental backups now actually capture only changed blocks.** Previously every "incremental" or "differential" run silently produced a full copy of each qcow2 disk: the engine emitted a regular libvirt push-mode backup XML with no `<incremental>` element, and the only "incremental" behaviour was a file-mtime gate that either (a) re-copied the whole disk anyway when the VM was running (mtime always advanced) or (b) skipped the disk entirely when the VM was cold (mtime unchanged after the previous backup) — silently losing data. The engine now creates a libvirt **checkpoint** (persistent dirty bitmap stored in qcow2) after each successful backup and references it via `<incremental>parent_checkpoint</incremental>` on subsequent runs, so libvirt streams only the dirty blocks since the parent checkpoint. Restore points record the checkpoint name in `vm_checkpoints` metadata; missing-parent or non-qcow2 disks fall back to a full backup with a clear log entry. Retention cleanup deletes orphaned libvirt checkpoints. (closes the broken VM Incremental / Differential modes)
- **Backups failing with `No space left on device` when staging to `/mnt/addons` (1 MB tmpfs).** The pool-discovery cascade in `internal/unraid/pools.go` selected any directory that exists under `/mnt/`, which on modern Unraid includes the `/mnt/addons` tmpfs mount. Pool discovery now reads `/proc/self/mountinfo` and rejects paths backed by `tmpfs`, `devtmpfs`, `ramfs`, `proc`, `sysfs`, `cgroup`, `cgroup2`, or `overlay` filesystems, so staging falls through to a real cache or array pool.

### Added

- VM **chain restore** for incremental and differential backups. The runner now stages each chain step into its own subdirectory and uses `qemu-img rebase -u` plus `qemu-img convert -O qcow2` to materialise a single self-contained qcow2 per disk before invoking the existing libvirt restore path. The qemu-img invocation lives in the runner package to preserve the engine package's pure-Go invariant.
- `BackupResult.Meta` channel for engine-specific metadata. The VM engine populates it with `vm_checkpoint`, `vm_backup_type`, and (when applicable) `vm_parent_checkpoint`, which the runner persists into restore-point metadata so future backups can locate their parent checkpoint and retention can clean up libvirt-side state.
- VM `vm_meta.json` sidecar now records `BackupType`, `Checkpoint`, `ParentCheckpoint`, and per-disk `Disks[]` entries (`Target`, `BackupFile`, `Format`) for diagnostics and forward compatibility.

### Changed

- Removed the legacy mtime-based `changed_since` gate from the VM backup path. Folder and container backups continue to use it. ZFS keeps its dataset-snapshot-based incremental flow unchanged.

### Verified

- Hot-VM backups end-to-end on a running Fedora guest: Full = 2.34 GB / 34 s (`vault-run-6`), Incremental from prior run = 3.5 MB / 1 s (`vault-run-7`), Differential from last full = 4.07 MB / 1 s (`vault-run-8`). All three runs created persistent libvirt checkpoints on the live (paused) backup session.
- VM **chain restore** end-to-end: restored Fedora from incremental RP 7 (parent = full RP 6) using `qemu-img rebase -u` + `qemu-img convert`. The runner staged both layers, materialised a single 2.18 GiB self-contained qcow2, the engine redefined the domain and auto-started it, and the guest kernel booted successfully (balloon driver active, vCPUs accumulating CPU time).

## [2026.05.00] - 2026-05-10

### Added

- **Defer remote upload mode** for slow-link backups (closes #77). New per-job toggle "Defer remote upload until all backups complete" stages every container backup to local disk first, restarts all containers as soon as staging is done, then streams the staged archives to the remote destination in a second phase. Containers go offline only for the (fast) local backup window, while the (slow) remote upload runs while every service is back online. The toggle is disabled when the destination is local (no benefit) and is automatically gated behind a non-local `dest.Type`. Per-file uploads are now retried up to 3 times with exponential backoff (5s → 30s → 2m), recovering automatically from transient remote-storage failures that previously failed entire jobs. WebSocket `backup_phase`, `item_staged`, and `item_upload_start` events drive a STAGING / UPLOADING phase indicator in the Backup Progress panel. Schema migration adds a `defer_remote_upload` column to `jobs` (default off). v1 scope: containers only — VM/folder/plugin/zfs items continue to upload inline.
- WebDAV storage backend (closes #83). Stateless HTTP-based backup target that avoids per-user concurrent-connection limits seen on managed SFTP/SMB providers. Supports Basic and Digest auth (negotiated automatically), optional self-signed TLS, and an optional base path. Compatible with Nextcloud, ownCloud, Synology WebDAV, and any RFC 4918 server.
- S3 / S3-compatible storage backend (closes #88). Built on AWS SDK v2 with a reusable, connection-pooled client. Works with AWS S3, Backblaze B2, MinIO, Cloudflare R2, and Wasabi via configurable endpoint and force-path-style options. Uses the SDK's manager.Uploader for streaming uploads of arbitrarily large archives without buffering to disk.

### Changed

- Restore wizard "Choose Version" step, the Restore page restore-point list, the Job History timeline, the Dashboard Recent Activity panel, and the Logs page now show **absolute timestamps** (e.g. `Apr 21, 2026, 21:22`) as the primary label on each row, with the relative form (`48m ago`, `2d ago`) preserved as a hover tooltip. This matches how Veeam, restic, and Time Machine present restore points and aligns with established UX guidance: when the user is matching a backup to a remembered point in time ("this was working fine until 17 April"), an absolute date is far easier to scan than mentally subtracting a relative offset (closes #81)

### Fixed

- S3 storage adapter requests now correctly include the bucket name in the URL when a custom endpoint is configured (e.g., MinIO, Backblaze B2, Cloudflare R2, Wasabi). The custom `EndpointResolverV2` previously returned the bare endpoint URI, which suppressed the AWS SDK v2's automatic bucket injection — every PutObject / GetObject / HeadBucket request landed on the root path and the server responded with `NoSuchBucket`. The resolver now injects the bucket into the URL path (path-style) or hostname (virtual-host style) itself, matching what the SDK does for the default AWS resolver. Confirmed end-to-end against MinIO via the new `make test-cloud-storage` smoke test (closes #88)
- Add Storage modal: switching between storage types (SFTP/SMB/NFS → Local Path, etc.) now correctly tears down the previous type's fields. Previously, fields from the prior selection could persist after a type change due to Svelte 5 reusing DOM input nodes across the dynamic `{#if}` branches. Wrapped the dynamic config block in `{#key form.type}` so the entire field subtree is recreated on every type switch
- Pool drive detection no longer falsely reports "No cache drive detected" when `/mnt/cache` exists as an unmounted directory or when the user's pool is named something other than `cache`. `unraid.PreferredPool()` now consults `/proc/self/mountinfo` (via the existing `IsMountedPool()` helper) and returns the first **mounted** pool from discovery instead of returning `/mnt/cache` whenever the directory is present. `checkCacheMount()` likewise uses `IsMountedPool()` rather than the previous "directory non-empty = mounted" heuristic, so empty-but-mounted pools are correctly recognised as available for hybrid mode. Daemon startup also now logs every discovered pool with its mount status to make this diagnosable from `/var/log/syslog` (closes #69)
- New / Edit Job wizard no longer freezes on the second step when accessed over plain HTTP (e.g. `http://<server>:81/plugins/vault/ui/...`). The Tooltip component called `crypto.randomUUID()` unconditionally, but that API is only available in secure contexts (HTTPS or `localhost`); on plain HTTP, Firefox throws `TypeError: crypto.randomUUID is not a function`, which crashed every wizard step that mounts new tooltips (Schedule / Details / Advanced) while the stepper indicator still advanced. Tooltip now generates its fallback id with a `Math.random()`-based path when `crypto.randomUUID` is unavailable. The same fix also restores the SMB/CIFS Username/Password fields and the Settings page, which previously failed to render in Brave / Edge / Firefox over plain HTTP for the same reason. Closes #67, closes #82
- Importing backups from a storage destination now restores the full job and per-item configuration, not just the job name and encryption type. The on-disk `manifest.json` is now versioned (v2) and persists per-item rows (`name`, `type`, `id`, `settings`) plus job-level settings (`backup_type_chain`, `retention_count`, `retention_days`, `container_mode`, `vm_mode`, `notify_on`, `verify_backup`). The Storage → Import flow uses these fields to recreate Vault's `job_items` rows so the restore wizard immediately lists each backed-up container/VM/folder. For older manifests without an `items` array, item names are inferred from `item_sizes`; appdata.backup imports create a single container item per file. Closes #75
- Restore points, Dashboard Protection Status, and the Disaster Recovery plan no longer disappear when a job's schedule is disabled. Protection is now derived from whether items have actual restore points on disk, not from the `enabled` schedule flag — so disabling a schedule (e.g. for a one-off / manual-only backup workflow) keeps already-backed-up items visible and protected. Affects `GET /api/v1/health/summary` (`protected_items` / `protection_pct`), `GET /api/v1/recovery/plan`, and the Restore page job picker (closes #76)
- Container backups no longer fail with `archive/tar: sockets not supported` when a container has a Unix domain socket bind-mounted (e.g. Dockhand mounting `/var/run/docker.sock`). The engine now (a) auto-skips bind mounts whose source is a non-regular inode (socket, named pipe, device, irregular) with a clear `unsupported inode type` skip reason, and (b) honours user-defined Container Path Exclusions for **file**-based bind mounts using the same pattern semantics as directory mounts — exact paths, parent directories, basenames, and globs (`*docker.sock*`, `*.sock`) all work as documented in the UI hint. Previously the file-mount branch ignored the exclusion list entirely (closes #70)
- Stop and Restart buttons on Settings → Vault no longer fail with `Vault service control failed (HTTP 403)`. The plugin's `control.php` was performing a redundant CSRF check, but Unraid's web framework already validates `csrf_token` at the gateway and **strips it from `$_POST` before forwarding to PHP** — so the in-script comparison always saw an empty token and returned 403. The redundant check has been removed; the gateway CSRF validation plus the POST-method requirement still protect the endpoint (closes #71)
- Eliminated the dual-scrollbar layout bug on the Vault web UI Settings page (and any other tall page). `html`, `body`, and `#app` are now locked to the viewport height with `overflow: hidden`, so the SPA's `<main>` is the only scroll surface — the page's left navigation can no longer scroll out of view (closes #73)
- Service Control on the Vault settings page no longer falsely reports "STOPPED" when a Stop/Restart action fails. The Stop and Restart buttons now use a conservative fallback (preserve the pre-action state on errors), surface an inline error message when the request fails or the rc script exits non-zero, and confirm the real outcome by polling the daemon after the action. The PHP `is_running()` helper now consults `/api/v1/health` (the same authoritative check used at page load) and only falls back to the PID file when the health endpoint is unreachable, so post-action status agrees with what the user sees on refresh (closes #71)
- Plugin upgrades now stop the running Vault daemon before `upgradepkg --install-new` and restart it afterwards, so users get the new binary without needing to reboot Unraid. User configuration (`vault.cfg`), the database (`vault.db`), and sealed credentials under `/boot/config/plugins/vault/` are preserved across upgrades (closes #72)
- Settings and configuration are no longer lost on plugin upgrade. In hybrid mode the daemon now refreshes the USB safety-net (`/boot/config/plugins/vault/vault.db.backup`) immediately after a successful restoration at startup, so the recovery chain (configured snapshot → default cache snapshot → USB backup) always has a current entry — even if a previous daemon shutdown was unclean. Combined with the graceful pre-upgrade stop, this guarantees a fresh-database fallback can no longer happen silently during an upgrade (closes #74)
- **Storage adapter durability:** `LocalAdapter`, `SFTPAdapter`, and `SMBAdapter` `Write()` now perform a checked `Sync()` (where supported) plus a checked `Close()` and remove the partial file on any error, instead of relying on a `defer f.Close()` whose error was swallowed. Previously a write that failed mid-stream — or a final `Close()` that returned an error from a buffered remote SMB/SFTP server — would leave a truncated file on the destination and report success to the caller.
- **Database restore endpoint hardening:** `POST /api/v1/storage/{id}/restore-db` now validates `storage_path`, rejecting empty values and any payload containing `..`. The handler also propagates errors from closing the live DB, the source temp file, and the destination DB file — and now `Sync()`s the destination before declaring success, removing the partial file on any failure. Previously these errors were silently discarded.
- **Health score rounding:** `GET /api/v1/health/summary` now rounds the weighted health score to the nearest integer instead of truncating, so a 99.6% score displays as 100 rather than 99.
- **Job history input validation:** `GET /api/v1/jobs/{id}/history` now returns HTTP 400 when `limit` is non-numeric or less than 1 instead of silently falling back to the default of 50.
- **Frontend listener registration:** the hash-based router and theme module now guard against duplicate `hashchange` and `prefers-color-scheme` listeners under hot module reload, eliminating dev-mode listener leaks.
- **SFTP port input validation:** the Storage modal `port` field now enforces `min=1`/`max=65535`, preventing accidental zero or negative values being stored.
- **Command palette error visibility:** `CommandPalette.svelte` now logs `listJobs` / `listStorage` / `runJob` / `testStorage` failures via `console.error` instead of swallowing them silently, making palette malfunctions diagnosable from devtools.
- **S3 List timeout consistency:** `S3Adapter.List()` now uses the shared `ctxOp()` helper instead of a divergent hardcoded 10-minute timeout, matching every other S3 operation.
- **Request logger guidance:** added an explicit comment in `internal/api/middleware.go` enumerating which authorization headers must be redacted if header logging is ever added (`Authorization`, `X-API-Key`, `Cookie`, `Set-Cookie`, `Proxy-Authorization`), to prevent accidental credential leaks in future changes.
- **Emergency-kit download race:** `Settings.svelte` now defers `URL.revokeObjectURL()` for one second after `a.click()` so slow disks/browsers don't lose the blob URL before the download starts.
- **Missing dynamix.cfg log warning:** `detectTimeFormatFromPath()` now logs a warning when reading `/boot/config/plugins/dynamix/dynamix.cfg` returns an unexpected error (anything other than "file not found"), so silent fallbacks to `auto` are no longer invisible.

## [2026.04.00] - 2026-04-16

### Added

- "Apply" button on the Temporary Work Area custom location input for consistency with the Database Location pattern
- GitHub Sponsors link in the About Vault section on the Settings page
- CORS middleware (`go-chi/cors`) restricting cross-origin requests to `*.myunraid.net`, `localhost`, and `127.0.0.1` origins only (OWASP A01)
- IP-based rate limiting (`go-chi/httprate`) on auth-sensitive endpoints: encryption verify (10 req/min), API key generate/rotate (5 req/min) (OWASP A05/A07)
- Auto-seal migration on daemon startup: legacy plaintext `encryption_passphrase` values are automatically sealed with the server key and the plaintext is cleared (OWASP A02)
- `Cache-Control: no-store` header on `GET /api/v1/settings/encryption/passphrase` to prevent caching of sensitive passphrase responses (OWASP A02)
- Remote Vault API Key field on the Add/Edit Replication Target form — allows users to enter the shared API key for authenticating with a remote Vault server during replication sync
- Replication sync and test-connection now use the configured API key (via `X-API-Key` header) when connecting to authenticated remote Vault instances
- Test Connection in the replication modal now always performs a live connectivity check against the remote Vault server, verifying the URL is reachable and the server is healthy
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

### Changed

- Storage form: added tooltips on SFTP Remote Path, SMB Share, NFS Export Path, and NFS Base Path to clarify the distinction between fields that look similar (e.g. export vs sub-path within mount)
- Replication target form: API key field is now required (marked with asterisk, form submit blocked without it); "Test Connection" is blocked if no API key is entered; warning callout directs users to generate a key on the remote server under Settings → API Access
- Removed the "Target Type" dropdown from the Replication target form — only "Remote Vault Server" is supported
- Removed the Fallback Locations collapsible section from the Temporary Work Area on the Settings page to reduce clutter
- Removed the WebSocket status row and Reconnect button from Server Information on the Settings page
- Removed the WebSocket/Polling status indicator from the sidebar footer
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
- Vault database backup now writes to a centralized `_vault/vault.db` path at the storage root instead of inside each job run directory, eliminating duplicate database copies across backup jobs
- Import Backups "Restore Full Database" section now shows a single "Vault Database" entry with the backup date instead of listing individual job names

### Removed

- Removed Google Drive and OneDrive replication support — only Remote Vault Server replication is now supported
- Removed OAuth infrastructure (client credentials, build-time ldflags, callback handlers) for cloud storage providers

### Fixed

- Fixed job creation wizard step indicator scrolling out of view on tall steps (e.g. step 3 with many schedule and storage fields) — the step indicator is now pinned in a non-scrolling band below the modal title, always visible regardless of scroll position
- Fixed path browser breadcrumbs showing double slashes (e.g. `//mnt` instead of `/mnt`) when browsing server paths for storage and database locations
- Added per-restore-point deletion: each restore point in the Restore wizard now has a trash button (two-click confirm) that deletes both the backup files from storage and the database record — closes the user request for "delete a backup without deleting the job"
- Added subfolder field to the Import Backups modal on the Storage page — lets users point the scanner at a specific subdirectory when their AppData Backup archives are not at the storage root (e.g. `appdata-backups/`)
- Fixed AppData Backup flash-backup detection to match any `<hostname>-<date>.zip` file instead of only `cube-*.zip` — works for systems named tower, unraid, or any other hostname
- Fixed Backup Size Trend chart showing wildly incorrect percentages (e.g. +1120%) when multiple jobs are visible — switched from last-point comparison to linear regression; mixed-job views now show a directional label (Growing/Shrinking/Stable) instead of a misleading number, while single-job filtered views still show the exact percentage
- Verified sequential job execution: 3 jobs queued simultaneously (Jackett, Mosquitto/Tailscale, Fedora VM) all completed in sequence without stalling — the reported "stops after 2 backups" issue does not reproduce on the current codebase
- Fixed missing `getAPIKeyStatus`, `generateAPIKey`, and `revokeAPIKey` methods in the API client (`api.js`), which caused a console error on every Settings page load and always showed "No API key configured" regardless of actual state
- Fixed undefined `bg-accent`/`text-accent` Tailwind v4 tokens on the "backup" type badge in History run rows — replaced with `bg-vault/15 text-vault` so the badge is now visible in both light and dark modes
- Added `aria-pressed` state to all filter/segmented-control buttons in History and Logs pages, and wrapped filter groups in `role="group"` with descriptive `aria-label` for screen-reader usability
- Added `aria-current="page"` to sidebar navigation and mobile bottom-nav buttons so the active page is announced by screen readers
- Added `aria-label` to the search input on the History page
- Changed mobile bottom-nav "More" button icon from a gear to a three-dots ellipsis, matching the standard convention for overflow menus
- Added an active indicator dot beneath the current page icon in the mobile bottom navigation bar
- Scoped the `html` theme transition (`background-color`/`color`) inside `@media (prefers-reduced-motion: no-preference)` to respect the OS reduced-motion accessibility setting
- Added scroll-fade gradient hint on mobile Dashboard stats row to indicate horizontal scrollability
- Replaced non-standard checkbox in Logs page auto-scroll control with a proper `role="switch"` toggle button matching the design pattern used throughout the Settings page
- Removed duplicate action buttons on Jobs, Storage, and Replication pages — top-right header button now only appears when items exist, eliminating redundancy with the empty-state center button
- Fixed nil pointer dereference on startup: `BrowseHandler` was not assigned to the server struct in `setupRoutes`, causing a panic when `SetZFSLister` was called
- Fixed `sync.Mutex` deadlock in runner: `RunJob` already holds `r.mu`, removed redundant lock around `snapshotManager` access in snapshot save
- Clipboard copy in Settings UI now handles errors with an error toast instead of silently failing
- Middleware test `SetSetting` calls now check for errors to prevent misleading test results
- API error responses no longer leak internal error details to clients; all 500 responses now return a generic "internal server error" message while the real error is logged server-side (OWASP A09)
- SMB storage adapter now enforces a 30-second dial timeout via `context.WithTimeout` to prevent indefinite connection hangs
- SFTP adapter logs a warning when falling back to `InsecureIgnoreHostKey` due to missing host key verification configuration
- Runner `SetSnapshotManager` write is now protected by mutex to prevent a data race with concurrent job execution
- SMB `smbReadCloser.Close()` now uses `errors.Join` to surface file/share/session close failures instead of silently discarding them
- NFS adapter `unmount()` now logs errors from `umount` and temp directory removal instead of silently discarding them
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
