# API Reference

Base URL: `http://<host>:24085/api/v1`

The Vault daemon exposes a REST API for managing backups, storage destinations, and system settings. WebSocket events are available for real-time progress streaming.

## Core

| Method | Endpoint          | Description                                            |
| ------ | ----------------- | ------------------------------------------------------ |
| GET    | `/health`         | Basic health, version, and mode                        |
| GET    | `/health/summary` | Aggregated dashboard health metrics                    |
| GET    | `/ws`             | WebSocket event stream                                 |
| GET    | `/runner/status`  | Current backup or restore state, including queued jobs |

## Jobs

| Method | Endpoint                    | Description                                  |
| ------ | --------------------------- | -------------------------------------------- |
| GET    | `/jobs`                     | List jobs                                    |
| POST   | `/jobs`                     | Create job                                   |
| GET    | `/jobs/next-runs`           | Next scheduled run for every job             |
| GET    | `/jobs/{id}`                | Get a job and its items                      |
| PUT    | `/jobs/{id}`                | Update a job                                 |
| DELETE | `/jobs/{id}`                | Delete a job                                 |
| GET    | `/jobs/{id}/history`        | Job run history                              |
| GET    | `/jobs/{id}/restore-points` | Restore points with chain health annotations |
| POST   | `/jobs/{id}/run`            | Trigger an immediate backup                  |
| POST   | `/jobs/{id}/restore`        | Trigger a restore                            |
| GET    | `/jobs/{id}/next-run`       | Next scheduled run for one job               |

## Storage

| Method | Endpoint                   | Description                               |
| ------ | -------------------------- | ----------------------------------------- |
| GET    | `/storage`                 | List storage destinations                 |
| POST   | `/storage`                 | Create storage destination                |
| GET    | `/storage/{id}`            | Get storage destination                   |
| PUT    | `/storage/{id}`            | Update storage destination                |
| DELETE | `/storage/{id}`            | Delete storage destination                |
| POST   | `/storage/{id}/test`       | Test storage connection                   |
| POST   | `/storage/{id}/scan`       | Scan storage for importable backups       |
| POST   | `/storage/{id}/import`     | Import backups discovered during scan     |
| POST   | `/storage/{id}/restore-db` | Restore the Vault database from storage   |
| GET    | `/storage/{id}/jobs`       | Jobs that depend on a storage destination |
| GET    | `/storage/{id}/list`       | List files under a storage path           |
| GET    | `/storage/{id}/files`      | Download a file from storage              |

## Settings

| Method | Endpoint                          | Description                       |
| ------ | --------------------------------- | --------------------------------- |
| GET    | `/settings`                       | List settings                     |
| PUT    | `/settings`                       | Update settings                   |
| GET    | `/settings/encryption`            | Encryption status                 |
| POST   | `/settings/encryption`            | Set encryption passphrase         |
| POST   | `/settings/encryption/verify`     | Verify encryption passphrase      |
| GET    | `/settings/encryption/passphrase` | Read the configured passphrase    |
| GET    | `/settings/staging`               | Staging directory info            |
| PUT    | `/settings/staging`               | Override the staging directory    |
| GET    | `/settings/database`              | Database snapshot settings        |
| PUT    | `/settings/database`              | Update database snapshot settings |
| POST   | `/settings/discord/test`          | Test the Discord webhook          |

## Discovery, Activity, Replication, and Recovery

| Method | Endpoint                 | Description                     |
| ------ | ------------------------ | ------------------------------- |
| GET    | `/browse`                | Browse filesystem paths         |
| GET    | `/containers`            | Discover Docker containers      |
| GET    | `/vms`                   | Discover VMs                    |
| GET    | `/folders`               | Discover folder presets         |
| GET    | `/plugins`               | Discover plugins                |
| GET    | `/activity`              | Activity log                    |
| GET    | `/replication`           | List replication sources        |
| POST   | `/replication`           | Create replication source       |
| POST   | `/replication/test-url`  | Test a replication URL          |
| GET    | `/replication/{id}`      | Get replication source          |
| PUT    | `/replication/{id}`      | Update replication source       |
| DELETE | `/replication/{id}`      | Delete replication source       |
| POST   | `/replication/{id}/test` | Test replication connection     |
| POST   | `/replication/{id}/sync` | Trigger replication immediately |
| GET    | `/replication/{id}/jobs` | List replicated jobs            |
| GET    | `/recovery/plan`         | Recovery plan                   |

## WebSocket

Connect to `/api/v1/ws` for real-time event streaming. Events include backup progress, job state changes, and runner queue updates.

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
