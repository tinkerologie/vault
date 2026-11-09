# Architecture decisions

The initial records describe the architecture in release [v2026.11.00](https://github.com/tinkerologie/vault/tree/v2026.11.00). The source code and operating conditions give the reasons for the decisions.

See the [glossary](../../GLOSSARY.md), [implementation context](../../CONTEXT.md) and [architecture overview](../architecture.md). Record a new, linked ADR when superseding a decision.

- [0001: Ship a pure Go daemon with an embedded UI](0001-pure-go-daemon-and-embedded-ui.md)

- [0002: Keep working state separate from durable configuration snapshots](0002-hybrid-configuration-persistence.md)

- [0003: Share Job mutation policy across REST and MCP](0003-shared-job-intake.md)

- [0004: Own deduplicated content at the Destination](0004-destination-owned-deduplication.md)

- [0005: Distinguish host-local trust from remote access](0005-host-trust-and-secret-ownership.md)

- [0006: Serialize conflicting runtime and offline maintenance work](0006-runtime-and-maintenance-exclusion.md)

- [0007: Commit a Job and its items together](0007-atomic-job-aggregate-mutation.md)
