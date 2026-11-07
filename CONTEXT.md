# Vault implementation context

Use [GLOSSARY.md](GLOSSARY.md) for domain language and [architecture decisions](docs/adr/README.md) for recorded trade-offs. This file maps responsibilities to their owning source.

- [CLI wiring](internal/cli/daemon.go) assembles daemon lifetime, persistence and runtime dependencies.
- [Job Intake](internal/jobs/intake.go) shares REST/MCP Job creation, update and deletion policy; import and replication have distinct ownership rules.
- [Scheduler](internal/scheduler/) decides when work becomes due; [Runner](internal/runner/) owns execution, progress, restore and maintenance exclusion.
- [Engine handlers](internal/engine/types.go) implement workload operations; [storage adapters](internal/storage/adapter.go) implement access to Destinations.
- [Database](internal/db/) owns records and configuration snapshots; [deduplication](internal/dedup/) owns shared backup content and its keys.
- [HTTP routes](internal/api/routes.go), [middleware](internal/api/middleware.go), [MCP](internal/mcp/) and [WebSocket hub](internal/ws/) expose the daemon.
- [Web application](web/src/) provides the embedded UI; [plugin](plugin/) and [Ansible](ansible/) own Unraid packaging and deployment.

See the [architecture overview](docs/architecture.md) for runtime flow, persistence and recovery. Verify contracts in source before changing callers.
