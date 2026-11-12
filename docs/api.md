# API Reference

Base URL: `http://<host>:24085/api/v1`

Use the Vault REST API to manage backups, storage destinations, and settings. Use WebSocket events to receive progress updates.

## Transport

To serve HTTPS, start the daemon with both `--tls-cert` and `--tls-key`. If you set only one option, the daemon does not start. If you set neither option, the daemon serves HTTP.

On a non-loopback address, anyone who can read HTTP traffic can read the API key and data. The daemon logs a warning at startup. Use HTTP only on a network you trust.

## Authentication

Open Vault from the Unraid web UI to use the plugin proxy. The proxy connects from the Vault server without a Vault API key.

For a remote connection, send `X-API-Key: <your-api-key>`. This applies to replication servers and [Home Assistant](https://github.com/tinkerologie/ha-vault).

The access rules apply to every API route, including `/health`.

| Caller                                                                    | Access                                                                                          |
| ------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------- |
| The Vault host: the Unraid plugin proxy, the CLI, `curl` or an SSH tunnel | No key needed                                                                                   |
| Anyone else, including an MCP bridge or script on another machine         | Send the API key. Vault returns `401` until a key exists                                        |
| A web page on another origin, on any method and from any address          | Vault returns `403` for origins outside the allowlist. Send the API key from a permitted origin |

Send an [Assistant Key](mcp.md#assistant-keys) in the same header to read the recovery catalog and Backup Item evidence. The key limits access on remote connections, loopback connections and SSH tunnels. Vault returns `401` for an invalid or revoked key and `403` for operations outside its permissions. Connect directly to the daemon. The Unraid plugin proxy refuses Assistant Key headers.

A local request must come from an address on the Vault host. Its `Host` header must contain an IP address, `localhost`, or the Vault host name. A domain that points to `127.0.0.1` does not get local access. This prevents DNS rebinding.

To make a key, open **Settings** → **Security** → **API Access** in Vault through the Unraid web UI. Generate the key. Copy the key to the client. On a headless server or Docker replica, use the CLI.

```bash
vault api-key generate                           # on the host; prints only the key
docker exec vault-replica vault api-key generate # a Docker replica
```

`vault api-key` also has `show`, `rotate`, `revoke`, and `status` commands. These commands connect to the daemon on this host. They use the address in `vault.cfg` unless you set `--addr ip:port`. If the daemon uses TLS, specify its certificate with `--tls-cert <file>`.

**Local-only routes:** these answer `403` to anything but the Vault host, even with a valid key: `/settings/api-key/key`, `/settings/api-key/generate`, `/settings/api-key/rotate`, `DELETE /settings/api-key`, `/settings/encryption/passphrase` and `/settings/server-key`.

**Failed attempts:** key checks are rate limited per address and in total, and answer `429` past the limit. A client using the correct key is not affected once it has authenticated.

**Browser origins.** The cross-origin allowlist is empty by default. To permit a page on another origin, add the exact origin. Repeat `--allowed-origin https://ha.example:8123` on `vault daemon` or `vault replica` for each origin. You can also set a comma-separated `ALLOWED_ORIGINS` in the `vault.cfg` next to the database. Restart the daemon after the change. Vault rejects wildcards. The list also controls CORS, Private Network Access preflights, and WebSocket origins.

**Remote browser.** When you open the daemon port from another machine, enter the API key. The browser keeps it for that tab. The UI uses polling for progress, anomalies, and mounts because the browser cannot send the key over WebSocket.

A same-origin page on the Vault host can connect without an API key. An SSH tunnel can give the page local access. Use the command for the address on which Vault listens.

- LAN address: `ssh -L 24085:<server-ip>:24085 root@<server-ip>`
- Loopback address: `ssh -L 24085:127.0.0.1:24085 root@<server-ip>`

Then open `http://127.0.0.1:24085`. Use the Unraid web UI, an SSH tunnel, or the local CLI to manage the API key.

## Core

| Method | Endpoint             | Description                                                                                                                                                   |
| ------ | -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| GET    | `/health`            | Get status, version, mode, and `startup` diagnostics. Use local access or send the API key. For an anonymous health check, use `GET /ping` at the server root |
| GET    | `/health/summary`    | Dashboard health metrics across every job, including `recent_partial`, `recent_skipped`, `recent_cancelled` and `stale_jobs`                                  |
| GET    | `/ws`                | WebSocket event stream                                                                                                                                        |
| GET    | `/runner/status`     | Current backup or restore state, queued jobs, and [progress](#progress)                                                                                       |
| GET    | `/release/changelog` | Embedded `CHANGELOG.md` parsed into version blocks (drives About modal)                                                                                       |
| GET    | `/release/latest`    | Latest GitHub release metadata (used by the About card update badge)                                                                                          |

The `recent_*` counts and `success_rate` include acknowledged backup runs. The `outstanding_failed`, `outstanding_skipped`, `outstanding_cancelled`, and `outstanding_partial` counts include only unacknowledged runs in the same per-job window. Protection coverage makes up 40% of the health score. The remaining 60% comes from the share of successes among successful and outstanding failed, skipped, and cancelled backup runs. With no scored runs, the success component is zero.

## Jobs

Create and update responses contain the Job and items read from the committed mutation. For an update, omit `items` or send `null` to keep the current selection. Send `[]` to clear it. Request validation can reject an invalid update before Vault checks if the Job exists.

The job payload's `schedule`, `verify_schedule` and `full_backup_schedule` fields accept standard 5-field cron expressions (`minute hour day-of-month month day-of-week`). An empty `schedule` means manual-only for backups — the scheduler skips regular runs entirely. Similarly, an empty `verify_schedule` or `full_backup_schedule` disables that secondary schedule. Arbitrary expressions are accepted, including steps, ranges and lists, along with the `@daily`-style descriptors and Vault's `L` day-of-month token (last day of the month). An expression the scheduler cannot parse is rejected at create/update time rather than stored and silently never run.

`full_backup_schedule` sets a separate schedule for full backups on an `incremental` or `differential` job. If `backup_type_chain` is `full`, Vault clears this field when you save. All runs for a full job are full backups.

The job payload's `backup_type_chain` field accepts `full`, `incremental`, or `differential`. Incremental and differential jobs automatically run as a **full** backup on their first run (when the job has no previous restore point to attach to); later runs capture only changes.

| Method | Endpoint                                           | Description                                                                               |
| ------ | -------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| GET    | `/jobs`                                            | List jobs                                                                                 |
| POST   | `/jobs`                                            | Create a Job and return the committed Job and items                                       |
| GET    | `/jobs/next-runs`                                  | Next scheduled run for every job                                                          |
| GET    | `/jobs/{id}`                                       | Get a job and its items                                                                   |
| PUT    | `/jobs/{id}`                                       | Update a Job and return the committed Job and items                                       |
| DELETE | `/jobs/{id}`                                       | Delete a job                                                                              |
| GET    | `/jobs/{id}/history`                               | Job run history (returns `[]` when empty)                                                 |
| GET    | `/jobs/{id}/restore-points`                        | Restore points with chain-health annotations (returns `[]` when empty)                    |
| GET    | `/jobs/{id}/retention-preview`                     | Preview which restore points the configured retention policy would prune                  |
| DELETE | `/jobs/{id}/restore-points/{rpid}`                 | Delete a specific restore point and its files                                             |
| GET    | `/jobs/{id}/restore-points/{rpid}/contents?item=…` | List files inside a restore point (works for classic and dedup restore points)            |
| POST   | `/jobs/{id}/restore-points/{rpid}/verify`          | Start SHA-256 verification of a restore point                                             |
| GET    | `/jobs/{id}/restore-points/{rpid}/verify-runs`     | List verification runs for a restore point                                                |
| GET    | `/jobs/{id}/verify-runs/{vrid}`                    | Get a single verification run record                                                      |
| POST   | `/jobs/{id}/run`                                   | Start a backup immediately                                                                |
| POST   | `/jobs/{id}/cancel`                                | Cancel a running backup job                                                               |
| POST   | `/jobs/{id}/restore`                               | Start a restore. Set `destination` to an absolute path or, for a ZFS item, a dataset name |
| GET    | `/jobs/{id}/next-run`                              | Next scheduled run for one job                                                            |

## Storage

| Method | Endpoint                       | Description                                                                                                                         |
| ------ | ------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------- |
| GET    | `/storage`                     | List storage destinations (credentials redacted in the response)                                                                    |
| POST   | `/storage`                     | Add a storage destination. Vault validates the configuration and removes secrets from the response                                  |
| GET    | `/storage/{id}`                | Get storage destination (redacted)                                                                                                  |
| PUT    | `/storage/{id}`                | Update specified fields. Vault rejects changes to `type` and `dedup_enabled`                                                        |
| DELETE | `/storage/{id}`                | Delete a destination. Set `?force=true` to permit deletion with dependent jobs. Set `?deleteFiles=true` to also delete backup files |
| POST   | `/storage/{id}/test`           | Test storage connection                                                                                                             |
| POST   | `/storage/{id}/health-check`   | Run the storage health probe (used by the Dashboard health widget)                                                                  |
| POST   | `/storage/{id}/capacity-check` | Refresh used / total / free capacity (broadcasts `storage_capacity_updated` over WS)                                                |
| POST   | `/storage/{id}/breaker/close`  | Manually close the destination circuit breaker — clears sticky failure state                                                        |
| POST   | `/storage/{id}/scan-orphans`   | Scan storage for orphaned files (no matching restore point)                                                                         |
| POST   | `/storage/{id}/delete-orphans` | Delete files that `scan-orphans` found. Vault reports an error for each path during a backup, import, or sync                       |
| GET    | `/storage/{id}/dedup-stats`    | Dedup repo statistics (chunks, packs, logical/physical bytes, dedup ratio)                                                          |
| POST   | `/storage/{id}/gc`             | Run mark-and-sweep GC on the destination's dedup repo                                                                               |
| POST   | `/storage/{id}/scan`           | Scan storage for importable backups. Send `passphrase` in the JSON body. Vault returns `400` if the query string contains it        |
| POST   | `/storage/{id}/import`         | Import backups discovered during scan (preserves dedup manifest IDs)                                                                |
| GET    | `/storage/{id}/db-backups`     | List Vault database backups found under `_vault/` on the destination (newest first)                                                 |
| POST   | `/storage/{id}/restore-db`     | Restore the Vault database from a `_vault/` backup — see body fields below                                                          |
| POST   | `/storage/dedup-keys/rewrap`   | After a recovery, re-seal dedup destinations with this server's key using the backup passphrase — see below                         |
| GET    | `/storage/{id}/jobs`           | Returns `{ jobs: [{id, name}], job_count: N }` — dependent-job list                                                                 |
| GET    | `/storage/{id}/list`           | List files under a storage path                                                                                                     |
| GET    | `/storage/{id}/files`          | Download a file from storage                                                                                                        |

## Settings

| Method | Endpoint                          | Description                                                                                            |
| ------ | --------------------------------- | ------------------------------------------------------------------------------------------------------ |
| GET    | `/settings`                       | List settings                                                                                          |
| PUT    | `/settings`                       | Update settings                                                                                        |
| GET    | `/settings/encryption`            | Encryption status                                                                                      |
| POST   | `/settings/encryption`            | Set encryption passphrase                                                                              |
| POST   | `/settings/encryption/verify`     | Verify encryption passphrase                                                                           |
| GET    | `/settings/encryption/passphrase` | Read the configured passphrase (local only, `Cache-Control: no-store`)                                 |
| GET    | `/settings/server-key`            | Download `vault.key`, the raw 32-byte server key (local only, `Cache-Control: no-store`, rate limited) |
| GET    | `/settings/staging`               | Staging directory info                                                                                 |
| PUT    | `/settings/staging`               | Override the staging directory                                                                         |
| GET    | `/settings/database`              | Database snapshot settings                                                                             |
| PUT    | `/settings/database`              | Update database snapshot settings                                                                      |
| POST   | `/settings/discord/test`          | Test the Discord webhook                                                                               |
| GET    | `/settings/api-key`               | API key status (configured / not configured)                                                           |
| GET    | `/settings/api-key/key`           | Read the current API key (local only)                                                                  |
| POST   | `/settings/api-key/generate`      | Generate a new API key (local only, rate-limited)                                                      |
| POST   | `/settings/api-key/rotate`        | Rotate the existing API key (local only, rate-limited)                                                 |
| DELETE | `/settings/api-key`               | Revoke the API key, refusing remote clients until a new one exists (local only)                        |
| GET    | `/settings/assistant-keys`        | List key IDs, names and creation times (daemon mode)                                                   |
| POST   | `/settings/assistant-keys`        | Create a key with `{"name":"<assistant-name>"}` (daemon mode, local only, rate limited)                |
| DELETE | `/settings/assistant-keys/{id}`   | Revoke the specified key (daemon mode, local only)                                                     |
| GET    | `/settings/diagnostics`           | Download a diagnostics ZIP (system info, schema, runs, scheduler, daemon log — credentials redacted)   |

Create returns `201` with `id`, `name`, `created_at` and the one-time `assistant_key` value. The response sets `Cache-Control: no-store`. A duplicate name returns `409`. A database restore in progress also returns `409` to a create or revoke request. Revoke returns `204` and closes that key's MCP sessions.

## Discovery

| Method | Endpoint              | Description                                                                                                                                 |
| ------ | --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| GET    | `/browse?path=…`      | Browse filesystem paths. Vault permits only paths that pass the safepath check, such as paths under `/mnt`                                  |
| GET    | `/path-exists?path=…` | Safepath-gated `os.Stat` (used by the Folder picker for staleness)                                                                          |
| GET    | `/containers`         | Discover Docker containers                                                                                                                  |
| GET    | `/vms`                | Find libvirt VMs. Each item has `disk_format` (`qcow2`, `raw`, `mixed`, or `unknown`) and `supports_incremental` (qcow2 only) in `settings` |
| GET    | `/folders`            | Discover folder presets (engine-known paths)                                                                                                |
| GET    | `/plugins`            | Discover installed Unraid plugins                                                                                                           |
| GET    | `/zfs`                | Discover ZFS datasets                                                                                                                       |
| GET    | `/presets/exclusions` | Per-container exclusion presets (`paths`), plus advisory `notes`/`warnings` for some apps (e.g. Immich DB)                                  |

## Activity Logs

| Method | Endpoint                                | Description                                                                                                                                                                                                                           |
| ------ | --------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| GET    | `/activity?limit=N`                     | Get activity log entries. The maximum `limit` is 1000. `run_gone` shows that the run log no longer exists                                                                                                                             |
| DELETE | `/activity`                             | Purge all activity log entries (irreversible)                                                                                                                                                                                         |
| GET    | `/runs/{runId}`                         | One backup/restore run with its status and item counters, however old. Replication reads it for a run outside the newest page of a job's history. Vault returns `404` for an unknown run and `400` for an invalid run ID              |
| GET    | `/runs/{runId}/logs?after=<id>&limit=N` | Get a run log, from oldest to newest. Use `?after=<id>` for newer lines. Vault limits `?limit=N` to 1 through 1000. Vault returns `404` for an unknown run. An invalid run ID, negative `after`, or nonpositive `limit` returns `400` |

Run log durations keep sub-second precision: messages show `<1s`, `2.4s`, or `1m5s`, and the structured `data` carries `duration_ms` beside the existing `duration_seconds`. A transfer rate is only reported for operations that take at least one second. Vault shows a rate only if `size_bytes` is positive. A run of at least one second that moved data sets `data.throughput_bytes_per_sec`. A shorter run that moved data says `throughput not calculated: duration below 1.0s` and sets `data.throughput_not_calculated`. A zero-byte run omits both fields. The web UI and Discord notifications apply the same one-second rule.

Each backup and restore run log also records:

- **Run details**: one line with the job name and ID, run ID, destination name and storage type, item count, and (restores) the restore point ID.
- **Phase lines**: one line per stage per item, for example `Phase upload completed for appdata in 4m12s`, with `data.phase`, `data.item_name`, `data.duration_ms`, and byte fields where measured. Backup stages are `stage`, `upload`, `dedup store`, and `verify`. Classic restore stages are `download` and `extract`. Deduplicated restore stages are `open repository` and `restore`. A failed stage logs `Phase <name> failed for <item> after <duration>` at error level, which shows where the operation stopped.
- **Retries**: each recovered storage failure (a retried request, a resumed stream, or a restarted download part) is a warning line naming the operation, the object's file name, and the attempt, for example `Transient storage error on ReadRange data.tar.zst (attempt 2/5), retrying: connection reset`. Error text is redacted: credentials are masked and URLs are cut to scheme and host. After 20 warning lines, Vault logs "further storage retry events suppressed" once. It continues to count all retries.
- **Measured bytes**, each under its own field: `stored_bytes` (what landed on the destination), `bytes_uploaded` (including retried attempts), `logical_bytes` and `duplicate_chunks` (deduplicated backups), `bytes_read` (read back from storage), `files_restored`, `retries`, and `resumes`. Verification lines include the bytes read and the time taken.
- **Summary totals**: `read=`, `retries=`, and `transient_errors=` are appended to the final line when storage was read or retried. Restore summaries also name the job and destination.

A restore of selected files reports the size it actually wrote (`size_source: "restored"`). A whole-item restore keeps reporting the size recorded at backup time (`size_source: "recorded item size"`).

## History

| Method | Endpoint    | Description                                              |
| ------ | ----------- | -------------------------------------------------------- |
| DELETE | `/history`  | Purge all job run history records (irreversible)         |
| POST   | `/runs/ack` | Acknowledge reviewed backup or restore runs on a primary |

Use `GET /history?limit_per_job=N` for all jobs, or `GET /jobs/{id}/history` for one job. Run records include `acknowledged_at`, `acknowledged_by`, and `acknowledge_reason`. These fields are null until an acknowledgement is recorded. The run's original outcome remains in the same record.

To acknowledge runs, send `{"ids":[12,34],"reason":"Reviewed"}`. Supply 1 to 1,000 positive run IDs and an optional reason of up to 500 characters. Vault acknowledges failed, error, cancelled, skipped, and partial runs. The response is `{"acknowledged":2,"skipped":0}`. Counts are based on unique IDs. Missing, ineligible, and previously acknowledged runs count as skipped. Invalid JSON returns `400`, invalid field values return `422`, and replicas return `403`.

Vault masks secrets in storage responses. Send the masked value to keep the stored secret on update. If you change a connection field in this list, enter the secret again.

| Storage type | Connection fields                                               |
| ------------ | --------------------------------------------------------------- |
| SFTP         | `host`, `port`, `host_key`, `known_hosts_file`                  |
| SMB          | `host`, `port`                                                  |
| WebDAV       | The scheme, host, and port of `url`, and `insecure_skip_verify` |
| S3           | `endpoint`, `bucket`, `region`                                  |

`POST /storage/test` accepts an optional `id` to test an edit. It uses the same rule for stored secrets. Without `id`, Vault rejects a masked secret.

## Replication

A replication source's `schedule` field takes the same cron expressions as a job's (see [Jobs](#jobs)); an empty string means the source is only synced on demand.

On a `vault replica`, you can create, edit, test, and sync a source. The replica returns `403` if you try to delete a source. The replica also refuses these operations:

- Storage garbage collection and orphan deletion
- Import
- Circuit breaker closure
- Forced storage destination deletion and backup file deletion
- History and activity purges, and run acknowledgement
- Anomaly acknowledgement

A sync copies only jobs with `source_id` set to `0` on the remote server. These jobs originated on that server. Vault skips jobs that the remote server copied from another source. To receive those jobs, add their origin server as a source.

For each copied restore point, Vault records a completed backup run with the original backup date. The run appears in History, and the point appears in the job's restore points. A replicated job has no configured items. Give `item_name` and `item_type` in a restore request.

Vault records `partial` if the remote backup was unsuccessful or incomplete. Otherwise it records `success`. If the remote server cannot list run history, Vault uses only the failures on the restore point.

Vault links an incremental or differential point to the local copy of the backup that it depends on. If Vault did not copy that backup, Vault does not record the dependent point.

If Vault cannot download or record a point, the sync ends as `partial`. If Vault records nothing, the sync ends as `failed`. Vault updates `last_sync_success_at` only on `success`. A later sync retries the failed copy.

If the source has no `storage_dest_id`, Vault selects the first storage destination. The first sync saves that choice. Each sync assigns that destination to replicated jobs from this source that have no destination.

A sync does not copy the remote `_vault/packs` store. If a restore point uses deduplication manifests, Vault reports an error and does not download its files. Vault can copy classic backups, such as VM and ZFS backups, from a deduplicated destination.

Vault stops a download if response headers or an individual read take two minutes. Time spent writing to local storage does not count. Vault has no limit on the total download time.

If the server refuses a request, Vault shows the HTTP status and the JSON `error` message. Vault removes control characters and limits the message to 200 characters.

| Method | Endpoint                 | Description                   |
| ------ | ------------------------ | ----------------------------- |
| GET    | `/replication`           | List replication sources      |
| POST   | `/replication`           | Create replication source     |
| POST   | `/replication/test-url`  | Test a replication URL        |
| GET    | `/replication/{id}`      | Get replication source        |
| PUT    | `/replication/{id}`      | Update replication source     |
| DELETE | `/replication/{id}`      | Delete replication source     |
| POST   | `/replication/{id}/test` | Test replication connection   |
| POST   | `/replication/{id}/sync` | Start replication immediately |
| GET    | `/replication/{id}/jobs` | List replicated jobs          |

Responses never contain a source's API key: `config` shows `••••••••` instead, and `credentials_locked` is `true` when this server's `vault.key` cannot open it. On update, omitted fields keep their stored values, an omitted, blank or masked `api_key` keeps the stored key, and a new value replaces it. Changing `url` requires sending the key again, so a stored key only goes to the server it was entered for. `warning` is set when the source uses plain HTTP to another machine. Plain HTTP stays allowed, so use it only on a network you trust.

## Recovery

| Method | Endpoint                                   | Description                                                                                                      |
| ------ | ------------------------------------------ | ---------------------------------------------------------------------------------------------------------------- |
| GET    | `/recovery/catalog`                        | List recorded Job and Restore Point metadata (daemon mode, available to Assistant Keys)                          |
| GET    | `/recovery/restore-points`                 | Find Restore Points across Jobs and check recorded Backup Item membership                                        |
| GET    | `/recovery/restore-points/{rpid}/evidence` | Inspect one Backup Item at a Restore Point                                                                       |
| GET    | `/recovery/plan`                           | Get the recovery plan and item status for each step. A step has status `warning` if an item has no restore point |
| GET    | `/recovery/path-audit`                     | Find job and item paths that do not exist on this server. The Recover Vault wizard uses this operation           |
| POST   | `/recovery/path-remap`                     | Rewrite audited paths to new locations (e.g. after a share or disk was renamed)                                  |

### Recovery catalog

`GET /recovery/catalog` returns the same data as the MCP tool `list_recovery_catalog`. Optional query parameters are `job_id` and `limit_per_job`. The default limit is 200 Restore Points per Job. The maximum is 1000. Each Job has a `truncated` flag.

The response contains Job IDs, names, enabled and replicated flags, Restore Point metadata and recorded item membership. Membership is `recorded` or `unknown`. Chain status describes recorded parent links. If the chain exceeds the catalog limit, Vault reports an unknown chain. Vault reads this catalog from the database. The catalog excludes storage paths, configuration and backup file contents.

### Backup Item evidence

In daemon mode, Assistant Keys can use both Restore Point endpoints. Supply `item_type` (`folder`, `container` or `vm`) and the exact `item_name`. Discovery returns Restore Points in descending ID order with membership `confirmed`, `missing` or `unknown`. The default page size is 100 and the maximum is 200. Pass the response's `next_before_id` as `before_id` to read the next page. Older records can confirm a name without confirming its type.

Evidence for a selected point lists its Backup Chain from oldest to newest. Each step shows membership, whether restore uses its stored data and the result of a metadata probe for required data. Deduplicated restore uses the selected point's manifest. Vault checks the repository header and the pack range for the manifest using the local chunk index. Classic evidence checks the recorded item directory or object metadata. Vault reports `unknown` if a probe cannot finish within its time or object limit or if the Destination cannot provide the necessary metadata.

Verification shows the latest recorded mode, status and times, plus any backup-time Verification flag. Missing prerequisites and known unusable artifacts prevent approval eligibility. Unavailable artifact evidence or a failed Verification produces a warning. Storage probes check metadata without reading backup payloads.

### Restore approvals (`/jobs/{id}/restore`)

Two optional Boolean fields give approval for a container restore. Both default to `false` and apply to all items in the request.

The MCP `restore_item` tool has no approval fields. The tool waits for the approval checks before it reports that the restore started. If approval is necessary, the tool returns an error. To give approval, open **Restore** → **Container approvals** in Vault.

- `allow_host_privileges` — restore host-level settings (privileged mode, host network, IPC or PID, added capabilities, devices, host paths outside `/mnt`) from a backup this server did not make: imported or replicated.
- `allow_unpinned_image` — for a deduplicated container backup whose recorded image digest is missing or cannot be pulled, use the image the tag points to now.

### Restoring the database (`restore-db`)

`POST /storage/{id}/restore-db` takes a JSON body:

- `storage_path` — path of the database backup on the destination, e.g. `_vault/vault.db.latest.age` (encrypted) or a timestamped copy like `_vault/vault.db.2026-07-01T02-00-00.age`. Unencrypted backups use the `.db` extension.
- `passphrase` — the backup password. Specify the password for an encrypted backup.
- `verify_only` — when `true`, validates the backup (and passphrase) without replacing the current database.

List the available backups first with `GET /storage/{id}/db-backups`.

Restoring a database preserves the configured API Key and the current Assistant Key set. The daemon keeps Assistant Key revocations alongside `vault.key` so an older snapshot cannot reactivate a revoked key.

The response's `locked_credentials` counts restored storage destinations and replication sources sealed with a different `vault.key`. See [Stored credentials and vault.key](guides/disaster-recovery.md#stored-credentials-and-vaultkey).

### Unlocking dedup destinations (`dedup-keys/rewrap`)

Vault protects deduplicated destinations with the server's `vault.key`. After recovery to a server with a different key, send `{"passphrase": "…"}` to `POST /storage/dedup-keys/rewrap`.

The API opens each destination through its backup-password escrow and encrypts the key again with this server's key. Each deduplicated backup that runs with a backup password writes this escrow.

Vault keeps the previous `_vault/repo.json` as `_vault/repo.json.<time>.bak` until it validates the new header. Then Vault removes that copy because the old key can still open it.

An empty passphrase returns status without changes. The response is
`{"destinations": [{"storage_id", "name", "status", "error"}]}`. The status can be
`ok`, `rewrapped`, `not_initialised`, `locked_needs_passphrase`,
`locked_no_escrow`, `locked_wrong_passphrase`, or `error`. Replicas refuse this operation.

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

| Field                                       | Meaning                                                                                                                     |
| ------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `phase`                                     | `scanning`, `preparing`, `processing`, `transferring`, `verifying`, or `finalizing`                                         |
| `phase_started_at`                          | When the phase began (RFC 3339)                                                                                             |
| `bytes_done` / `bytes_total`                | Bytes processed in this phase. A total of `0` means unknown                                                                 |
| `files_done` / `files_total`                | Files processed in this phase. A total of `0` means unknown                                                                 |
| `progress_determinate`                      | If Vault knows the total, this value is `true`. `percent` (`current_item_percent` in the status) gives the phase percentage |
| `rate_bytes_per_sec` / `rate_files_per_sec` | Smoothed rate, sampled at most once a second                                                                                |
| `eta_seconds`                               | Estimated seconds to finish the phase. The value is `0` until at least 30 seconds and 1 percent of the work have elapsed    |
| `last_progress_at`                          | Time of the last change in progress. An old value can show that the operation has stopped                                   |

Counters do not decrease. If Vault retries an upload, it keeps the previous maximum until it makes more progress. If work exceeds the expected total, Vault increases the total. A phase shows at most 99% until the next phase starts.

If Vault does not know the total, `progress_determinate` is `false`. Show the phase and counters without a percentage.

The JSON omits zero and `false` values. Read an absent counter as `0` and an absent `progress_determinate` as `false`. If Vault has no structured progress for the item, it omits all progress fields.

The WebSocket follows the same access rules as the rest of the API: same-origin or an [allowlisted origin](#authentication), plus local access or the `X-API-Key` header.

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
