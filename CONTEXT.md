# Domain vocabulary

The names Vault's code, docs and UI use for the things it manages. Use these
terms exactly; where a term names a module, the module is the one place that
concept's rules live.

Architecture vocabulary — module, interface, depth, seam, adapter, leverage,
locality — is separate and is not defined here.

## Backup configuration

**Job** — a unit of backup configuration: what to back up, where to put it, on
what schedule, and how long to keep it. Owns compression, encryption,
retention, verification and notification settings.

**Job Item** — one thing inside a Job: a container, a VM, a folder, or the
flash drive. Carries its own settings blob.

**Job Run** — one execution of a Job, with its own status, timings, per-item
outcomes and streaming run log.

**Job Intake** (`internal/jobs`) — the module every Job write passes through.
It validates and normalises the request, persists the Job and its Items,
reloads the scheduler, flushes the database to flash, and broadcasts the
change — as one indivisible operation. The REST handlers and the MCP tools are
adapters over it and share a single instance, so both enforce the same rules by
construction. Job Item edits are not yet part of it.

**Restore Point** — the artefact one successful Job Run produced, and the unit
retention keeps or removes.

**Destination** — where backups are written: local, SFTP, SMB, NFS, S3 or
WebDAV. Each is an adapter behind `storage.Adapter`.

## Runtime

**Scheduler** (`internal/scheduler`) — the cron engine. It reads Jobs at start
and on an explicit reload, and never refreshes on its own; a Job change that
does not reload it does not take effect.

**Runner** (`internal/runner`) — executes Job Runs and restores, one at a time,
and reports live status.

**Backup Item** — a Job Item as seen at run time, resolved to the concrete
containers, disks or paths the engine will archive.

**Anomaly** — a detected deviation from a Job's established baseline (size,
duration, item count) or a Destination's capacity trajectory.

**Replication Source** — another Vault instance whose Jobs and Restore Points
this one mirrors. Importing from one creates local Jobs directly, deliberately
disabled and outside Job Intake: the input is a manifest rather than a caller's
request, and imported Jobs must not be scheduled.
