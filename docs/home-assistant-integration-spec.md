# Vault — Home Assistant Integration Specification

> Developer reference for building a Home Assistant custom integration that interfaces with the **Vault** backup daemon running on an Unraid server.

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Configuration / Config Flow](#configuration--config-flow)
- [Vault REST API Reference](#vault-rest-api-reference)
- [WebSocket Real-Time Events](#websocket-real-time-events)
- [Entities to Create](#entities-to-create)
- [Services to Register](#services-to-register)
- [Automation Examples](#automation-examples)
- [Error Handling](#error-handling)
- [Authentication](#authentication)

---

## Overview

**Vault** is a backup/restore daemon for Unraid servers. It backs up Docker containers, libvirt VMs, and folders to pluggable storage destinations (local, SFTP, S3, SMB). It exposes a REST API and a WebSocket endpoint for real-time progress.

The HA integration should:

1. Monitor Vault health, job statuses, and backup history
2. Trigger on-demand backups (e.g., before an HA OS upgrade)
3. Show real-time backup progress via WebSocket
4. Work with HA's built-in backup mechanism to trigger Vault backups as a pre-upgrade step
5. Expose sensors, binary sensors, and services for use in automations

**Vault API base URL:** `http://<unraid-ip>:24085/api/v1`
**WebSocket URL:** `ws://<unraid-ip>:24085/api/v1/ws`

> **Authentication** is supported via API key (`--api-key` flag or `VAULT_API_KEY` env var). When configured, all API requests require a valid key via `Authorization: Bearer <key>`, `X-API-Key: <key>` header, or `?token=<key>` query parameter. TLS is also supported (`--tls-cert`, `--tls-key`) — when enabled, use `https://` and `wss://` URLs.
>
> The `/api/v1/auth/status` endpoint returns `{"auth_required": true/false}` so the integration can detect whether authentication is needed.
>
> **Request body size limit:** 1 MB for all API endpoints.
>
> **Storage credential redaction:** `GET /storage` and `GET /storage/{id}` redact sensitive config fields (passwords, secret keys) with `••••••••` in responses.

---

## Architecture

```text
Home Assistant
├── config_flow.py          # UI setup: host, port
├── __init__.py             # Platform setup, coordinator
├── coordinator.py          # DataUpdateCoordinator (polls API)
├── sensor.py               # Sensors (job status, last run, size, etc.)
├── binary_sensor.py        # Binary sensors (vault online, job running)
├── button.py               # Buttons (run backup now)
├── const.py                # Constants (DOMAIN, DEFAULT_PORT, etc.)
├── services.yaml           # Service definitions
├── strings.json            # UI translations
├── manifest.json           # Integration manifest
└── websocket.py            # Optional: persistent WS connection for events
```

### Recommended Approach

- Use a **`DataUpdateCoordinator`** that polls `/health`, `/jobs`, and `/activity` every 30–60 seconds
- Optionally maintain a persistent **WebSocket** connection to `/api/v1/ws` for real-time backup progress events (fire HA events like `vault_backup_started`, `vault_backup_completed`, etc.)
- Register **services** for on-demand operations (run backup, restore)

---

## Configuration / Config Flow

The integration should use HA's config flow UI:

### User Input

| Field   | Type   | Default | Description                            |
| ------- | ------ | ------- | -------------------------------------- |
| host    | string | —       | Unraid IP or hostname                  |
| port    | int    | 24085   | Vault API port                         |
| api_key | string | —       | API key (optional, if auth is enabled) |
| tls     | bool   | false   | Use HTTPS/WSS instead of HTTP/WS       |

### Validation Step

During config flow validation:

1. Check if auth is required:

```http
GET http://{host}:{port}/api/v1/auth/status
```

```json
{ "auth_required": true }
```

1. Validate connectivity (include auth header if `auth_required` is `true`):

```http
GET http://{host}:{port}/api/v1/health
Authorization: Bearer <api_key>
```

Expected response:

```json
{ "status": "ok", "version": "2026.3.0" }
```

If this returns 200 with `status: "ok"`, config is valid. Store the `version` for display.
If `auth_required` is `true` and no `api_key` was provided, prompt the user to enter one.

---

## Vault REST API Reference

All endpoints are under `http://{host}:{port}/api/v1`.
All responses are JSON. Errors return `{"error": "message"}`.

**Authentication:** When API key auth is enabled, include one of:

- `Authorization: Bearer <api_key>` header
- `X-API-Key: <api_key>` header
- `?token=<api_key>` query parameter

Unauthenticated requests to protected endpoints return `401 Unauthorized`.

**Public endpoints** (no auth required): `/health`, `/auth/status`, `/ping`.

### Auth Status

| Method | Path           | Description                         |
| ------ | -------------- | ----------------------------------- |
| GET    | `/auth/status` | Check if authentication is required |

**Response:**

```json
{ "auth_required": true }
```

### Health

| Method | Path      | Description      |
| ------ | --------- | ---------------- |
| GET    | `/health` | Health + version |

**Response:**

```json
{ "status": "ok", "version": "2026.3.0" }
```

### Ping

| Method | Path    | Description           |
| ------ | ------- | --------------------- |
| GET    | `/ping` | Simple liveness check |

Returns `200 OK` with body `.` (plain text). Useful for fast connectivity checks.

---

### Jobs

| Method | Path                        | Description              |
| ------ | --------------------------- | ------------------------ |
| GET    | `/jobs`                     | List all backup jobs     |
| POST   | `/jobs`                     | Create a new job         |
| GET    | `/jobs/{id}`                | Get job + items          |
| PUT    | `/jobs/{id}`                | Update a job             |
| DELETE | `/jobs/{id}`                | Delete a job             |
| GET    | `/jobs/{id}/history`        | Get run history          |
| GET    | `/jobs/{id}/restore-points` | List restore points      |
| POST   | `/jobs/{id}/run`            | Trigger immediate backup |
| POST   | `/jobs/{id}/restore`        | Trigger a restore        |

#### GET `/jobs` — List Jobs

**Response:** Array of Job objects

```json
[
  {
    "id": 1,
    "name": "Daily Docker Backup",
    "description": "Backs up all production containers",
    "enabled": true,
    "schedule": "0 2 * * *",
    "backup_type_chain": "full",
    "retention_count": 5,
    "retention_days": 30,
    "compression": "zstd",
    "encryption": "none",
    "container_mode": "one_by_one",
    "pre_script": "",
    "post_script": "",
    "notify_on": "failure",
    "verify_backup": true,
    "storage_dest_id": 1,
    "created_at": "2026-02-28T10:00:00Z",
    "updated_at": "2026-02-28T10:00:00Z"
  }
]
```

**Key fields for HA:**

| Field               | Type   | Values                                      | Description                        |
| ------------------- | ------ | ------------------------------------------- | ---------------------------------- |
| `id`                | int    | —                                           | Job ID (use for run/restore calls) |
| `name`              | string | —                                           | Human-readable name                |
| `enabled`           | bool   | —                                           | Whether scheduled runs are active  |
| `schedule`          | string | cron expression                             | e.g., `"0 2 * * *"` = 2 AM daily   |
| `encryption`        | string | `"none"`, `"age"`                           | Whether backups are encrypted      |
| `compression`       | string | `"none"`, `"gzip"`, `"zstd"`                | Compression method                 |
| `backup_type_chain` | string | `"full"`, `"incremental"`, `"differential"` | Backup chain type                  |

#### GET `/jobs/{id}` — Get Job Details

**Response:**

```json
{
  "job": {
    /* Job object as above */
  },
  "items": [
    {
      "id": 1,
      "job_id": 1,
      "item_type": "container",
      "item_name": "homeassistant",
      "item_id": "abc123def",
      "settings": "{\"image\":\"homeassistant/home-assistant:latest\"}",
      "sort_order": 0
    }
  ]
}
```

#### POST `/jobs/{id}/run` — Trigger Backup

**This is the primary endpoint for HA automations.**

No request body needed. Returns immediately; backup runs asynchronously.

**Response (202 Accepted):**

```json
{
  "message": "backup started",
  "job_id": 1
}
```

Progress is streamed via WebSocket (see below).

#### GET `/jobs/{id}/history?limit=50` — Run History

**Response:** Array of JobRun objects (newest first)

```json
[
  {
    "id": 10,
    "job_id": 1,
    "status": "completed",
    "backup_type": "full",
    "started_at": "2026-02-28T02:00:00Z",
    "completed_at": "2026-02-28T02:15:30Z",
    "log": "[homeassistant] OK (524288000 bytes) [verified]\n",
    "items_total": 3,
    "items_done": 3,
    "items_failed": 0,
    "size_bytes": 1572864000
  }
]
```

**Status values:** `"running"`, `"completed"`, `"partial"`, `"failed"`

#### GET `/jobs/{id}/restore-points` — List Restore Points

**Response:**

```json
[
  {
    "id": 5,
    "job_run_id": 10,
    "job_id": 1,
    "backup_type": "full",
    "storage_path": "vault/Daily Docker Backup/10_2026-02-28_020000",
    "metadata": "{\"items\":3,\"items_failed\":0,\"job_name\":\"Daily Docker Backup\",\"verified\":true}",
    "size_bytes": 1572864000,
    "created_at": "2026-02-28T02:15:30Z"
  }
]
```

#### POST `/jobs/{id}/restore` — Trigger Restore

**Request:**

```json
{
  "restore_point_id": 5,
  "item_name": "homeassistant",
  "item_type": "container",
  "destination": "",
  "passphrase": "my-secret-passphrase"
}
```

| Field              | Required | Description                                   |
| ------------------ | -------- | --------------------------------------------- |
| `restore_point_id` | Yes      | ID from the restore points list               |
| `item_name`        | Yes      | Name of the item to restore                   |
| `item_type`        | Yes      | `"container"`, `"vm"`, or `"folder"`          |
| `destination`      | No       | Override restore destination path             |
| `passphrase`       | No       | Required if the backup was encrypted with age |

**Response (202 Accepted):**

```json
{ "message": "restore started" }
```

---

### Storage Destinations

| Method | Path                 | Description           |
| ------ | -------------------- | --------------------- |
| GET    | `/storage`           | List all destinations |
| GET    | `/storage/{id}`      | Get destination       |
| POST   | `/storage/{id}/test` | Test connection       |

#### GET `/storage` — List Storage Destinations

```json
[
  {
    "id": 1,
    "name": "Local NAS",
    "type": "local",
    "config": "{\"path\":\"/mnt/user/backups\"}",
    "created_at": "2026-02-28T10:00:00Z",
    "updated_at": "2026-02-28T10:00:00Z"
  }
]
```

**Storage types:** `"local"`, `"sftp"`, `"s3"`, `"smb"`

#### POST `/storage/{id}/test` — Test Connection

**Response:**

```json
{ "success": true }
```

or

```json
{ "success": false, "error": "connection refused" }
```

---

### Settings

| Method | Path                   | Description           |
| ------ | ---------------------- | --------------------- |
| GET    | `/settings`            | Get all settings      |
| PUT    | `/settings`            | Update settings       |
| GET    | `/settings/encryption` | Get encryption status |

#### GET `/settings/encryption`

```json
{ "encryption_enabled": true }
```

---

### Activity Log

| Method | Path        | Description                 |
| ------ | ----------- | --------------------------- |
| GET    | `/activity` | Recent activity log entries |

**Response:**

```json
[
  {
    "id": 42,
    "level": "info",
    "category": "backup",
    "message": "Backup completed: Daily Docker Backup",
    "details": "Run ID: 10, Done: 3, Failed: 0, Size: 1572864000 bytes",
    "created_at": "2026-02-28T02:15:30Z"
  }
]
```

**Levels:** `"info"`, `"warning"`, `"error"`
**Categories:** `"backup"`, `"restore"`, `"system"`

---

### Discovery

| Method | Path          | Description            |
| ------ | ------------- | ---------------------- |
| GET    | `/containers` | List Docker containers |
| GET    | `/vms`        | List libvirt VMs       |
| GET    | `/folders`    | List folder presets    |

These are informational — useful for showing what's available to back up.

---

## WebSocket Real-Time Events

**Endpoint:** `ws://{host}:{port}/api/v1/ws`

Connect using a standard WebSocket client. The server broadcasts JSON messages to all connected clients.

**Authentication:** If API key auth is enabled, pass the key as a query parameter:

```text
ws://{host}:{port}/api/v1/ws?token=<api_key>
```

**Origin validation:** The WebSocket endpoint only accepts connections from local network origins (localhost, `127.0.0.1`, `*.local`, `192.168.*.*`, `10.*.*.*`, `172.16.*.*`). HA running on the same LAN will connect without issues.

**TLS:** When TLS is enabled, use `wss://` instead of `ws://`.

### Message Types

All messages have a `"type"` field:

#### `job_run_started`

```json
{
  "type": "job_run_started",
  "job_id": 1,
  "run_id": 10
}
```

#### `item_backup_start`

```json
{
  "type": "item_backup_start",
  "job_id": 1,
  "run_id": 10,
  "item_name": "homeassistant",
  "item_type": "container"
}
```

#### `backup_progress`

```json
{
  "type": "backup_progress",
  "item": "homeassistant",
  "percent": 45,
  "message": "Backing up volumes..."
}
```

#### `item_backup_done`

```json
{
  "type": "item_backup_done",
  "job_id": 1,
  "run_id": 10,
  "item_name": "homeassistant",
  "size_bytes": 524288000,
  "verified": true
}
```

#### `item_backup_failed`

```json
{
  "type": "item_backup_failed",
  "job_id": 1,
  "run_id": 10,
  "item_name": "homeassistant",
  "error": "container not running"
}
```

#### `job_run_completed`

```json
{
  "type": "job_run_completed",
  "job_id": 1,
  "run_id": 10,
  "status": "completed",
  "items_done": 3,
  "items_failed": 0,
  "size_bytes": 1572864000
}
```

### HA Integration Approach

1. Maintain a persistent WebSocket connection (reconnect on disconnect)
2. Parse incoming messages and fire corresponding HA events:
   - `vault_backup_started` — when `job_run_started` received
   - `vault_backup_progress` — when `backup_progress` received
   - `vault_backup_completed` — when `job_run_completed` received
   - `vault_backup_failed` — when `job_run_completed` with `status: "failed"` received
3. Update entity states immediately on WebSocket events (don't wait for poll)

---

## Entities to Create

### Sensors

| Entity ID                                 | Type     | Source                                  | Description                                                   |
| ----------------------------------------- | -------- | --------------------------------------- | ------------------------------------------------------------- |
| `sensor.vault_status`                     | string   | `GET /health` → `status`                | `"ok"` or `"unavailable"`                                     |
| `sensor.vault_version`                    | string   | `GET /health` → `version`               | e.g., `"2026.3.0"`                                            |
| `sensor.vault_jobs_total`                 | int      | `GET /jobs` → count                     | Total number of configured jobs                               |
| `sensor.vault_jobs_enabled`               | int      | `GET /jobs` → count where enabled       | Number of enabled jobs                                        |
| `sensor.vault_{job_name}_status`          | string   | Last run's `status`                     | `"completed"`, `"partial"`, `"failed"`, `"running"`, `"idle"` |
| `sensor.vault_{job_name}_last_run`        | datetime | Last run's `completed_at`               | Timestamp of last completed run                               |
| `sensor.vault_{job_name}_last_size`       | int      | Last run's `size_bytes`                 | Bytes, use `device_class: data_size`                          |
| `sensor.vault_{job_name}_items_backed_up` | int      | Last run's `items_done`                 | Number of items in last run                                   |
| `sensor.vault_{job_name}_items_failed`    | int      | Last run's `items_failed`               | Failures in last run                                          |
| `sensor.vault_{job_name}_progress`        | int      | WebSocket `backup_progress`             | 0–100 percent during active backup                            |
| `sensor.vault_{job_name}_restore_points`  | int      | `GET /jobs/{id}/restore-points` → count | Number of available restore points                            |
| `sensor.vault_storage_{name}_status`      | string   | `POST /storage/{id}/test`               | `"connected"` or `"error"`                                    |
| `sensor.vault_encryption_status`          | string   | `GET /settings/encryption`              | `"enabled"` or `"disabled"`                                   |

### Binary Sensors

| Entity ID                                     | Type | Source                           | Description                  |
| --------------------------------------------- | ---- | -------------------------------- | ---------------------------- |
| `binary_sensor.vault_online`                  | bool | `GET /health` succeeds           | Vault daemon reachable       |
| `binary_sensor.vault_{job_name}_running`      | bool | Last run status == `"running"`   | Backup currently in progress |
| `binary_sensor.vault_{job_name}_last_success` | bool | Last run status == `"completed"` | Last run fully successful    |

### Buttons

| Entity ID                         | Type   | Action                | Description              |
| --------------------------------- | ------ | --------------------- | ------------------------ |
| `button.vault_{job_name}_run_now` | button | `POST /jobs/{id}/run` | Trigger immediate backup |

### Device Info

Create a single **device** for each Vault instance:

```python
{
    "identifiers": {(DOMAIN, f"{host}:{port}")},
    "name": "Vault Backup",
    "manufacturer": "Vault",
    "model": "Unraid Backup Daemon",
    "sw_version": version,  # from /health
    "configuration_url": f"http://{host}:{port}",
}
```

---

## Services to Register

Define in `services.yaml` and implement in `__init__.py`:

### `vault.run_backup`

Trigger an immediate backup for a specific job.

```yaml
run_backup:
  name: Run Backup
  description: Trigger an immediate backup run for a Vault job
  fields:
    job_id:
      name: Job ID
      description: The Vault backup job ID to run
      required: true
      selector:
        number:
          min: 1
          mode: box
    job_name:
      name: Job Name
      description: Alternatively, specify the job by name
      required: false
      selector:
        text:
```

**Implementation:** `POST /api/v1/jobs/{id}/run`

### `vault.restore`

Trigger a restore from a specific restore point.

```yaml
restore:
  name: Restore Backup
  description: Restore an item from a Vault backup restore point
  fields:
    job_id:
      name: Job ID
      required: true
      selector:
        number:
          min: 1
          mode: box
    restore_point_id:
      name: Restore Point ID
      required: true
      selector:
        number:
          min: 1
          mode: box
    item_name:
      name: Item Name
      description: Name of the item to restore (container name, VM name, or folder)
      required: true
      selector:
        text:
    item_type:
      name: Item Type
      description: Type of the item
      required: true
      selector:
        select:
          options:
            - container
            - vm
            - folder
    passphrase:
      name: Encryption Passphrase
      description: Required if the backup was created with age encryption
      required: false
      selector:
        text:
    destination:
      name: Destination Override
      description: Optional override for restore destination path
      required: false
      selector:
        text:
```

**Implementation:** `POST /api/v1/jobs/{id}/restore`

### `vault.test_storage`

Test connectivity to a storage destination.

```yaml
test_storage:
  name: Test Storage Connection
  description: Test connectivity to a Vault storage destination
  fields:
    storage_id:
      name: Storage Destination ID
      required: true
      selector:
        number:
          min: 1
          mode: box
```

**Implementation:** `POST /api/v1/storage/{id}/test`

---

## Automation Examples

### 1. Backup HAOS VM Before Home Assistant Upgrade

This automation triggers a Vault backup of the Home Assistant OS VM on Unraid before HA upgrades itself, using HA's built-in update entity.

```yaml
alias: "Vault: Backup HAOS VM before HA upgrade"
description: >
  When a Home Assistant update becomes available and the user triggers it,
  first run a Vault backup of the HAOS VM on Unraid, wait for completion,
  then proceed with the HA update.
trigger:
  - platform: state
    entity_id: update.home_assistant_core_update
    to: "on"
    # Fires when an update becomes available
condition: []
action:
  # Step 1: Trigger Vault backup of the HAOS VM job
  - service: vault.run_backup
    data:
      job_id: 2 # The job ID that backs up the HAOS VM

  # Step 2: Wait for the backup to complete (monitor the sensor)
  - wait_for_trigger:
      - platform: state
        entity_id: sensor.vault_haos_vm_backup_status
        to: "completed"
    timeout: "01:00:00"
    continue_on_timeout: false

  # Step 3: Notify that backup is done
  - service: notify.persistent_notification
    data:
      title: "Vault Backup Complete"
      message: "HAOS VM backup completed successfully. Safe to upgrade."

  # Step 4: Optionally auto-install the update
  # - service: update.install
  #   target:
  #     entity_id: update.home_assistant_core_update
```

### 2. Backup HA Container Before Upgrade (Docker-based HA)

For users running Home Assistant as a Docker container on Unraid:

```yaml
alias: "Vault: Backup HA container before upgrade"
trigger:
  - platform: state
    entity_id: update.home_assistant_core_update
    to: "on"
action:
  - service: vault.run_backup
    data:
      job_id: 1 # Job that includes the homeassistant container

  - wait_for_trigger:
      - platform: state
        entity_id: sensor.vault_daily_docker_backup_status
        to: "completed"
    timeout: "00:30:00"
    continue_on_timeout: false

  - service: notify.mobile_app_phone
    data:
      title: "Vault Backup Done"
      message: "Home Assistant container backed up. Proceeding with update."

  - service: update.install
    target:
      entity_id: update.home_assistant_core_update
```

### 3. Using HA's Built-in Backup + Vault Together

This chains HA's built-in backup mechanism with Vault, so you get both an HA-level backup AND a Vault infrastructure backup before upgrading:

```yaml
alias: "Full pre-upgrade backup chain"
trigger:
  - platform: state
    entity_id: update.home_assistant_core_update
    to: "on"
action:
  # Step 1: Create HA's built-in backup first
  - service: backup.create
    data:
      name: "pre-upgrade-{{ now().strftime('%Y%m%d-%H%M') }}"

  # Step 2: Wait for HA backup to complete
  - delay: "00:05:00"

  # Step 3: Now trigger Vault to back up the entire HA VM/container
  # This captures the HA backup file + all container data
  - service: vault.run_backup
    data:
      job_id: 2

  # Step 4: Wait for Vault backup
  - wait_for_trigger:
      - platform: state
        entity_id: sensor.vault_haos_vm_backup_status
        to: "completed"
    timeout: "01:00:00"
    continue_on_timeout: false

  # Step 5: Notify
  - service: notify.persistent_notification
    data:
      title: "Pre-Upgrade Backups Complete"
      message: >
        Both HA backup and Vault infrastructure backup completed.
        Safe to proceed with Home Assistant upgrade.
```

### 4. Alert on Backup Failures

```yaml
alias: "Vault: Alert on backup failure"
trigger:
  - platform: event
    event_type: vault_backup_completed
    event_data:
      status: "failed"
action:
  - service: notify.mobile_app_phone
    data:
      title: "⚠️ Vault Backup Failed"
      message: >
        Job {{ trigger.event.data.job_id }} failed.
        Items done: {{ trigger.event.data.items_done }},
        Failed: {{ trigger.event.data.items_failed }}
```

### 5. Daily Backup Health Check

```yaml
alias: "Vault: Daily health check"
trigger:
  - platform: time
    at: "08:00:00"
condition:
  - condition: state
    entity_id: binary_sensor.vault_online
    state: "off"
action:
  - service: notify.persistent_notification
    data:
      title: "Vault Offline"
      message: "The Vault backup daemon on Unraid is not responding."
```

### 6. Nightly Backup Summary

```yaml
alias: "Vault: Nightly backup summary"
trigger:
  - platform: time
    at: "07:00:00"
action:
  - service: notify.persistent_notification
    data:
      title: "Vault Backup Summary"
      message: >
        Docker backup: {{ states('sensor.vault_daily_docker_backup_status') }}
        ({{ states('sensor.vault_daily_docker_backup_last_size') | filesizeformat }})
        VM backup: {{ states('sensor.vault_haos_vm_backup_status') }}
        Restore points: {{ states('sensor.vault_daily_docker_backup_restore_points') }}
```

---

## Error Handling

### API Errors

All error responses follow this format:

```json
{ "error": "descriptive error message" }
```

Common HTTP status codes:

| Code | Meaning                                            |
| ---- | -------------------------------------------------- |
| 200  | Success                                            |
| 201  | Created (new resource)                             |
| 202  | Accepted (async operation started)                 |
| 204  | Deleted (no content)                               |
| 400  | Bad request (invalid JSON, missing required field) |
| 401  | Unauthorized (missing or invalid API key)          |
| 404  | Not found (invalid job/storage/restore point ID)   |
| 413  | Request body too large (>1 MB)                     |
| 500  | Internal server error                              |

### Coordinator Error Handling

- If `/health` fails, mark all entities as unavailable
- If a specific endpoint fails, only mark related entities as unavailable
- If `401 Unauthorized` is returned, mark all entities as unavailable and trigger re-auth flow
- Implement exponential backoff on repeated failures
- Reconnect WebSocket with exponential backoff (start 5s, max 5m)

---

## Authentication

Vault supports optional API key authentication. When the daemon is started with `--api-key=<key>` (or `VAULT_API_KEY` env var), all API endpoints except `/health`, `/auth/status`, and `/ping` require a valid key.

### Supported Auth Methods

| Method              | Example                             |
| ------------------- | ----------------------------------- |
| Bearer token header | `Authorization: Bearer my-api-key`  |
| API key header      | `X-API-Key: my-api-key`             |
| Query parameter     | `?token=my-api-key`                 |

### Integration Flow

1. On setup, call `GET /api/v1/auth/status` to check if auth is required
2. If `auth_required: true`, prompt the user for their API key in the config flow
3. Store the API key in HA's config entry (encrypted by HA)
4. Include the key in all subsequent API and WebSocket requests
5. On `401` responses, mark entities as unavailable and prompt for re-authentication

### TLS Support

When the daemon is started with `--tls-cert` and `--tls-key`, it serves HTTPS. The integration should:

- Use `https://` for REST API calls
- Use `wss://` for WebSocket connections
- Allow the user to toggle TLS in the config flow
- Support self-signed certificates (with appropriate HA config)

### Sensitive Data Handling

- **Storage credentials** are redacted in `GET /storage` and `GET /storage/{id}` responses — passwords and secret keys are replaced with `••••••••`
- **Encryption passphrase** is sealed at rest (AES-256-GCM) and never returned by `GET /settings`
- The integration does **not** need to handle credential management — this is done via the Vault web UI

---

## Implementation Notes

### Polling Interval

- **Default:** 60 seconds for the main coordinator
- **Active backup:** When a job is running (detected via WebSocket or polling), increase poll rate to 10 seconds for progress updates
- **Idle:** 60 seconds is sufficient

### Entity Naming

Use slugified job names for entity IDs:

```python
# "Daily Docker Backup" → "daily_docker_backup"
slug = re.sub(r'[^a-z0-9]+', '_', job_name.lower()).strip('_')
entity_id = f"sensor.vault_{slug}_status"
```

### Coordinator Data Structure

```python
@dataclass
class VaultData:
    health: dict           # /health response
    jobs: list[dict]       # /jobs response
    job_runs: dict[int, list[dict]]   # job_id → recent runs
    storage: list[dict]    # /storage response
    encryption: dict       # /settings/encryption response
    activity: list[dict]   # /activity response
```

### WebSocket Integration

```python
import aiohttp

async def websocket_listener(hass, host, port, api_key=None, use_tls=False):
    scheme = "wss" if use_tls else "ws"
    url = f"{scheme}://{host}:{port}/api/v1/ws"
    if api_key:
        url += f"?token={api_key}"
    async with aiohttp.ClientSession() as session:
        async with session.ws_connect(url) as ws:
            async for msg in ws:
                if msg.type == aiohttp.WSMsgType.TEXT:
                    data = json.loads(msg.data)
                    event_type = f"vault_{data['type']}"
                    hass.bus.async_fire(event_type, data)
```

### manifest.json

```json
{
  "domain": "vault",
  "name": "Vault Backup",
  "version": "1.0.0",
  "documentation": "https://github.com/ruaan-deysel/vault",
  "requirements": ["aiohttp"],
  "codeowners": ["@your-github-handle"],
  "iot_class": "local_polling",
  "config_flow": true
}
```

---

## Summary of Key Endpoints for HA

| Use Case                  | Method | Endpoint                     | Notes                     |
| ------------------------- | ------ | ---------------------------- | ------------------------- |
| Check if Vault is alive   | GET    | `/health`                    | Poll every 60s            |
| List all jobs             | GET    | `/jobs`                      | For entity creation       |
| Get job details + items   | GET    | `/jobs/{id}`                 | Items list for restore UI |
| Trigger backup            | POST   | `/jobs/{id}/run`             | Returns 202, runs async   |
| Get last run status       | GET    | `/jobs/{id}/history?limit=1` | Most recent run           |
| List restore points       | GET    | `/jobs/{id}/restore-points`  | For restore service       |
| Trigger restore           | POST   | `/jobs/{id}/restore`         | Returns 202, runs async   |
| List storage destinations | GET    | `/storage`                   | For status sensors        |
| Test storage connection   | POST   | `/storage/{id}/test`         | For health monitoring     |
| Check encryption status   | GET    | `/settings/encryption`       | Informational sensor      |
| Activity log              | GET    | `/activity`                  | For recent events sensor  |
| Check auth requirement    | GET    | `/auth/status`               | Public, no auth needed    |
| Real-time events          | WS     | `/ws`                        | Persistent connection     |
