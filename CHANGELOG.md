# Changelog

All notable changes to the Vault plugin will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project uses date-based versioning (`YYYY.MM.PATCH`).

## [Unreleased]

## [2026.03.02] - 2026-03-19

### Added

- Cleaned up documentation.

### Fixed

- Tailscale enabled containers backups failing.
- UI/UX fixes and polish.

## [2026.03.01] - 2026-03-18

### Added

- MCP tools for plugin discovery and runner status
- Restore-point chain health annotations in the API and MCP restore-point listings

### Changed

- MCP health output now includes version and mode, aligned with the REST `/health` response
- README refreshed to document the current REST API, MCP transports, and tool coverage
- `make verify` now exercises MCP streamable HTTP via the official Go SDK client
- Release packaging now targets `.txz` bundles with SHA256 verification and release automation updates the PLG checksum accordingly
- The PLG now advertises `project` and `readme` metadata and prunes stale cached plugin bundles during install

### Fixed

- Restore-point docs now reflect chain health and retention-preserved parents
- Repository URLs now point at `ruaan-deysel/vault`
- UI Fixes

## [2026.03.00] - 2026-03-02

### Added in 2026.03.00

- Full backup/restore engine for Docker containers, libvirt VMs, and folders
- Storage backends: Local, SMB, NFS, SFTP, NFS
- Cron-based job scheduling with retention policies
- Svelte 5 web UI with real-time WebSocket progress
- API key authentication and TLS support
- Encrypted storage credentials (AES-256-GCM sealed passphrase)
- Replication: sync restore points to remote Vault instances
- Job duplication with one-click copy
- Mobile-responsive bottom navigation bar
- Pull-to-refresh on Dashboard and History pages
- Theme cycle keyboard shortcut (Ctrl+Shift+L)
- Aria-labels on all icon-only action buttons
- Backup size trend chart (filters to completed runs only)
- Ansible-driven build/deploy/verify pipeline
- Proper Unraid plugin bundle pattern with MD5 verification
- GitHub Actions release workflow with automatic PLG MD5 update

### Fixed in 2026.03.00

- Backup trend chart no longer includes failed runs with partial sizes

## [0.1.0] - 2025-01-01

### Added in 0.1.0

- Initial release
- Docker container backup and restore (full image + config + appdata)
- VM backup and restore (live snapshot + cold backup)
- Storage destinations: Local, SMB, NFS, SFTP, NFS
- Full, incremental, and differential backup types
- Job scheduling with retention policies
- Web UI with Dashboard, Jobs, Restore, Storage, History, Settings

[Unreleased]: https://github.com/ruaan-deysel/vault/compare/v2026.03.00...HEAD
[2026.03.00]: https://github.com/ruaan-deysel/vault/releases/tag/v2026.03.00
[0.1.0]: https://github.com/ruaan-deysel/vault/releases/tag/v0.1.0
