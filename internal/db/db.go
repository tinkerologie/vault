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
}

func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

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

	return &DB{sqlDB}, nil
}
