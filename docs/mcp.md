# MCP Integration

Vault exposes a [Model Context Protocol](https://modelcontextprotocol.io/) (MCP) server for AI assistants and automation tools.

## Transports

| Transport       | Endpoint                         | Use Case                                     |
| --------------- | -------------------------------- | -------------------------------------------- |
| Streamable HTTP | `http://<host>:24085/api/v1/mcp` | All clients through a stdio bridge or tunnel |

> Vault no longer supports `vault mcp --db <path>`. This command wrote directly to the database. It did not notify the scheduler or the UI and did not flush changes to flash storage. Use the `mcp-remote` stdio bridge to connect a local client to the HTTP endpoint.

<!-- Separator: keeps these two callouts as distinct blockquotes (MD028). -->

> For a Claude Desktop custom connector, use an HTTPS endpoint that Anthropic's servers can connect to. A LAN address is not available from those servers. A self-signed certificate is not valid for this connection. See the two methods under **Claude Desktop**.

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

```json
{
  "mcpServers": {
    "vault": {
      "command": "npx",
      "args": [
        "-y",
        "mcp-remote",
        "http://<host>:24085/api/v1/mcp",
        "--allow-http"
      ]
    }
  }
}
```

For a connection from another machine, use an API key. Vault refuses the connection until an API key exists
(Settings → Security → API Access in the Unraid web UI). Pass it as a header. Note there is
no space after the colon — `mcp-remote` mishandles spaces in header values:

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
        "X-API-Key:<your-key>"
      ]
    }
  }
}
```

#### Custom Connector: public HTTPS endpoint

For a custom connector, use an HTTPS endpoint that Anthropic's servers can connect to. The endpoint must use a publicly trusted certificate. Vault can serve HTTPS with `--tls-cert` and `--tls-key`.

You can use a tunnel or a reverse proxy with a publicly trusted certificate. In Claude Desktop, add the public `https://…/api/v1/mcp` URL under **Settings** → **Connectors**.

### Claude Code

Add to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "vault": {
      "type": "http",
      "url": "http://<unraid-host>:24085/mcp"
    }
  }
}
```

The daemon binds to loopback by default, so reach it over an SSH tunnel
(`ssh -L 24085:127.0.0.1:24085 root@<server-ip>`, or
`ssh -L 24085:<server-ip>:24085 root@<server-ip>` if Vault is bound to the
server's IP, where nothing listens on loopback) and point the URL at
`http://127.0.0.1:24085/mcp`. For a client that only speaks stdio, bridge it
with `npx mcp-remote http://127.0.0.1:24085/mcp` rather than running a second
process against the database directly.

## Available Tools

The MCP surface is intentionally curated rather than a 1:1 mirror of every REST route. The daemon currently registers 27 tools across the groups below. Settings, encryption, replication mutations, recovery planning, and storage scan/import remain REST-only — see the [API Reference](api.md).

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

| Tool                  | Description                   |
| --------------------- | ----------------------------- |
| `list_restore_points` | List restore points for a job |
| `restore_item`        | Restore a backup item         |

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
