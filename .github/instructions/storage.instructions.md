---
applyTo: "internal/storage/**/*.go"
---

# Storage Instructions

Read `internal/storage/adapter.go`, `internal/storage/factory.go`, and the
affected provider before editing.

- Implement every method in the current `Adapter` interface, including
  retry-safe writes, ranged reads, capacity reporting, and usage reporting.
- Construct adapters through `NewAdapter`/`NewAdapterWithOptions`; configuration
  is stored as a JSON string.
- Verify supported providers in the factory switch and implementation files.
- Preserve the factory middleware order: provider, throttle, retry, metrics,
  logging.
- `WriteFrom` must reopen a fresh source stream for each retry.
- `ReadRange` must release underlying handles and honor its EOF/range contract.
- Return `ErrUsageNotSupported` when a provider cannot report free/total space.
- Validate storage-relative paths and prevent traversal.
- Close remote sessions, files, mounts, and response bodies on every path.
- Never log passwords, tokens, private keys, or complete configuration blobs.
- Test connection checks must clean up their own artifacts.

Do not duplicate the interface or provider configuration structs here; source
files are authoritative.
