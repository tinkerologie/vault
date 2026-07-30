---
applyTo: "internal/db/**/*.go"
---

# Database Instructions

Read `internal/db/db.go`, `internal/db/migrations.go`, and the affected
repository file before editing.

- Use `modernc.org/sqlite`; Vault must remain CGO-free.
- Preserve the DSN pragmas in `Open`: immediate transactions, 30-second busy
  timeout, WAL, foreign keys, and the WAL size limit.
- `DB` owns an atomic `*sql.DB` handle so restore can reopen the database.
  Route operations through `DB` methods rather than caching the underlying
  handle.
- Schema changes remain additive and idempotent. This repository does not use a
  versioned migration framework.
- Use bound parameters for values; never interpolate untrusted data into SQL.
- Use context-aware query methods when the caller has a context.
- Close rows and check iteration/scan errors.
- Preserve foreign-key and restore/snapshot semantics when changing data
  relationships.
- Use `t.TempDir()` databases when filesystem, reopen, WAL, or multi-handle
  behavior matters. `:memory:` is acceptable for isolated single-handle tests.

Do not duplicate the table inventory here; `internal/db/migrations.go` is the
authoritative schema.
