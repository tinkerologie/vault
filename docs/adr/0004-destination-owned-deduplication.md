---
status: accepted
---

# Own deduplicated content at the Destination

Deduplicated jobs on the same destination share its backup repository and keys. The jobs can share chunks. Deletion and garbage collection must keep all chunks that a restore point still uses. To recover the repository, use its keys or the supported backup-password escrow.

Sources: [internal/dedup/repo.go](../../internal/dedup/repo.go), [internal/dedup/escrow.go](../../internal/dedup/escrow.go).
