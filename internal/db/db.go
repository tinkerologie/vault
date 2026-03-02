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

	return &DB{DB: sqlDB, path: path}, nil
}
