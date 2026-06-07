# Vault

[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/ruaan-deysel/vault)
[![Build & Test](https://github.com/ruaan-deysel/vault/actions/workflows/build.yml/badge.svg)](https://github.com/ruaan-deysel/vault/actions/workflows/build.yml)
[![Latest Release](https://img.shields.io/github/v/release/ruaan-deysel/vault?sort=date&label=release)](https://github.com/ruaan-deysel/vault/releases/latest)
[![Go Version](https://img.shields.io/badge/go-1.26-00ADD8?logo=go)](https://go.dev/)
[![Svelte](https://img.shields.io/badge/svelte-5-FF3E00?logo=svelte&logoColor=white)](https://svelte.dev/)
[![License](https://img.shields.io/github/license/ruaan-deysel/vault)](https://github.com/ruaan-deysel/vault/blob/main/LICENSE)

Vault is a backup and restore daemon for [Unraid](https://unraid.net/) servers. It protects Docker containers, libvirt VMs, ZFS datasets, folders, and plugins by backing them up to pluggable storage destinations — local disk, SFTP, SMB, NFS, WebDAV, or S3-compatible object storage. Vault ships with a REST API, an MCP server for AI assistants, WebSocket progress streaming, and an integrated web UI built with Svelte 5.

![Vault Dashboard](docs/screenshots/01-dashboard.png)

## Features

**Backup sources**

- Docker containers — image, XML template, and every mapped appdata volume; per-container path exclusions
- libvirt VMs — live snapshot or cold mode, with NVRAM preservation
- ZFS datasets — native `zfs send`/`receive` with snapshot management
- Folders and plugins — any path on the host, plus all installed Unraid plugins
- Stale-item detection — flags items that disappear from the host so jobs stay clean

**Storage destinations**

- Local, SFTP, SMB, NFS, WebDAV, and S3-compatible (AWS S3, Backblaze B2, MinIO, Cloudflare R2, Wasabi, MEGA, IDrive E2)
- Bandwidth throttling per remote destination
- Test-connection and storage-health probes
- Scan + import for backups produced by other Vault instances or AppData Backup

**Backup strategy**

- Full, incremental, and differential chains
- Simple-count retention or Grandfather-Father-Son (`keep_latest`/`daily`/`weekly`/`monthly`/`yearly`)
- AES-256-GCM encryption with per-passphrase key derivation
- Content-defined deduplication (Keyed-FastCDC + per-destination dedup repo) with `vault dedup gc`/`repair` CLI helpers
- Per-run SHA-256 verification and on-demand restore-point verify
- Per-job notifications (success/failure) via Discord webhook

**Scheduling**

- Cron, hourly/daily/weekly/monthly/yearly presets, plus "first/last day of month"
- No-progress stall watchdog per job (cancels only after ~2h of zero bytes moved); no fixed total-job time cap, so long backups that keep transferring are never killed
- Cancellation propagated end-to-end (file I/O, traversal, engine handlers)

**Web UI**

- Dashboard, Jobs, Restore, Storage, History, Replication, Recovery, Logs, Settings
- Live WebSocket progress streaming and runner queue visibility
- Light/dark themes with mobile-responsive layout
- Recovery plan that explains how to rebuild from scratch

**Integration**

- REST API at `/api/v1` with token-based auth for non-loopback callers
- MCP server (streamable HTTP + stdio) for Claude Desktop, Claude Code, and other AI tooling
- Hybrid SQLite snapshot (RAM working DB + persistent snapshot + USB shadow) survives reboots
- Diagnostics bundle export (redacted) for support requests

## Installation

### Unraid Community Applications

Search for **Vault** in the Unraid Community Applications store and click Install.

### Manual Install

Paste this URL into **Plugins > Install Plugin** in the Unraid web UI:

```text
https://raw.githubusercontent.com/ruaan-deysel/vault/main/plugin/vault.plg
```

## Quick Start

1. **Add Storage** — Go to the Storage page and configure a backup destination (local, SFTP, SMB, NFS, WebDAV, or S3)
2. **Create a Job** — Go to the Jobs page, pick what to back up, choose a schedule, and set retention
3. **Run Backup** — Click _Run Now_ or wait for the schedule
4. **Monitor** — Watch live progress on the Dashboard or check the History page for results

## Documentation

| Document                                                         | Description                                           |
| ---------------------------------------------------------------- | ----------------------------------------------------- |
| [Getting Started](docs/getting-started.md)                       | Visual walkthrough of the web UI with screenshots     |
| [Backup Jobs](docs/guides/backup-jobs.md)                        | Job options, scheduling, retention, restore           |
| [Storage Destinations](docs/guides/storage-destinations.md)      | Per-backend configuration with provider notes         |
| [Anomaly Detection](docs/guides/anomaly-detection.md)            | Drift/reliability/capacity alerts + baseline learning |
| [API Reference](docs/api.md)                                     | Full REST API endpoint reference                      |
| [MCP Integration](docs/mcp.md)                                   | Model Context Protocol server for AI tools            |
| [Home Assistant Integration](docs/home-assistant-integration.md) | Sensors, automations, and dashboard cards             |
| [Architecture](docs/architecture.md)                             | Project structure, build commands, deployment         |
| [Changelog](CHANGELOG.md)                                        | Release notes by version                              |

## Requirements

- Unraid 7.0 or newer

## Support and Feedback

- Bug reports: [open a bug report](https://github.com/ruaan-deysel/vault/issues/new?template=01-bug-report.yml)
- Enhancement requests: [request an improvement](https://github.com/ruaan-deysel/vault/issues/new?template=02-enhancement-request.yml)
- Questions and support: [use the Unraid forum support thread](https://forums.unraid.net/topic/197786-plugin-vault-backup-manager)

## License

Vault is licensed under the [MIT License](LICENSE). It is a third-party community plugin for Unraid OS.
