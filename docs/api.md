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

| Method | Endpoint             | Description                                                              |
| ------ | -------------------- | ------------------------------------------------------------------------ |
| GET    | `/health`            | Basic health, version, and mode                                          |
| GET    | `/health/summary`    | Aggregated dashboard health metrics                                      |
| GET    | `/ws`                | WebSocket event stream                                                   |
| GET    | `/runner/status`     | Current backup or restore state, including queued jobs                   |
| GET    | `/release/changelog` | Embedded `CHANGELOG.md` parsed into version blocks (drives About modal)  |
| GET    | `/release/latest`    | Latest GitHub release metadata (used by the About card update badge)     |

## Jobs

| Method | Endpoint                                                         | Description                                                                    |
| ------ | ---------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| GET    | `/jobs`                                                          | List jobs                                                                      |
| POST   | `/jobs`                                                          | Create job (response echoes the persisted items)                               |
| GET    | `/jobs/next-runs`                                                | Next scheduled run for every job                                               |
| GET    | `/jobs/{id}`                                                     | Get a job and its items                                                        |
| PUT    | `/jobs/{id}`                                                     | Update a job (response echoes the persisted items)                             |
| DELETE | `/jobs/{id}`                                                     | Delete a job                                                                   |
| GET    | `/jobs/{id}/history`                                             | Job run history (returns `[]` when empty)                                      |
| GET    | `/jobs/{id}/restore-points`                                      | Restore points with chain-health annotations (returns `[]` when empty)         |
| GET    | `/jobs/{id}/retention-preview`                                   | Preview which restore points the configured retention policy would prune       |
| DELETE | `/jobs/{id}/restore-points/{rpid}`                               | Delete a specific restore point and its files                                  |
| GET    | `/jobs/{id}/restore-points/{rpid}/contents?item=…`               | List files inside a restore point (works for classic and dedup restore points) |
| POST   | `/jobs/{id}/restore-points/{rpid}/verify`                        | Trigger an on-demand SHA-256 verification of a restore point                   |
| GET    | `/jobs/{id}/restore-points/{rpid}/verify-runs`                   | List verification runs for a restore point                                     |
| GET    | `/jobs/{id}/verify-runs/{vrid}`                                  | Get a single verification run record                                           |
| POST   | `/jobs/{id}/run`                                                 | Trigger an immediate backup                                                    |
| POST   | `/jobs/{id}/cancel`                                              | Cancel a running backup job                                                    |
| POST   | `/jobs/{id}/restore`                                             | Trigger a restore                                                              |
| GET    | `/jobs/{id}/next-run`                                            | Next scheduled run for one job                                                 |

## Storage

| Method | Endpoint                          | Description                                                                            |
| ------ | --------------------------------- | -------------------------------------------------------------------------------------- |
| GET    | `/storage`                        | List storage destinations (credentials redacted in the response)                       |
| POST   | `/storage`                        | Create storage destination (config validated; response is redacted)                    |
| GET    | `/storage/{id}`                   | Get storage destination (redacted)                                                     |
| PUT    | `/storage/{id}`                   | Partial-merge update; rejects mutation of immutable fields (`type`, `dedup_enabled`)   |
| DELETE | `/storage/{id}`                   | Delete storage destination; `?force=true` drops dependent-job guard; `?deleteFiles=true` also removes backup files |
| POST   | `/storage/{id}/test`              | Test storage connection                                                                |
| POST   | `/storage/{id}/health-check`      | Run the storage health probe (used by the Dashboard health widget)                     |
| POST   | `/storage/{id}/capacity-check`    | Refresh used / total / free capacity (broadcasts `storage_capacity_updated` over WS)   |
| POST   | `/storage/{id}/breaker/close`     | Manually close the destination circuit breaker — clears sticky failure state           |
| POST   | `/storage/{id}/scan-orphans`      | Scan storage for orphaned files (no matching restore point)                            |
| POST   | `/storage/{id}/delete-orphans`    | Delete files surfaced by `scan-orphans`                                                |
| GET    | `/storage/{id}/dedup-stats`       | Dedup repo statistics (chunks, packs, logical/physical bytes, dedup ratio)             |
| POST   | `/storage/{id}/gc`                | Run mark-and-sweep GC on the destination's dedup repo                                  |
| POST   | `/storage/{id}/scan`              | Scan storage for importable backups                                                    |
| POST   | `/storage/{id}/import`            | Import backups discovered during scan (preserves dedup manifest IDs)                   |
| POST   | `/storage/{id}/restore-db`        | Restore the Vault database from a centralized backup at `_vault/vault.db`              |
| GET    | `/storage/{id}/jobs`              | Returns `{ jobs: [{id, name}], job_count: N }` — dependent-job list                    |
| GET    | `/storage/{id}/list`              | List files under a storage path                                                        |
| GET    | `/storage/{id}/files`             | Download a file from storage                                                           |

## Settings

| Method | Endpoint                          | Description                                                   |
| ------ | --------------------------------- | ------------------------------------------------------------- |
| GET    | `/settings`                       | List settings                                                 |
| PUT    | `/settings`                       | Update settings                                               |
| GET    | `/settings/encryption`            | Encryption status                                             |
| POST   | `/settings/encryption`            | Set encryption passphrase                                     |
| POST   | `/settings/encryption/verify`     | Verify encryption passphrase                                  |
| GET    | `/settings/encryption/passphrase` | Read the configured passphrase (`Cache-Control: no-store`)    |
| GET    | `/settings/staging`               | Staging directory info                                        |
| PUT    | `/settings/staging`               | Override the staging directory                                |
| GET    | `/settings/database`              | Database snapshot settings                                    |
| PUT    | `/settings/database`              | Update database snapshot settings                             |
| POST   | `/settings/discord/test`          | Test the Discord webhook                                      |
| GET    | `/settings/api-key`               | API key status (configured / not configured)                  |
| GET    | `/settings/api-key/key`           | Read the current API key (loopback callers only)              |
| POST   | `/settings/api-key/generate`      | Generate a new API key (rate-limited)                         |
| POST   | `/settings/api-key/rotate`        | Rotate the existing API key (rate-limited)                    |
| DELETE | `/settings/api-key`               | Revoke the API key                                            |
| GET    | `/settings/diagnostics`           | Download a diagnostics ZIP (system info, schema, runs, scheduler, daemon log — credentials redacted) |

## Discovery

| Method | Endpoint                     | Description                                                          |
| ------ | ---------------------------- | -------------------------------------------------------------------- |
| GET    | `/browse?path=…`             | Browse filesystem paths (safepath-gated; allowed under `/mnt` etc.)  |
| GET    | `/path-exists?path=…`        | Safepath-gated `os.Stat` (used by the Folder picker for staleness)   |
| GET    | `/containers`                | Discover Docker containers                                           |
| GET    | `/vms`                       | Discover libvirt VMs                                                 |
| GET    | `/folders`                   | Discover folder presets (engine-known paths)                         |
| GET    | `/plugins`                   | Discover installed Unraid plugins                                    |
| GET    | `/zfs`                       | Discover ZFS datasets                                                |
| GET    | `/presets/exclusions`        | Per-container exclusion presets (Plex cache, Sonarr db, …)           |

## Activity Logs

| Method | Endpoint                | Description                                                              |
| ------ | ----------------------- | ------------------------------------------------------------------------ |
| GET    | `/activity?limit=N`     | Activity log entries (`limit` is clamped to ≤ 1000 to prevent OOM)       |
| DELETE | `/activity`             | Purge all activity log entries (irreversible)                            |

## History

| Method | Endpoint   | Description                                      |
| ------ | ---------- | ------------------------------------------------ |
| DELETE | `/history` | Purge all job run history records (irreversible) |

> Per-job history is available at `GET /jobs/{id}/history`. There is no global `GET /history` endpoint; query individual jobs and merge client-side.

## Replication

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

| Method | Endpoint         | Description                                                                            |
| ------ | ---------------- | -------------------------------------------------------------------------------------- |
| GET    | `/recovery/plan` | Recovery plan with per-step item status; step downgrades to `warning` if any item is missing a restore point |

## MCP (Model Context Protocol)

| Method | Endpoint   | Description                                                                                  |
| ------ | ---------- | -------------------------------------------------------------------------------------------- |
| Any    | `/mcp`     | Streamable-HTTP MCP entry point (Claude Desktop, web AI clients). Daemon mode only.          |
| Any    | `/mcp/*`   | MCP transport sub-paths used by the protocol                                                 |

See [MCP Integration](mcp.md) for the available tools and client configuration.

## WebSocket

Connect to `/api/v1/ws` for real-time event streaming. Events include:

- `progress` — per-item backup/restore progress with bytes-done and rate
- `job_state` — backup or restore lifecycle transitions
- `runner_queue` — current and queued jobs
- `config_changed` — emitted after any CRUD mutation (jobs, storage, settings, replication) so dashboards refresh derived state
- `import_completed` — emitted after a successful Storage → Import run

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
