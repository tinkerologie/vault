---
applyTo: "internal/db/**/*.go"
---

# Database Instructions

Reference: [`AGENTS.md`](../../AGENTS.md) for full project context.

## SQLite Configuration

- Pure Go driver: `modernc.org/sqlite` (no CGO)
- WAL mode with 5s busy timeout
- Foreign keys enabled via PRAGMA

## Schema

Inline in `migrations.go` using `CREATE TABLE IF NOT EXISTS`. No versioned migration framework. Five tables:

- `storage_destinations` — storage backend configs (JSON blob in `config` column)
- `jobs` — backup job definitions
- `job_items` — items (containers/VMs) within a job
- `job_runs` — execution history
- `restore_points` — available restore points

## Repository Pattern

- `job_repo.go` — Job, JobItem, JobRun, RestorePoint CRUD
- `storage_repo.go` — StorageDestination CRUD
- All methods on `*DB` struct

## Conventions

- Always close `rows` with `defer rows.Close()`
- Use `_ = sqlDB.Close()` in error paths
- Scan nullable fields with `sql.NullString`, `sql.NullInt64`
- Return `(int64, error)` for Create operations (returns last insert ID)
- Return `error` for Update/Delete operations
