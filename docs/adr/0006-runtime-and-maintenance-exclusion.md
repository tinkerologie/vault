---
status: accepted
---

# Serialize conflicting runtime and offline maintenance work

The Runner coordinates conflicting backups, restores and maintenance work, and tracks active operations for shutdown drain, while individual operations may still use internal concurrency. Standalone deduplication maintenance uses the daemon maintenance lock and fails closed if it cannot acquire it, accepting reduced simultaneous access to protect shared backup state.

Sources: [internal/runner/runner.go](../../internal/runner/runner.go), [internal/cli/dedup.go](../../internal/cli/dedup.go).
