# API Reference

Base URL: `http://<host>:24085/api/v1`

The Vault daemon exposes a REST API for managing backups, storage destinations, and system settings. WebSocket events are available for real-time progress streaming.

## Authentication

By default Vault binds to `127.0.0.1` and all loopback requests are unauthenticated. When you configure an API key under **Settings → Security → API Access**, remote requests (non-loopback IPs) must include the key in every request:

```http
X-API-Key: <your-api-key>
```

Loopback requests (`127.0.0.1` and `::1`) are always exempt from API key validation so that the Unraid plugin proxy can reach the daemon without a key.

**Generating a key:** Go to **Settings → Security → API Access** → **Generate Key**. Copy and store it securely — it is shown only once. Use this key for Home Assistant, replication targets, and any other external integrations.

## Core

| Method | Endpoint             | Description                                                             |
| ------ | -------------------- | ----------------------------------------------------------------------- |
| GET    | `/health`            | Basic health, version, and mode                                         |
| GET    | `/health/summary`    | Aggregated dashboard health metrics                                     |
| GET    | `/ws`                | WebSocket event stream                                                  |
| GET    | `/runner/status`     | Current backup or restore state, queued jobs, and [progress](#progress) |
| GET    | `/release/changelog` | Embedded `CHANGELOG.md` parsed into version blocks (drives About modal) |
| GET    | `/release/latest`    | Latest GitHub release metadata (used by the About card update badge)    |

## Jobs

The job payload's `schedule`, `verify_schedule` and `full_backup_schedule` fields accept standard 5-field cron expressions (`minute hour day-of-month month day-of-week`). An empty `schedule` means manual-only for backups — the scheduler skips regular runs entirely. Similarly, an empty `verify_schedule` or `full_backup_schedule` disables that secondary schedule. Arbitrary expressions are accepted, including steps, ranges and lists, along with the `@daily`-style descriptors and Vault's `L` day-of-month token (last day of the month). An expression the scheduler cannot parse is rejected at create/update time rather than stored and silently never run.

`full_backup_schedule` forces a **full** backup on its own cadence, independently of `schedule`, so an `incremental` or `differential` job carries its periodic full without a duplicate job. It is ignored — and cleared on save — when `backup_type_chain` is `full`, because every run is already a full.

The job payload's `backup_type_chain` field accepts `full`, `incremental`, or `differential`. Incremental and differential jobs automatically run as a **full** backup on their first run (when the job has no previous restore point to attach to); later runs capture only changes.

| Method | Endpoint                                           | Description                                                                    |
| ------ | -------------------------------------------------- | ------------------------------------------------------------------------------ |
| GET    | `/jobs`                                            | List jobs                                                                      |
| POST   | `/jobs`                                            | Create job (response echoes the persisted items)                               |
| GET    | `/jobs/next-runs`                                  | Next scheduled run for every job                                               |
| GET    | `/jobs/{id}`                                       | Get a job and its items                                                        |
| PUT    | `/jobs/{id}`                                       | Update a job (response echoes the persisted items)                             |
| DELETE | `/jobs/{id}`                                       | Delete a job                                                                   |
| GET    | `/jobs/{id}/history`                               | Job run history (returns `[]` when empty)                                      |
| GET    | `/jobs/{id}/restore-points`                        | Restore points with chain-health annotations (returns `[]` when empty)         |
| GET    | `/jobs/{id}/retention-preview`                     | Preview which restore points the configured retention policy would prune       |
| DELETE | `/jobs/{id}/restore-points/{rpid}`                 | Delete a specific restore point and its files                                  |
| GET    | `/jobs/{id}/restore-points/{rpid}/contents?item=…` | List files inside a restore point (works for classic and dedup restore points) |
| POST   | `/jobs/{id}/restore-points/{rpid}/verify`          | Trigger an on-demand SHA-256 verification of a restore point                   |
| GET    | `/jobs/{id}/restore-points/{rpid}/verify-runs`     | List verification runs for a restore point                                     |
| GET    | `/jobs/{id}/verify-runs/{vrid}`                    | Get a single verification run record                                           |
| POST   | `/jobs/{id}/run`                                   | Trigger an immediate backup                                                    |
| POST   | `/jobs/{id}/cancel`                                | Cancel a running backup job                                                    |
| POST   | `/jobs/{id}/restore`                               | Trigger a restore                                                              |
| GET    | `/jobs/{id}/next-run`                              | Next scheduled run for one job                                                 |

## Storage

| Method | Endpoint                       | Description                                                                                                        |
| ------ | ------------------------------ | ------------------------------------------------------------------------------------------------------------------ |
| GET    | `/storage`                     | List storage destinations (credentials redacted in the response)                                                   |
| POST   | `/storage`                     | Create storage destination (config validated; response is redacted)                                                |
| GET    | `/storage/{id}`                | Get storage destination (redacted)                                                                                 |
| PUT    | `/storage/{id}`                | Partial-merge update; rejects mutation of immutable fields (`type`, `dedup_enabled`)                               |
| DELETE | `/storage/{id}`                | Delete storage destination; `?force=true` drops dependent-job guard; `?deleteFiles=true` also removes backup files |
| POST   | `/storage/{id}/test`           | Test storage connection                                                                                            |
| POST   | `/storage/{id}/health-check`   | Run the storage health probe (used by the Dashboard health widget)                                                 |
| POST   | `/storage/{id}/capacity-check` | Refresh used / total / free capacity (broadcasts `storage_capacity_updated` over WS)                               |
| POST   | `/storage/{id}/breaker/close`  | Manually close the destination circuit breaker — clears sticky failure state                                       |
| POST   | `/storage/{id}/scan-orphans`   | Scan storage for orphaned files (no matching restore point)                                                        |
| POST   | `/storage/{id}/delete-orphans` | Delete files surfaced by `scan-orphans`                                                                            |
| GET    | `/storage/{id}/dedup-stats`    | Dedup repo statistics (chunks, packs, logical/physical bytes, dedup ratio)                                         |
| POST   | `/storage/{id}/gc`             | Run mark-and-sweep GC on the destination's dedup repo                                                              |
| POST   | `/storage/{id}/scan`           | Scan storage for importable backups                                                                                |
| POST   | `/storage/{id}/import`         | Import backups discovered during scan (preserves dedup manifest IDs)                                               |
| GET    | `/storage/{id}/db-backups`     | List Vault database backups found under `_vault/` on the destination (newest first)                                |
| POST   | `/storage/{id}/restore-db`     | Restore the Vault database from a `_vault/` backup — see body fields below                                         |
| POST   | `/storage/dedup-keys/rewrap`   | After a recovery, re-seal dedup destinations with this server's key using the backup passphrase — see below        |
| GET    | `/storage/{id}/jobs`           | Returns `{ jobs: [{id, name}], job_count: N }` — dependent-job list                                                |
| GET    | `/storage/{id}/list`           | List files under a storage path                                                                                    |
| GET    | `/storage/{id}/files`          | Download a file from storage                                                                                       |

## Settings

| Method | Endpoint                          | Description                                                                                          |
| ------ | --------------------------------- | ---------------------------------------------------------------------------------------------------- |
| GET    | `/settings`                       | List settings                                                                                        |
| PUT    | `/settings`                       | Update settings                                                                                      |
| GET    | `/settings/encryption`            | Encryption status                                                                                    |
| POST   | `/settings/encryption`            | Set encryption passphrase                                                                            |
| POST   | `/settings/encryption/verify`     | Verify encryption passphrase                                                                         |
| GET    | `/settings/encryption/passphrase` | Read the configured passphrase (`Cache-Control: no-store`)                                           |
| GET    | `/settings/server-key`            | Download `vault.key`, the raw 32-byte server key (`Cache-Control: no-store`, rate limited)           |
| GET    | `/settings/staging`               | Staging directory info                                                                               |
| PUT    | `/settings/staging`               | Override the staging directory                                                                       |
| GET    | `/settings/database`              | Database snapshot settings                                                                           |
| PUT    | `/settings/database`              | Update database snapshot settings                                                                    |
| POST   | `/settings/discord/test`          | Test the Discord webhook                                                                             |
| GET    | `/settings/api-key`               | API key status (configured / not configured)                                                         |
| GET    | `/settings/api-key/key`           | Read the current API key (loopback callers only)                                                     |
| POST   | `/settings/api-key/generate`      | Generate a new API key (rate-limited)                                                                |
| POST   | `/settings/api-key/rotate`        | Rotate the existing API key (rate-limited)                                                           |
| DELETE | `/settings/api-key`               | Revoke the API key                                                                                   |
| GET    | `/settings/diagnostics`           | Download a diagnostics ZIP (system info, schema, runs, scheduler, daemon log — credentials redacted) |

## Discovery

| Method | Endpoint              | Description                                                                                                                                   |
| ------ | --------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| GET    | `/browse?path=…`      | Browse filesystem paths (safepath-gated; allowed under `/mnt` etc.)                                                                           |
| GET    | `/path-exists?path=…` | Safepath-gated `os.Stat` (used by the Folder picker for staleness)                                                                            |
| GET    | `/containers`         | Discover Docker containers                                                                                                                    |
| GET    | `/vms`                | Discover libvirt VMs; each item's `settings` includes `disk_format` (`qcow2`/`raw`/`mixed`/`unknown`) and `supports_incremental` (qcow2-only) |
| GET    | `/folders`            | Discover folder presets (engine-known paths)                                                                                                  |
| GET    | `/plugins`            | Discover installed Unraid plugins                                                                                                             |
| GET    | `/zfs`                | Discover ZFS datasets                                                                                                                         |
| GET    | `/presets/exclusions` | Per-container exclusion presets (`paths`), plus advisory `notes`/`warnings` for some apps (e.g. Immich DB)                                    |

## Activity Logs

| Method | Endpoint                                | Description                                                                                                                                                                                                               |
| ------ | --------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| GET    | `/activity?limit=N`                     | Activity log entries (`limit` is clamped to ≤ 1000 to prevent OOM)                                                                                                                                                        |
| DELETE | `/activity`                             | Purge all activity log entries (irreversible)                                                                                                                                                                             |
| GET    | `/runs/{runId}/logs?after=<id>&limit=N` | Streaming log entries for one backup/restore run, oldest-first. `?after=<id>` tails only newer lines; `?limit=N` clamped to [1, 1000]. Unknown run → 404; invalid run id or negative `after` / non-positive `limit` → 400 |

Run log durations keep sub-second precision: messages show `<1s`, `2.4s`, or `1m5s`, and the structured `data` carries `duration_ms` beside the existing `duration_seconds`. A transfer rate is only reported for operations that take at least one second. A rate also needs a positive `size_bytes`. A run of at least one second that moved data sets `data.throughput_bytes_per_sec`. A shorter run that moved data says `throughput not calculated: duration below 1.0s` and sets `data.throughput_not_calculated`. A zero-byte run omits both fields. The web UI and Discord notifications apply the same one-second rule.

Each backup and restore run log also records:

- **Run details**: one line with the job name and ID, run ID, destination name and storage type, item count, and (restores) the restore point ID.
- **Phase lines**: one line per stage per item, for example `Phase upload completed for appdata in 4m12s`, with `data.phase`, `data.item_name`, `data.duration_ms`, and byte fields where measured. Backup stages are `stage`, `upload`, `dedup store`, and `verify`; restore stages are `download` and `extract` (classic) or `open repository` and `restore` (deduplicated). A failed stage logs `Phase <name> failed for <item> after <duration>` at error level, which shows where the operation stopped.
- **Retries**: each recovered storage failure (a retried request, a resumed stream, or a restarted download part) is a warning line naming the operation, the object's file name, and the attempt, for example `Transient storage error on ReadRange data.tar.zst (attempt 2/5), retrying: connection reset`. Error text is redacted: credentials are masked and URLs are cut to scheme and host. After 20 such lines a single "further storage retry events suppressed" notice is written; the totals are still counted.
- **Measured bytes**, each under its own field: `stored_bytes` (what landed on the destination), `bytes_uploaded` (including retried attempts), `logical_bytes` and `duplicate_chunks` (deduplicated backups), `bytes_read` (read back from storage), `files_restored`, `retries`, and `resumes`. Verification lines include the bytes read and the time taken.
- **Summary totals**: `read=`, `retries=`, and `transient_errors=` are appended to the final line when storage was read or retried. Restore summaries also name the job and destination.

A restore of selected files reports the size it actually wrote (`size_source: "restored"`). A whole-item restore keeps reporting the size recorded at backup time (`size_source: "recorded item size"`).

## History

| Method | Endpoint   | Description                                      |
| ------ | ---------- | ------------------------------------------------ |
| DELETE | `/history` | Purge all job run history records (irreversible) |

> Per-job history is available at `GET /jobs/{id}/history`. There is no global `GET /history` endpoint; query individual jobs and merge client-side.

## Replication

A replication source's `schedule` field takes the same cron expressions as a job's (see [Jobs](#jobs)); an empty string means the source is only synced on demand.

Replication is pull-based: a sync imports only the remote jobs whose `source_id` is `0`, i.e. jobs created on that server. Jobs the remote server itself replicated from elsewhere are skipped, so two servers can each configure the other as a source without bouncing jobs back and forth. Multi-hop relays (A → B → C) are not forwarded; configure each origin server as a direct source.

| Method | Endpoint                 | Description                     |
| ------ | ------------------------ | ------------------------------- |
| GET    | `/replication`           | List replication sources        |
| POST   | `/replication`           | Create replication source       |
| POST   | `/replication/test-url`  | Test a replication URL          |
| GET    | `/replication/{id}`      | Get replication source          |
| PUT    | `/replication/{id}`      | Update replication source       |
| DELETE | `/replication/{id}`      | Delete replication source       |
| POST   | `/replication/{id}/test` | Test replication connection     |
| POST   | `/replication/{id}/sync` | Trigger replication immediately |
| GET    | `/replication/{id}/jobs` | List replicated jobs            |

## Recovery

| Method | Endpoint               | Description                                                                                                  |
| ------ | ---------------------- | ------------------------------------------------------------------------------------------------------------ |
| GET    | `/recovery/plan`       | Recovery plan with per-step item status; step downgrades to `warning` if any item is missing a restore point |
| GET    | `/recovery/path-audit` | List job/item paths that don't exist on this server (used by the Recover Vault wizard's path check)          |
| POST   | `/recovery/path-remap` | Rewrite audited paths to new locations (e.g. after a share or disk was renamed)                              |

### Restoring the database (`restore-db`)

`POST /storage/{id}/restore-db` takes a JSON body:

- `storage_path` — path of the database backup on the destination, e.g. `_vault/vault.db.latest.age` (encrypted) or a timestamped copy like `_vault/vault.db.2026-07-01T02-00-00.age`. Unencrypted backups use the `.db` extension.
- `passphrase` — the backup password; required when the backup is encrypted.
- `verify_only` — when `true`, validates the backup (and passphrase) without replacing the current database.

List the available backups first with `GET /storage/{id}/db-backups`.

### Unlocking dedup destinations (`dedup-keys/rewrap`)

Deduplicated destinations are sealed with the server's `vault.key`. After a
recovery onto a server with a different key, `POST /storage/dedup-keys/rewrap`
with `{"passphrase": "…"}` opens each one through its backup-password escrow
(written by every dedup backup that ran with a backup password set) and
re-seals it with this server's key; the previous `_vault/repo.json` is kept
as `_vault/repo.json.<time>.bak`. An empty passphrase changes nothing and only
reports status. The response is
`{"destinations": [{"storage_id", "name", "status", "error"}]}`, where
`status` is `ok`, `rewrapped`, `not_initialised`, `locked_needs_passphrase`,
`locked_no_escrow`, `locked_wrong_passphrase` or `error`. Blocked on
replicas.

While a destination is still sealed with another key, browsing its restore
points (`GET /jobs/{id}/restore-points/{rpid}/contents`) returns
**424** with `{"code": "dedup_key_mismatch", "error": "…"}`.

## MCP (Model Context Protocol)

| Method | Endpoint | Description                                                                         |
| ------ | -------- | ----------------------------------------------------------------------------------- |
| Any    | `/mcp`   | Streamable-HTTP MCP entry point (Claude Desktop, web AI clients). Daemon mode only. |
| Any    | `/mcp/*` | MCP transport sub-paths used by the protocol                                        |

See [MCP Integration](mcp.md) for the available tools and client configuration.

## WebSocket

Connect to `/api/v1/ws` for real-time event streaming. Events include:

- `backup_progress` / `restore_progress` — progress of the item currently running: `item`, `item_type`, `percent`, `message`, plus the [progress fields](#progress) below
- `job_state` — backup or restore lifecycle transitions
- `runner_queue` — current and queued jobs
- `config_changed` — emitted after any CRUD mutation (jobs, storage, settings, replication) so dashboards refresh derived state
- `import_completed` — emitted after a successful Storage → Import run

### Progress

`GET /runner/status` and every `backup_progress` / `restore_progress` event describe the current item's work in its current phase:

| Field                                       | Meaning                                                                                                     |
| ------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| `phase`                                     | `scanning`, `preparing`, `processing`, `transferring`, `verifying`, or `finalizing`                         |
| `phase_started_at`                          | When the phase began (RFC 3339)                                                                             |
| `bytes_done` / `bytes_total`                | Bytes processed in this phase. A total of `0` means unknown                                                 |
| `files_done` / `files_total`                | Files processed in this phase. A total of `0` means unknown                                                 |
| `progress_determinate`                      | `true` when a total is known; `percent` (`current_item_percent` in the status) is then the phase percentage |
| `rate_bytes_per_sec` / `rate_files_per_sec` | Smoothed rate, sampled at most once a second                                                                |
| `eta_seconds`                               | Estimated seconds left in the phase; `0` until 30 s of the phase and 1% of its work have elapsed            |
| `last_progress_at`                          | When work last moved; a stale value suggests the operation is stalled                                       |

Counters never move backward: a retried upload holds its previous high-water mark. Work beyond the expected total raises the total, and a phase reads at most 99% until the next phase starts. When no total is known (for example while scanning), `progress_determinate` is `false` and clients should show the phase and counters without a percentage. Zero and `false` values are omitted from the JSON, so an absent `progress_determinate` means `false` and an absent counter means `0`; all of the fields are absent when the daemon has no structured progress for the item.

Origin patterns cover `localhost`, `127.0.0.1`, `*.local`, `192.168.*.*`, `10.*.*.*`, and the full RFC 1918 `172.16.0.0/12` range (including Docker's default `172.17.0.0/16`).

## Examples

### List all jobs

```bash
curl http://localhost:24085/api/v1/jobs
```

### Create a storage destination

```bash
curl -X POST http://localhost:24085/api/v1/storage \
  -H "Content-Type: application/json" \
  -d '{"name": "Local Backup", "type": "local", "config_json": "{\"path\": \"/mnt/user/backups\"}"}'
```

### Trigger a backup

```bash
curl -X POST http://localhost:24085/api/v1/jobs/1/run
```

### Test a storage connection

```bash
curl -X POST http://localhost:24085/api/v1/storage/1/test
```

### Check health

```bash
curl http://localhost:24085/api/v1/health
```
