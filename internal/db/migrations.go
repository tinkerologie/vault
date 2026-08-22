package db

const schema = `
CREATE TABLE IF NOT EXISTS storage_destinations (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	type TEXT NOT NULL,
	config TEXT NOT NULL DEFAULT '{}',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS jobs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	description TEXT DEFAULT '',
	enabled INTEGER DEFAULT 1,
	schedule TEXT DEFAULT '',
	backup_type_chain TEXT DEFAULT 'full',
	retention_count INTEGER DEFAULT 7,
	retention_days INTEGER DEFAULT 30,
	compression TEXT DEFAULT 'zstd',
	compression_level TEXT DEFAULT '',
	container_mode TEXT DEFAULT 'one_by_one',
	vm_mode TEXT DEFAULT 'snapshot',
	pre_script TEXT DEFAULT '',
	post_script TEXT DEFAULT '',
	notify_on TEXT DEFAULT 'failure',
	verify_backup INTEGER DEFAULT 1,
	storage_dest_id INTEGER REFERENCES storage_destinations(id),
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS job_items (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	item_type TEXT NOT NULL,
	item_name TEXT NOT NULL,
	item_id TEXT NOT NULL,
	settings TEXT DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS job_runs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	status TEXT NOT NULL DEFAULT 'running',
	backup_type TEXT NOT NULL,
	started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	completed_at DATETIME,
	log TEXT DEFAULT '',
	items_total INTEGER DEFAULT 0,
	items_done INTEGER DEFAULT 0,
	items_failed INTEGER DEFAULT 0,
	size_bytes INTEGER DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_job_runs_job_started ON job_runs(job_id, started_at DESC);

CREATE TABLE IF NOT EXISTS restore_points (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	job_run_id INTEGER NOT NULL REFERENCES job_runs(id) ON DELETE CASCADE,
	job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	backup_type TEXT NOT NULL,
	storage_path TEXT NOT NULL,
	metadata TEXT DEFAULT '{}',
	size_bytes INTEGER DEFAULT 0,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS activity_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	level TEXT NOT NULL DEFAULT 'info',
	category TEXT NOT NULL DEFAULT 'system',
	message TEXT NOT NULL,
	details TEXT DEFAULT '',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_activity_log_ts ON activity_log(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_activity_log_cat ON activity_log(category);

CREATE TABLE IF NOT EXISTS run_log_entries (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id INTEGER NOT NULL REFERENCES job_runs(id) ON DELETE CASCADE,
	ts DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	level TEXT NOT NULL DEFAULT 'info',
	message TEXT NOT NULL,
	data TEXT DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_run_log_entries_run ON run_log_entries(run_id, id);

CREATE TABLE IF NOT EXISTS verify_runs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	restore_point_id INTEGER NOT NULL REFERENCES restore_points(id) ON DELETE CASCADE,
	mode TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'running',
	files_checked INTEGER NOT NULL DEFAULT 0,
	files_failed INTEGER NOT NULL DEFAULT 0,
	bytes_read INTEGER NOT NULL DEFAULT 0,
	started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	completed_at DATETIME,
	error_summary TEXT DEFAULT ''
);

CREATE TABLE IF NOT EXISTS replication_sources (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	url TEXT NOT NULL,
	storage_dest_id INTEGER REFERENCES storage_destinations(id),
	schedule TEXT DEFAULT '',
	enabled INTEGER DEFAULT 1,
	last_sync_at DATETIME,
	last_sync_status TEXT DEFAULT '',
	last_sync_error TEXT DEFAULT '',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS dedup_packs (
	id           TEXT PRIMARY KEY,
	storage_id   INTEGER NOT NULL,
	path         TEXT NOT NULL,
	size_bytes   INTEGER NOT NULL,
	chunk_count  INTEGER NOT NULL,
	created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	-- Set once GC has written this pack's tombstone but before the blob and
	-- row are gone. HasDedupChunk ignores chunks belonging to a flagged pack,
	-- so a retried delete can never leave the chunks advertised for reuse.
	pending_delete INTEGER DEFAULT 0,
	FOREIGN KEY (storage_id) REFERENCES storage_destinations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_dedup_packs_storage ON dedup_packs(storage_id);

CREATE TABLE IF NOT EXISTS dedup_chunks (
	chunk_id     BLOB NOT NULL,
	storage_id   INTEGER NOT NULL,
	pack_id      TEXT NOT NULL,
	offset       INTEGER NOT NULL,
	length       INTEGER NOT NULL,
	PRIMARY KEY (storage_id, chunk_id),
	FOREIGN KEY (storage_id) REFERENCES storage_destinations(id) ON DELETE CASCADE,
	FOREIGN KEY (pack_id) REFERENCES dedup_packs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_dedup_chunks_pack ON dedup_chunks(storage_id, pack_id);

CREATE TABLE IF NOT EXISTS dedup_gc_runs (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	storage_id       INTEGER NOT NULL,
	started_at       DATETIME NOT NULL,
	completed_at     DATETIME NOT NULL,
	reachable        INTEGER NOT NULL DEFAULT 0,
	freed_packs      INTEGER NOT NULL DEFAULT 0,
	freed_bytes      INTEGER NOT NULL DEFAULT 0,
	rewritable_bytes INTEGER NOT NULL DEFAULT 0,
	error_count      INTEGER NOT NULL DEFAULT 0,
	compacted_packs  INTEGER NOT NULL DEFAULT 0,
	reclaimed_bytes  INTEGER NOT NULL DEFAULT 0,
	FOREIGN KEY (storage_id) REFERENCES storage_destinations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_dedup_gc_runs_storage ON dedup_gc_runs(storage_id, completed_at DESC);

CREATE TABLE IF NOT EXISTS anomalies (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	fingerprint     TEXT NOT NULL,
	detector        TEXT NOT NULL,
	severity        TEXT NOT NULL,
	scope_kind      TEXT NOT NULL,
	scope_id        INTEGER NOT NULL,
	metric          TEXT NOT NULL,
	observed        REAL NOT NULL,
	expected        REAL,
	deviation       REAL,
	job_run_id      INTEGER,
	summary         TEXT NOT NULL,
	details         TEXT NOT NULL DEFAULT '',
	state           TEXT NOT NULL,
	first_seen_at   DATETIME NOT NULL,
	last_seen_at    DATETIME NOT NULL,
	resolved_at     DATETIME,
	acknowledged_at DATETIME,
	ack_action      TEXT,
	ack_by          TEXT,
	ack_reason      TEXT,
	notified_at     DATETIME
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_anomalies_open_fingerprint ON anomalies(fingerprint) WHERE state = 'open';
CREATE INDEX IF NOT EXISTS idx_anomalies_state_severity ON anomalies(state, severity, last_seen_at DESC);
CREATE INDEX IF NOT EXISTS idx_anomalies_scope ON anomalies(scope_kind, scope_id, last_seen_at DESC);
CREATE INDEX IF NOT EXISTS idx_anomalies_job_run ON anomalies(job_run_id) WHERE job_run_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS job_baselines (
	job_id          INTEGER PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
	sample_count    INTEGER NOT NULL,
	bytes_median    REAL NOT NULL,
	bytes_mad       REAL NOT NULL,
	duration_median REAL NOT NULL,
	duration_mad    REAL NOT NULL,
	failure_rate    REAL NOT NULL,
	updated_at      DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS destination_capacity_samples (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	dest_id     INTEGER NOT NULL REFERENCES storage_destinations(id) ON DELETE CASCADE,
	sampled_at  DATETIME NOT NULL,
	free_bytes  INTEGER NOT NULL,
	total_bytes INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_capacity_samples_dest_time ON destination_capacity_samples(dest_id, sampled_at DESC);

-- Add verify_backup column if it does not exist.
-- SQLite does not support IF NOT EXISTS for ALTER TABLE, so we
-- attempt the ALTER in Go and silently ignore "duplicate column" errors.
`

// alterMigrations are idempotent ALTER TABLE statements for columns added
// after the initial schema. Errors (e.g. duplicate column) are expected.
var alterMigrations = []string{
	"ALTER TABLE jobs ADD COLUMN verify_backup INTEGER DEFAULT 1",
	"ALTER TABLE job_items ADD COLUMN sort_order INTEGER DEFAULT 0",
	"ALTER TABLE jobs ADD COLUMN encryption TEXT DEFAULT 'none'",
	"ALTER TABLE restore_points ADD COLUMN parent_restore_point_id INTEGER DEFAULT 0",
	"ALTER TABLE jobs ADD COLUMN vm_mode TEXT DEFAULT 'snapshot'",
	"ALTER TABLE jobs ADD COLUMN source_id INTEGER DEFAULT 0",
	"ALTER TABLE jobs ADD COLUMN compression_level TEXT DEFAULT ''",
	"ALTER TABLE restore_points ADD COLUMN source_id INTEGER DEFAULT 0",
	"ALTER TABLE job_runs ADD COLUMN run_type TEXT DEFAULT 'backup'",
	"ALTER TABLE replication_sources ADD COLUMN type TEXT DEFAULT 'remote_vault'",
	"ALTER TABLE replication_sources ADD COLUMN config TEXT DEFAULT '{}'",
	"ALTER TABLE jobs ADD COLUMN defer_remote_upload INTEGER DEFAULT 0",
	// Marks a dedup pack whose tombstone is written but whose blob/row
	// deletion has not completed. Keeps its chunks out of HasDedupChunk so a
	// failed delete cannot leave them advertised for reuse by a later backup.
	"ALTER TABLE dedup_packs ADD COLUMN pending_delete INTEGER DEFAULT 0",
	// Long-Term Retention (LTR) buckets. Each defaults to 0 meaning
	// "ignore this bucket". If any of the five is > 0 the runner uses the
	// LTR algorithm and ignores retention_count / retention_days.
	"ALTER TABLE jobs ADD COLUMN keep_latest INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE jobs ADD COLUMN keep_daily INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE jobs ADD COLUMN keep_weekly INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE jobs ADD COLUMN keep_monthly INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE jobs ADD COLUMN keep_yearly INTEGER NOT NULL DEFAULT 0",
	// Storage destination health tracking (Feature F). Refreshed by the
	// daily HealthChecker; surfaced in the UI as a per-destination badge.
	"ALTER TABLE storage_destinations ADD COLUMN last_health_check_at DATETIME",
	"ALTER TABLE storage_destinations ADD COLUMN last_health_check_status TEXT DEFAULT ''",
	"ALTER TABLE storage_destinations ADD COLUMN last_health_check_error TEXT DEFAULT ''",
	// Scheduled verification (Feature A). verify_schedule is a cron
	// expression; verify_mode is "quick" or "deep". Both empty means no
	// scheduled verification for that job.
	"ALTER TABLE jobs ADD COLUMN verify_schedule TEXT DEFAULT ''",
	"ALTER TABLE jobs ADD COLUMN verify_mode TEXT DEFAULT 'quick'",
	// Deduplication (Feature D). dedup_enabled toggles content-defined
	// chunking + pack-based storage on a destination. manifest_id holds
	// the SHA-256 (or similar) of the per-restore-point manifest blob;
	// NULL means the restore point is not dedup-backed.
	"ALTER TABLE storage_destinations ADD COLUMN dedup_enabled INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE restore_points       ADD COLUMN manifest_id   BLOB    DEFAULT NULL",
	// Resilience hardening (spec 2026-05-22) — additive schema.
	"ALTER TABLE jobs ADD COLUMN retry_max_override INTEGER DEFAULT NULL",
	"ALTER TABLE jobs ADD COLUMN retry_delays_override TEXT DEFAULT NULL",
	"ALTER TABLE job_runs ADD COLUMN retry_of_run_id INTEGER DEFAULT NULL",
	"ALTER TABLE job_runs ADD COLUMN retry_attempt INTEGER DEFAULT 0",
	"ALTER TABLE job_runs ADD COLUMN retry_next_at TIMESTAMP DEFAULT NULL",
	"ALTER TABLE storage_destinations ADD COLUMN consecutive_failures INTEGER DEFAULT 0",
	"ALTER TABLE storage_destinations ADD COLUMN breaker_state TEXT DEFAULT 'closed'",
	"ALTER TABLE storage_destinations ADD COLUMN breaker_opened_at TIMESTAMP DEFAULT NULL",
	"ALTER TABLE storage_destinations ADD COLUMN backup_database_enabled INTEGER DEFAULT 0",
	// Capacity probe (spec 2026-05-26): per-destination space accounting
	// refreshed daily alongside the health check. capacity_total_bytes == 0
	// means "quota unknown" (S3, generic WebDAV). capacity_source identifies
	// the probe method that produced the numbers (statfs, webdav-quota,
	// sftp-statvfs, smb-fsctl, s3-list-sum). capacity_error carries the most
	// recent probe failure for support reports; empty on success.
	"ALTER TABLE storage_destinations ADD COLUMN capacity_total_bytes INTEGER",
	"ALTER TABLE storage_destinations ADD COLUMN capacity_used_bytes  INTEGER",
	"ALTER TABLE storage_destinations ADD COLUMN capacity_free_bytes  INTEGER",
	"ALTER TABLE storage_destinations ADD COLUMN capacity_probed_at   TIMESTAMP",
	"ALTER TABLE storage_destinations ADD COLUMN capacity_source      TEXT DEFAULT ''",
	"ALTER TABLE storage_destinations ADD COLUMN capacity_error       TEXT DEFAULT ''",
	// Dedup GC compaction counters (Task 5). Added after initial dedup_gc_runs
	// table creation so existing on-disk DBs gain both columns automatically.
	"ALTER TABLE dedup_gc_runs ADD COLUMN compacted_packs INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE dedup_gc_runs ADD COLUMN reclaimed_bytes INTEGER NOT NULL DEFAULT 0",
	// Anomaly detection (2026-05-30): per-job and per-destination sensitivity
	// override. Empty string means "use the global anomaly_sensitivity_default".
	"ALTER TABLE jobs ADD COLUMN anomaly_sensitivity TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE storage_destinations ADD COLUMN anomaly_sensitivity TEXT NOT NULL DEFAULT ''",
	// Per-job upload concurrency (storage resilience). DEFAULT 1 means serial for
	// existing rows; new jobs that don't set the field explicitly get the default
	// of 3 from EffectiveUploadConcurrency (0 sentinel → 3).
	"ALTER TABLE jobs ADD COLUMN max_parallel_uploads INTEGER DEFAULT 1",
	"ALTER TABLE jobs ADD COLUMN adaptive_enabled INTEGER NOT NULL DEFAULT 0",
	// Stale-item remediation (#119). missing_since is set (RFC3339) when a
	// backup run detects the item no longer exists on the system; NULL means
	// present/healthy. Never auto-removed — the user clears it by removing
	// the item via the remediation endpoint.
	"ALTER TABLE job_items ADD COLUMN missing_since TEXT",
	// Stall visibility (#265). stalled_at is set when the runner's watchdog
	// decides a run has stopped making progress, and stall_reason carries the
	// operator-facing explanation. Both stay NULL/'' for healthy runs. They
	// are advisory: the row's status remains 'running' because the run has not
	// actually finished, but the API can now tell a working backup apart from
	// a wedged one instead of both reading as "running". TIMESTAMP, matching
	// retry_next_at, so the driver hands back a time.Time.
	"ALTER TABLE job_runs ADD COLUMN stalled_at TIMESTAMP DEFAULT NULL",
	"ALTER TABLE job_runs ADD COLUMN stall_reason TEXT DEFAULT ''",
	// Replication sync summary (#287). The per-sync counters were only ever
	// broadcast on the completion WebSocket event, so a page load (or the 10s
	// poll) could not tell "connected but nothing new to replicate" apart from
	// a sync that actually transferred data. Persist the last run's counters
	// plus the last time a sync fully succeeded so the UI can show an explicit
	// up-to-date / synced / failed state with per-item numbers.
	"ALTER TABLE replication_sources ADD COLUMN last_sync_jobs_synced INTEGER DEFAULT 0",
	"ALTER TABLE replication_sources ADD COLUMN last_sync_jobs_failed INTEGER DEFAULT 0",
	"ALTER TABLE replication_sources ADD COLUMN last_sync_restore_points INTEGER DEFAULT 0",
	"ALTER TABLE replication_sources ADD COLUMN last_sync_bytes INTEGER DEFAULT 0",
	"ALTER TABLE replication_sources ADD COLUMN last_sync_success_at DATETIME",
}

// dataMigrations are idempotent row rewrites, applied after alterMigrations.
// Unlike the ALTER statements these are not expected to fail, so errors are
// logged rather than swallowed.
var dataMigrations = []string{
	// container_mode canonicalisation (#261). The API's allow-list once named
	// this mode "all_at_once", a value nothing else in the codebase used — the
	// runner has only ever compared against "stop_all", so such a job silently
	// ran sequentially. Converting every row here, once, at upgrade keeps the
	// behaviour change visible and uniform; repairing rows lazily on save
	// would instead make identical data behave differently depending on which
	// jobs an operator happened to edit.
	"UPDATE jobs SET container_mode = 'stop_all' WHERE container_mode = 'all_at_once'",
	// Replication last-successful-sync backfill (#287). last_sync_success_at is
	// a new column, so existing sources that already completed a successful
	// sync before the upgrade would report no successful sync until the next
	// run. Seed it from last_sync_at for rows whose last recorded status was a
	// success, so the UI shows the historical timestamp immediately.
	"UPDATE replication_sources SET last_sync_success_at = last_sync_at WHERE last_sync_success_at IS NULL AND last_sync_status = 'success' AND last_sync_at IS NOT NULL",
	// Anomaly activity category + level normalization (#328 r3 #11, #12).
	// Round 1 (7e62338) fixed the code to write anomaly activity rows at WARN,
	// but rows written before that still sit in activity_log with the
	// non-canonical category 'anomaly' (the console's categories are backup,
	// restore, health, system) and/or level 'info'. Move every anomaly row
	// under the canonical "health" category — anomaly detection is the
	// run/storage health-monitoring subsystem — and promote its remaining
	// INFO rows to WARN so anomaly reports never surface as INFO. The level
	// UPDATE is scoped to anomaly messages so legitimate INFO rows from the
	// health subsystem ("Storage health check ... status=ok", "Health check
	// job=...") are left untouched. Both statements are idempotent: re-running
	// them is a no-op.
	"UPDATE activity_log SET category = 'health' WHERE category = 'anomaly'",
	"UPDATE activity_log SET level = 'warn' WHERE category = 'health' AND level = 'info' AND (message LIKE 'Anomaly %' OR message LIKE '%anomaly(s) acknowledged%')",
}
