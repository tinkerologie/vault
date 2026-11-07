---
status: accepted
---

# Own deduplicated content at the Destination

Deduplicated Jobs sharing a Destination share its backup repository and key ownership rather than keeping independent per-Job content stores. This permits cross-Job reuse but makes deletion and garbage collection responsible for preserving every retained reference, and recovery dependent on the repository keys or supported passphrase escrow.

Sources: [internal/dedup/repo.go](../../internal/dedup/repo.go), [internal/dedup/escrow.go](../../internal/dedup/escrow.go).
