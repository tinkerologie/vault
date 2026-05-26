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
	"ALTER TABLE restore_points ADD COLUMN source_id INTEGER DEFAULT 0",
	"ALTER TABLE job_runs ADD COLUMN run_type TEXT DEFAULT 'backup'",
	"ALTER TABLE replication_sources ADD COLUMN type TEXT DEFAULT 'remote_vault'",
	"ALTER TABLE replication_sources ADD COLUMN config TEXT DEFAULT '{}'",
	"ALTER TABLE jobs ADD COLUMN defer_remote_upload INTEGER DEFAULT 0",
	// GFS (grandfather-father-son) retention. Each defaults to 0 meaning
	// "ignore this bucket". If any of the five is > 0 the runner uses the
	// GFS algorithm and ignores retention_count / retention_days.
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
}
