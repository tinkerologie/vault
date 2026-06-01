package db

import (
	"database/sql"
	"errors"
	"fmt"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type DB struct {
	*sql.DB
	path string
}

// Path returns the filesystem path of the database.
func (d *DB) Path() string {
	return d.path
}

func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=30000&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite only supports a single writer. Limiting connections to 1
	// prevents "database is locked" errors from concurrent goroutines.
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if _, err := sqlDB.Exec("PRAGMA foreign_keys = ON"); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	// Bound the WAL file size between checkpoints so a write burst does
	// not leave a permanently-large WAL on disk. 64 MiB is comfortable
	// for Vault's write rate.
	if _, err := sqlDB.Exec(`PRAGMA journal_size_limit = 67108864`); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("setting journal_size_limit: %w", err)
	}

	if _, err := sqlDB.Exec(schema); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	// Run idempotent ALTER TABLE migrations for new columns.
	for _, m := range alterMigrations {
		_, _ = sqlDB.Exec(m) //nolint:errcheck // duplicate column errors expected
	}

	d := &DB{DB: sqlDB, path: path}
	if err := d.insertDefaultSettings(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("seed default settings: %w", err)
	}
	return d, nil
}

// Vacuum reclaims free space in the database file.
func (d *DB) Vacuum() error {
	_, err := d.Exec("VACUUM")
	return err
}

// IntegrityCheck runs PRAGMA integrity_check and returns nil if the
// result is "ok". Otherwise returns an error containing the first
// failure line. Use this after restoring a snapshot to confirm the
// on-disk file is not corrupt before promoting it to the working DB.
func (d *DB) IntegrityCheck() error {
	rows, err := d.Query(`PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return fmt.Errorf("integrity_check: empty result")
	}
	var first string
	if err := rows.Scan(&first); err != nil {
		return fmt.Errorf("integrity_check scan: %w", err)
	}
	if first != "ok" {
		return fmt.Errorf("integrity_check failed: %s", first)
	}
	return nil
}

// insertDefaultSettings seeds key/value rows for settings introduced by schema
// migrations. INSERT OR IGNORE makes this safe to call on every Open.
func (d *DB) insertDefaultSettings() error {
	defaults := []struct{ key, value string }{
		{"retry_max_default", "2"},
		{"retry_delays_default", "[900,3600,14400]"},
		{"breaker_fail_threshold", "3"},
		{"breaker_close_successes", "2"},
		{"dedup_compaction_min_dead_ratio", "0.5"},
		// Anomaly detection defaults (2026-05-30).
		{"anomaly_detection_enabled", "true"},
		{"anomaly_sensitivity_default", "balanced"},
		{"anomaly_notify_min_severity", "critical"},
		// Storage resilience defaults (2026-06-01).
		{"storage_verbose_logging", "false"},
	}
	for _, kv := range defaults {
		if _, err := d.Exec(
			`INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)`,
			kv.key, kv.value,
		); err != nil {
			return fmt.Errorf("seeding setting %s: %w", kv.key, err)
		}
	}
	return nil
}
