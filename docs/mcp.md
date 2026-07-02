# MCP Integration

Vault exposes a [Model Context Protocol](https://modelcontextprotocol.io/) (MCP) server for AI assistants and automation tools.

## Transports

| Transport       | Endpoint                         | Use Case                                       |
| --------------- | -------------------------------- | ---------------------------------------------- |
| Streamable HTTP | `http://<host>:24085/api/v1/mcp` | Remote clients, via a stdio bridge or a tunnel |
| Stdio           | `vault mcp --db <path>`          | Local clients (Claude Code, CLI tools)         |

> **You can't paste the `http://…` endpoint straight into Claude Desktop.** Its
> "Add custom connector" flow only accepts **HTTPS** URLs, and connectors are
> reached **from Anthropic's cloud** — so a LAN address (`192.168.x.x`,
> `tower.local`) is unreachable from Anthropic's servers and a self-signed
> certificate is rejected. Native HTTPS alone (see below) does not make a LAN URL
> work. Use one of the two paths in the Claude Desktop section.

## Configuration

### Claude Desktop

Pick the path that matches how you reach your server.

#### LAN-only (recommended): `mcp-remote` stdio bridge

Claude Desktop launches a local [`mcp-remote`](https://www.npmjs.com/package/mcp-remote)
process that bridges to Vault's HTTP endpoint over your LAN. Vault must be
configured to listen on a LAN-reachable address (Settings → Vault on the Unraid
webgui — the default bind is `127.0.0.1`, which is not reachable from another
machine). Traffic, including the API key header, travels unencrypted over HTTP,
so only use this on a trusted LAN. No TLS or public exposure is required; you
just need Node.js installed on the same machine as Claude Desktop.

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

If you have set an API key (Settings → API), pass it as a header. Note there is
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

To add Vault as a native Custom Connector, the endpoint must be reachable from
Anthropic's cloud over HTTPS with a **publicly-trusted** certificate. Vault
serves HTTPS natively when the daemon is started with `--tls-cert`/`--tls-key`,
but a self-signed cert won't be accepted and a private IP won't be reachable.
Put Vault behind a tunnel or reverse proxy that terminates trusted TLS — e.g.
Cloudflare Tunnel, Tailscale Funnel, or nginx with a Let's Encrypt
certificate — then add the resulting public `https://…/api/v1/mcp` URL under
Settings → Connectors in Claude Desktop.

### Claude Code

Add to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "vault": {
      "type": "stdio",
      "command": "vault",
      "args": ["mcp", "--db", "/path/to/vault.db"]
    }
  }
}
```

## Available Tools

The MCP surface is intentionally curated rather than a 1:1 mirror of every REST route. The daemon currently registers 27 tools across the groups below. Settings, encryption, replication mutations, recovery planning, and storage scan/import remain REST-only — see the [API Reference](api.md).

### Jobs

| Tool              | Description                 |
| ----------------- | --------------------------- |
| `list_jobs`       | List all backup jobs        |
| `get_job`         | Get a job and its items     |
| `create_job`      | Create a new backup job     |
| `update_job`      | Update an existing job      |
| `delete_job`      | Delete a job                |
| `run_job`         | Trigger an immediate backup |
| `get_job_history` | Job run history             |

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

| Tool                 | Description                  |
| -------------------- | ---------------------------- |
| `get_health`         | Basic health check           |
| `get_health_summary` | Dashboard health metrics     |
| `get_runner_status`  | Current backup/restore state |
| `get_activity_log`   | Recent activity log          |

### Restore

| Tool                  | Description                   |
| --------------------- | ----------------------------- |
| `list_restore_points` | List restore points for a job |
| `restore_item`        | Restore a backup item         |

### Replication

| Tool                 | Description                 |
| -------------------- | --------------------------- |
| `list_replication`   | List replication sources    |
| `get_replication`    | Get a replication source    |
| `delete_replication` | Delete a replication source |

## REST-Only Endpoints

The following operations are available via REST API only and are not exposed through MCP:

- Settings management and encryption configuration
- Storage scan and import workflows
- File downloads from storage
- Replication create and sync flows
- Recovery plan endpoint

See [API Reference](api.md) for the complete REST surface.
