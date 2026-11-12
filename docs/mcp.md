# MCP Integration

Vault exposes a [Model Context Protocol](https://modelcontextprotocol.io/) (MCP) server for AI assistants and automation tools.

## Transports

| Transport       | Endpoint                         | Use Case                                     |
| --------------- | -------------------------------- | -------------------------------------------- |
| Streamable HTTP | `http://<host>:24085/api/v1/mcp` | All clients through a stdio bridge or tunnel |

> Vault no longer supports `vault mcp --db <path>`. This command wrote directly to the database. It did not notify the scheduler or the UI and did not flush changes to flash storage. Use the `mcp-remote` stdio bridge to connect a local client to the HTTP endpoint.

<!-- Separator: keeps these two callouts as distinct blockquotes (MD028). -->

> For a Claude Desktop custom connector, use an HTTPS endpoint that Anthropic's servers can connect to. A LAN address is not available from those servers. A self-signed certificate is not valid for this connection. See the two methods under **Claude Desktop**.

## Assistant Keys

Open **Settings** → **Security** → **Assistant Keys** in the Unraid web UI. Enter a name and create a key. Copy the value before you dismiss it. Give each assistant its own key so you can revoke access separately.

Send the key in the `X-API-Key` header on every request to the daemon. An Assistant Key can use `list_recovery_catalog`, `find_restore_points` and `get_recovery_evidence`. The same permissions apply through an SSH tunnel. Use TLS or a trusted network to protect the key in transit.

The catalog lists Jobs, Restore Points, recorded parent links and recorded backup item names. Vault reports `unknown` when a Restore Point has no item membership records. The tool accepts optional `job_id` and `limit_per_job` fields. The default limit is 200 Restore Points per Job. The maximum is 1000. The tool reads catalog metadata from the database and makes no storage changes.

Use `find_restore_points` with the exact `item_name` and `item_type` (`folder`, `container` or `vm`). Use `get_recovery_evidence` with those same fields and a `restore_point_id` to check the required Backup Chain, artifact metadata and Verification for a candidate. Both tools return the same structured evidence as the [REST endpoints](api.md#backup-item-evidence).

An operator can enable **Allow Restore Requests** for a key in Settings. Reconnect the MCP client after changing permission. The additional tools use the key on every call.

| Tool                       | Input                                                                                                               | Result                                                                        |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| `submit_restore_request`   | `job_id`, `restore_point_id`, exact folder `item_name`, `write_mode` (`replace` or `merge`), optional `destination` | Pending request, review link, evidence and write effects                      |
| `get_restore_request`      | `id`                                                                                                                | Redacted request, linked Restore Run, and recorded outcome for the owning key |
| `withdraw_restore_request` | `id`                                                                                                                | Recorded state after attempting to withdraw the owning key's pending request  |

A request covers one whole folder and expires after 24 hours. Submission performs no restore. Open the review link on the Unraid host to inspect the request. The link carries no authorization. A missing request and another key's request both return a tool error. See the [REST contract](api.md#folder-restore-requests) for state and error details.

An exact repeat returns the existing pending ID and `reused: true`. Each key can hold five unexpired pending requests and submit six new requests per rolling hour. Vault returns a tool error when a new operation reaches either limit. Withdraw a pending request to free a pending slot, or retry after the rolling window.

After the operator approves and the Runner admits the request, use `get_restore_request` to follow the same `run_id` to its recorded outcome. Only a signed-in Unraid operator can stop that run. If the stop takes effect, the tool reports `cancelled` and the stop request time. Vault reports item counts and readiness checks and labels application health as `unchecked`. The tool accepts the request ID and only the owning Assistant Key can read the result. Existing administrator `restore_item` behavior changes in [#646](https://github.com/tinkerologie/vault/issues/646).

## Configuration

### Claude Desktop

Pick the path that matches how you reach your server.

#### LAN-only (recommended): `mcp-remote` stdio bridge

Claude Desktop starts [`mcp-remote`](https://www.npmjs.com/package/mcp-remote) on your device.
Install Node.js on the device that runs Claude Desktop.
Set Vault to listen on a LAN address under **Settings** → **Vault** in Unraid.
The default address (`127.0.0.1`) permits only local connections.
HTTP sends the API key without encryption. Use HTTP only on a trusted LAN.
A LAN address also enables mDNS. See [Network discovery](getting-started.md#network-discovery-mdns).

For recovery inspection, configure the bridge with an Assistant Key.

```json
{
  "mcpServers": {
    "vault": {
      "command": "npx",
      "args": [
        "-y",
        "mcp-remote",
        "http://<host>:24085/api/v1/mcp",
        "--allow-http",
        "--header",
        "X-API-Key:<your-assistant-key>"
      ]
    }
  }
}
```

#### Custom Connector: public HTTPS endpoint

For a custom connector, use an HTTPS endpoint that Anthropic's servers can connect to. The endpoint must use a publicly trusted certificate. Vault can serve HTTPS with `--tls-cert` and `--tls-key`.

You can use a tunnel or a reverse proxy with a publicly trusted certificate. In Claude Desktop, add the public `https://…/api/v1/mcp` URL under **Settings** → **Connectors**.

### Claude Code

Configure a client that can send the Assistant Key header. This example reads the key from the environment.

```json
{
  "mcpServers": {
    "vault": {
      "type": "http",
      "url": "http://127.0.0.1:24085/api/v1/mcp",
      "headers": { "X-API-Key": "${VAULT_ASSISTANT_KEY}" }
    }
  }
}
```

The daemon binds to loopback by default, so reach it over an SSH tunnel
(`ssh -L 24085:127.0.0.1:24085 root@<server-ip>`, or
`ssh -L 24085:<server-ip>:24085 root@<server-ip>` if Vault is bound to the
server's IP, where nothing listens on loopback) and point the URL at
`http://127.0.0.1:24085/api/v1/mcp`. For a client that only speaks stdio, bridge it
with the same header through `mcp-remote`.

## Available Tools

The tools below are available to local and administrator clients. For settings, encryption, recovery planning and storage import, use the [REST API](api.md).

### Jobs

| Tool              | Description                |
| ----------------- | -------------------------- |
| `list_jobs`       | List all backup jobs       |
| `get_job`         | Get a job and its items    |
| `create_job`      | Create a new backup job    |
| `update_job`      | Update an existing job     |
| `delete_job`      | Delete a job               |
| `run_job`         | Start a backup immediately |
| `get_job_history` | Job run history            |

### Storage

| Tool                 | Description                  |
| -------------------- | ---------------------------- |
| `list_storage`       | List storage destinations    |
| `get_storage`        | Get a storage destination    |
| `create_storage`     | Create a storage destination |
| `update_storage`     | Update a storage destination |
| `delete_storage`     | Delete a storage destination |
| `test_storage`       | Test a storage connection    |
| `list_storage_files` | List files in storage        |

### Discovery

| Tool              | Description                |
| ----------------- | -------------------------- |
| `list_containers` | Discover Docker containers |
| `list_vms`        | Discover libvirt VMs       |
| `list_folders`    | Discover folder presets    |
| `list_plugins`    | Discover Unraid plugins    |

### Status

| Tool                 | Description                                                                     |
| -------------------- | ------------------------------------------------------------------------------- |
| `get_health`         | Basic health check                                                              |
| `get_health_summary` | Dashboard health metrics                                                        |
| `get_runner_status`  | Current backup/restore state, with phase, byte and file counters, rate, and ETA |
| `get_activity_log`   | Recent activity log                                                             |
| `get_run_logs`       | Per-run streaming log entries                                                   |

### Restore

| Tool                    | Description                                                                                                   |
| ----------------------- | ------------------------------------------------------------------------------------------------------------- |
| `list_recovery_catalog` | List recorded recovery metadata across Jobs and Restore Points                                                |
| `find_restore_points`   | Find Restore Points across Jobs and check recorded Backup Item membership                                     |
| `get_recovery_evidence` | Inspect recorded membership, the required Backup Chain, storage metadata and Verification for one Backup Item |
| `list_restore_points`   | List restore points for a job                                                                                 |
| `restore_item`          | Restore a backup item                                                                                         |

`restore_item` returns after the restore passes the approval checks. The restore continues in the background.

If approval is necessary for host access or a replacement image, the tool returns an error before it changes data. To give approval, use **Container approvals** in the Restore wizard. An error before the approval checks also returns to the client.

Vault refuses the tool call while a backup, restore, garbage collection, or orphan deletion is active. Vault also refuses it while the daemon shuts down. Vault does not queue the request. Retry after the other operation completes or the daemon restarts. After the restore starts, Vault waits for it to finish before it starts another backup or restore.

An MCP restore does not appear in `get_runner_status` or in run history. You cannot cancel it. The daemon log records the result. If you enable notifications, `get_activity_log` also shows "Restore completed" or "Restore failed". To use progress, history, and cancellation, use the Restore wizard or `POST /jobs/{id}/restore`.

For a deduplicated container restore, the call can take as long as pulling the backed-up image. For a classic container restore from an imported or replicated backup, it can take as long as downloading the backup.

### Replication

| Tool                 | Description                 |
| -------------------- | --------------------------- |
| `list_replication`   | List replication sources    |
| `get_replication`    | Get a replication source    |
| `delete_replication` | Delete a replication source |

## REST-Only Endpoints

Use the REST API for these operations.

- Settings management and encryption configuration
- Storage scan and import workflows
- File downloads from storage
- Replication create and sync flows
- Recovery plan endpoint

See [API Reference](api.md) for the complete REST surface.
