---
status: accepted
---

# Keep working state separate from durable configuration snapshots

Unraid boots from USB flash, so Vault normally uses a RAM-backed working database with pool snapshots and a USB shadow rather than writing each database change to flash. Startup recovery selects a fresh integrity-checked source, with USB-direct operation available when no pool is mounted; this reduces flash writes at the cost of explicit flush and recovery ordering.

Sources: [internal/db/snapshot.go](../../internal/db/snapshot.go), [internal/cli/daemon.go](../../internal/cli/daemon.go).
