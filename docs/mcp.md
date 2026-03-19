# MCP Integration

Vault exposes a [Model Context Protocol](https://modelcontextprotocol.io/) (MCP) server for AI assistants and automation tools.

## Transports

| Transport       | Endpoint                         | Use Case                                   |
| --------------- | -------------------------------- | ------------------------------------------ |
| Streamable HTTP | `http://<host>:24085/api/v1/mcp` | Remote clients (Claude Desktop, web tools) |
| Stdio           | `vault mcp --db <path>`          | Local clients (Claude Code, CLI tools)     |

## Configuration

### Claude Desktop

Add to your Claude Desktop MCP settings:

```json
{
  "mcpServers": {
    "vault": {
      "url": "http://<host>:24085/api/v1/mcp"
    }
  }
}
```

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

The MCP surface is intentionally curated rather than a 1:1 mirror of every REST route. It covers these tool groups:

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
- Auth bootstrap and API key management
- Storage scan and import workflows
- File downloads from storage
- Replication create and sync flows
- Recovery plan endpoint

See [API Reference](api.md) for the complete REST surface.
