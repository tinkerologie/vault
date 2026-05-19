package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCreatesDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer database.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("database file was not created")
	}
}

func TestMigrationsCreateTables(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer database.Close()

	tables := []string{"jobs", "job_runs", "restore_points", "storage_destinations", "job_items"}
	for _, table := range tables {
		var name string
		err := database.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

// TestDedupSchemaMigration verifies that opening a fresh DB creates the
// two dedup tables (dedup_packs, dedup_chunks) and that the new
// dedup_enabled / manifest_id columns are present on the relevant
// existing tables via the idempotent ALTER migrations.
func TestDedupSchemaMigration(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer database.Close()

	// 1. dedup_packs and dedup_chunks tables must exist.
	for _, table := range []string{"dedup_packs", "dedup_chunks"} {
		var name string
		err := database.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err == sql.ErrNoRows {
			t.Errorf("table %q not created", table)
		} else if err != nil {
			t.Errorf("lookup table %q: %v", table, err)
		}
	}

	// 2. storage_destinations must expose dedup_enabled column.
	if !columnExists(t, database, "storage_destinations", "dedup_enabled") {
		t.Errorf("storage_destinations.dedup_enabled column missing")
	}

	// 3. restore_points must expose manifest_id column.
	if !columnExists(t, database, "restore_points", "manifest_id") {
		t.Errorf("restore_points.manifest_id column missing")
	}
}

// columnExists returns true if the named column is present on the
// given table according to PRAGMA table_info.
func columnExists(t *testing.T, d *DB, table, column string) bool {
	t.Helper()
	rows, err := d.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan PRAGMA row for %s: %v", table, err)
		}
		if name == column {
			return true
		}
	}
	return false
}
