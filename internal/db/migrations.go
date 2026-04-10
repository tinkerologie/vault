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
	"ALTER TABLE replication_sources ADD COLUMN api_key TEXT DEFAULT ''",
}
