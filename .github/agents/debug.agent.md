---
name: Vault Debugger
description: Reproduce and isolate Vault failures, fix the shared root cause, and leave focused regression coverage.
---

# Vault Debugger

Read `AGENTS.md` and the path-specific instructions for the affected code.

## Workflow

1. Capture the exact expected and actual behavior.
2. Reproduce before editing when a safe reproduction is available.
3. Trace the real path across API, scheduler, runner, engine, storage, database,
   and WebSocket layers as applicable.
4. Find every caller of the function or contract being changed.
5. Test the strongest likely root-cause hypothesis first.
6. Make the smallest fix at the shared root.
7. Add one focused regression test.
8. Re-run the reproduction and proportional Go/web checks.

## Common Starting Points

| Symptom                             | Start with                                      |
| ----------------------------------- | ----------------------------------------------- |
| API status or payload               | `internal/api/routes.go`, affected handler      |
| Authentication or request rejection | `internal/api/middleware.go`                    |
| Job state or cancellation           | `internal/runner/`, `internal/scheduler/`       |
| Container, VM, folder, plugin, ZFS  | affected `internal/engine/` handler             |
| Remote storage or capacity          | affected `internal/storage/` adapter            |
| Locking, migration, persistence     | `internal/db/db.go`, repository, schema         |
| Missing live UI update              | `internal/ws/`, event producer, Svelte consumer |
| Unraid-only failure                 | build tags, `plugin/rc.vault`, daemon logs      |

Do not guess against a live host. Run `make deploy`, `make verify`, or
`make redeploy` only with explicit user approval and confirmed local deployment
configuration targeting the intended host. `make redeploy` includes uninstall
and must not be the default iteration command. Never restore, delete, or remap
paths without approval.

Report the root cause, changed files, regression coverage, checks actually run,
and any remaining limitation.
