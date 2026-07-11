package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sync/atomic"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

// DB wraps *sql.DB behind an atomic pointer so Reopen can swap the handle
// in place without a data race against concurrent queries. All query
// methods forward to the current handle.
type DB struct {
	handle atomic.Pointer[sql.DB]
	path   string
}

// sql returns the current underlying handle.
func (d *DB) sql() *sql.DB { return d.handle.Load() }

// Thin forwarders for the *sql.DB methods the codebase calls. They keep
// every existing call site compiling unchanged now that the handle is no
// longer an embedded field.

func (d *DB) Exec(query string, args ...any) (sql.Result, error) {
	return d.sql().Exec(query, args...)
}

func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.sql().ExecContext(ctx, query, args...)
}

func (d *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return d.sql().Query(query, args...)
}

func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.sql().QueryContext(ctx, query, args...)
}

func (d *DB) QueryRow(query string, args ...any) *sql.Row {
	return d.sql().QueryRow(query, args...)
}

func (d *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.sql().QueryRowContext(ctx, query, args...)
}

func (d *DB) Begin() (*sql.Tx, error) { return d.sql().Begin() }

func (d *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return d.sql().BeginTx(ctx, opts)
}

func (d *DB) Ping() error { return d.sql().Ping() }

func (d *DB) PingContext(ctx context.Context) error { return d.sql().PingContext(ctx) }

func (d *DB) Conn(ctx context.Context) (*sql.Conn, error) { return d.sql().Conn(ctx) }

func (d *DB) Close() error { return d.sql().Close() }

// Path returns the filesystem path of the database.
func (d *DB) Path() string {
	return d.path
}

func Open(path string) (*DB, error) {
	// modernc.org/sqlite only understands the _pragma=name(value) DSN form
	// (plus _txlock/_time_format). The old _journal_mode=/_busy_timeout=
	// params were silently ignored, leaving fresh databases in rollback
	// journal mode with no busy timeout. DSN pragmas apply to every
	// connection database/sql opens, unlike a one-off Exec.
	// journal_size_limit bounds how large the WAL file stays after
	// checkpoints; it can still grow past it during long transactions.
	sqlDB, err := sql.Open("sqlite", path+"?_txlock=immediate"+
		"&_pragma=busy_timeout(30000)"+
		"&_pragma=journal_mode(WAL)"+
		"&_pragma=foreign_keys(ON)"+
		"&_pragma=journal_size_limit(67108864)")
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

	// The journal_mode pragma reports the resulting mode instead of erroring
	// when WAL can't be enabled (e.g. a filesystem without shared-memory
	// support), and the driver discards that result — so check explicitly.
	var mode string
	if err := sqlDB.QueryRow("PRAGMA journal_mode").Scan(&mode); err == nil && mode != "wal" {
		log.Printf("Warning: database %s runs in journal_mode=%s — WAL is unavailable on this filesystem; access under contention will be slower", path, mode)
	}

	if _, err := sqlDB.Exec(schema); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	// Run idempotent ALTER TABLE migrations for new columns.
	for _, m := range alterMigrations {
		_, _ = sqlDB.Exec(m) //nolint:errcheck // duplicate column errors expected
	}

	d := &DB{path: path}
	d.handle.Store(sqlDB)
	if err := d.insertDefaultSettings(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("seed default settings: %w", err)
	}
	return d, nil
}

// Reopen re-opens the database at the same path and atomically swaps the
// underlying handle in place. Every component (server, scheduler, runner,
// hub) shares this *DB pointer, so after Reopen they all see the new
// database. Used after RestoreDB swaps the file on disk, so the daemon
// keeps running without a restart.
// Known limitation: concurrent queries during the Close→Reopen window can
// see transient "database is closed" errors — an availability blip during
// deliberate maintenance, not corruption and not a data race (the swap is
// atomic). Fallback if this misbehaves in practice: a restart endpoint.
func (d *DB) Reopen() error {
	nd, err := Open(d.path)
	if err != nil {
		return fmt.Errorf("reopening database: %w", err)
	}
	// Close the old handle; sql.DB.Close is idempotent, so this is safe
	// even when the caller (restore flow) already closed it.
	if old := d.handle.Swap(nd.handle.Load()); old != nil {
		_ = old.Close()
	}
	return nil
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
