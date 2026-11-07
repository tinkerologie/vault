---
status: accepted
---

# Keep working state separate from durable configuration snapshots

Unraid boots from USB flash. Vault normally writes database changes to a working database in RAM. It saves pool snapshots and a USB shadow. At startup, Vault selects the newest valid source. If no pool is mounted, Vault can use the database directly on USB. This reduces flash writes but requires a defined order for flush and recovery.

Sources: [internal/db/snapshot.go](../../internal/db/snapshot.go), [internal/cli/daemon.go](../../internal/cli/daemon.go).
