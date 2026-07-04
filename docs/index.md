# Vault

Vault is a backup and restore daemon for [Unraid](https://unraid.net/) servers. It protects Docker containers, libvirt VMs, ZFS datasets, folders, and plugins by backing them up to pluggable storage destinations — local disk, SFTP, SMB, NFS, WebDAV, or S3-compatible object storage. Vault ships with a REST API, an MCP server for AI assistants, WebSocket progress streaming, and an integrated web UI built with Svelte 5.

![Vault Dashboard](screenshots/01-dashboard.png)

**[Get started →](getting-started.md)**

## What you can do

- **Back up more than containers** — Docker containers, libvirt VMs, ZFS datasets, folders, and installed Unraid plugins.
- **Send backups anywhere** — local disk, SFTP, SMB, NFS, WebDAV, or S3-compatible object storage, with per-destination bandwidth throttling.
- **Choose a strategy** — full, incremental, and differential chains with simple-count or Long-Term Retention.
- **Stay safe** — AES-256-GCM encryption, content-defined deduplication, and per-run SHA-256 verification.
- **Monitor in real time** — live WebSocket progress streaming across Dashboard, Jobs, Restore, Storage, History, Replication, Recovery, Logs, and Settings pages.

See [How Backup Works](how-backup-works.md) for the concepts behind these features.

## Integrations

- **Home Assistant** — monitor jobs and trigger backups from your dashboard with the ready-to-use custom integration [`ha-vault`](https://github.com/ruaan-deysel/ha-vault).
- **AI assistants (MCP)** — drive Vault from Claude and other MCP clients via the built-in [MCP server](mcp.md).
- **Scripting & automation** — the full [REST API](api.md) exposes jobs, storage, runs, and restore points.

## Explore the UI

|                                        |                                        |
| -------------------------------------- | -------------------------------------- |
| ![Storage](screenshots/03-storage.png) | ![Jobs](screenshots/04-jobs.png)       |
| ![History](screenshots/05-history.png) | ![Restore](screenshots/06-restore.png) |
