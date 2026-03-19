# Vault

[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/ruaan-deysel/vault)
[![Build & Test](https://github.com/ruaan-deysel/vault/actions/workflows/build.yml/badge.svg)](https://github.com/ruaan-deysel/vault/actions/workflows/build.yml)
[![Latest Release](https://img.shields.io/github/v/release/ruaan-deysel/vault?sort=date&label=release)](https://github.com/ruaan-deysel/vault/releases/latest)
[![Go Version](https://img.shields.io/badge/go-1.26-00ADD8?logo=go)](https://go.dev/)
[![Svelte](https://img.shields.io/badge/svelte-5-FF3E00?logo=svelte&logoColor=white)](https://svelte.dev/)
[![License](https://img.shields.io/github/license/ruaan-deysel/vault)](https://github.com/ruaan-deysel/vault/blob/main/LICENSE)
[![GitHub Issues](https://img.shields.io/github/issues/ruaan-deysel/vault)](https://github.com/ruaan-deysel/vault/issues)
[![GitHub Stars](https://img.shields.io/github/stars/ruaan-deysel/vault?style=flat)](https://github.com/ruaan-deysel/vault/stargazers)

Vault is a backup and restore daemon for [Unraid](https://unraid.net/) servers. It protects Docker containers, libvirt VMs, folders, and plugins by backing them up to pluggable storage destinations. Vault ships with a REST API, an MCP server for AI assistants, WebSocket progress events, and an integrated web UI built with Svelte 5.

![Vault Dashboard](docs/screenshots/01-dashboard.png)

## Features

- Docker container backup and restore with image, config, and appdata handling
- VM backup and restore with snapshot and cold modes
- Folder and plugin backup support
- Full, incremental, and differential backup chains
- Local, SFTP, SMB, and NFS storage backends
- Cron-based scheduling with retention policies
- Web UI with Dashboard, Jobs, Restore, Storage, History, Replication, Recovery, and Settings
- WebSocket progress streaming and runner queue visibility
- MCP tools for AI assistants and automation
- Light and dark themes with mobile-responsive layout

## Installation

### Unraid Community Applications

Search for **Vault** in the Unraid Community Applications store and click Install.

### Manual Install

Paste this URL into **Plugins > Install Plugin** in the Unraid web UI:

```text
https://raw.githubusercontent.com/ruaan-deysel/vault/main/plugin/vault.plg
```

## Quick Start

1. **Add Storage** — Go to the Storage page and configure a backup destination (local path, SFTP, SMB, or NFS)
2. **Create a Job** — Go to the Jobs page, pick what to back up, choose a schedule, and set retention rules
3. **Run Backup** — Click "Run Now" or wait for the schedule to kick in
4. **Monitor** — Watch live progress on the Dashboard or check the History page for results

## Documentation

| Document                                   | Description                                       |
| ------------------------------------------ | ------------------------------------------------- |
| [Getting Started](docs/getting-started.md) | Visual walkthrough of the web UI with screenshots |
| [API Reference](docs/api.md)               | Full REST API endpoint reference                  |
| [MCP Integration](docs/mcp.md)             | Model Context Protocol server for AI tools        |
| [Architecture](docs/architecture.md)       | Project structure, build commands, deployment     |

## Requirements

- Unraid 7.0 or newer

## Support and Feedback

- Bug reports: [open a bug report](https://github.com/ruaan-deysel/vault/issues/new?template=01-bug-report.yml)
- Enhancement requests: [request an improvement](https://github.com/ruaan-deysel/vault/issues/new?template=02-enhancement-request.yml)
- Questions and support: [use the Unraid forum support thread](https://forums.unraid.net/topic/197786-plugin-vault-backup-manager)

## License

Vault is licensed under the [MIT License](LICENSE). It is a third-party community plugin for Unraid OS.
