---
status: accepted
---

# Ship a pure Go daemon with an embedded UI

Vault ships a statically built Go daemon with its prebuilt UI embedded, avoiding runtime Node.js and CGO dependencies on Unraid. Linux-specific workload operations use platform-specific implementations so portable recovery and development builds remain possible.

Sources: [Makefile](../../Makefile), [internal/engine/types.go](../../internal/engine/types.go).
