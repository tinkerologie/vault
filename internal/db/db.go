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

// insertDefaultSettings seeds key/value rows for the resilience hardening
// settings introduced by the 2026-05-22 migration. INSERT OR IGNORE makes
// this safe to call on every Open.
func (d *DB) insertDefaultSettings() error {
	defaults := []struct{ key, value string }{
		{"retry_max_default", "2"},
		{"retry_delays_default", "[900,3600,14400]"},
		{"breaker_fail_threshold", "3"},
		{"breaker_close_successes", "2"},
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
