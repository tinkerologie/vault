# Vault implementation context

Use the [glossary](GLOSSARY.md) for technical terms. See the [architecture decisions](docs/adr/README.md) for the reasons for the design.

- [CLI wiring](internal/cli/daemon.go) starts the daemon and connects its components.
- [Job Intake](internal/jobs/intake.go) applies the same policy to REST and MCP job changes. Import and replication use separate paths.
- [Scheduler](internal/scheduler/) sets job start times. The [Runner](internal/runner/) starts jobs, reports progress, and prevents conflicting restore and maintenance operations.
- [Engine handlers](internal/engine/types.go) do backup and restore operations for each item type. The [storage adapters](internal/storage/adapter.go) connect to storage destinations.
- [Database](internal/db/) stores records and configuration snapshots. The [deduplication code](internal/dedup/) manages shared backup data and its keys.
- [HTTP routes](internal/api/routes.go), [middleware](internal/api/middleware.go), [MCP](internal/mcp/) and [WebSocket hub](internal/ws/) expose the daemon.
- [Web application](web/src/) contains the embedded UI. The [plugin](plugin/) and [Ansible](ansible/) contain the Unraid packaging and deployment code.

See the [architecture overview](docs/architecture.md) for runtime flow, persistence and recovery. Before you change a caller, examine the applicable source code.
