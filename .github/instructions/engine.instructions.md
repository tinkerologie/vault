---
applyTo: "internal/engine/**/*.go"
---

# Engine Instructions

Read `internal/engine/types.go`, every caller in `internal/runner/`, and the
affected handler before editing.

- Implement the current context-aware `Handler` contract exactly.
- Implement `ChunkedHandler` only for engines that participate in deduplicated
  backup and restore.
- Verify current handlers in `internal/engine/` and reuse their shared helpers
  before adding another abstraction.
- Honor context cancellation in walks, SDK/RPC calls, archive work, and copy
  loops.
- Treat progress reporting as best-effort and keep reported progress monotonic
  where a determinate total exists.
- Preserve restart/recovery behavior when a backup temporarily stops a
  container or VM.
- Keep Linux-only code behind build tags and maintain a useful non-Linux stub.
- Validate source and destination paths through existing safe-path helpers.
- Add the smallest regression test that exercises the changed behavior.

Do not copy the interface into this file; the source is authoritative.
