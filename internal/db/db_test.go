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

// TestReopen mirrors the real restore-db flow: close the handle, swap a
// DIFFERENT valid database file over the same path (removing stale WAL/SHM
// sidecars), then Reopen and verify the same *DB pointer sees the NEW
// file's data — all without restarting the daemon.
func TestReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting("reopen_marker", "old"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	// Build a second, different database and swap it over the same path,
	// as the restore handler does after downloading a snapshot.
	otherPath := filepath.Join(dir, "restored.db")
	other, err := Open(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.SetSetting("reopen_marker", "new"); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if err := os.Rename(otherPath, path); err != nil {
		t.Fatal(err)
	}

	if err := d.Reopen(); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	got, err := d.GetSetting("reopen_marker", "")
	if err != nil {
		t.Fatalf("query after reopen: %v", err)
	}
	if got != "new" {
		t.Fatalf("marker = %q, want %q (swapped-in file's data)", got, "new")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestReopenConcurrentAccess is a race-detector regression test: queries on
// the shared *DB must not race with Reopen's handle swap. Readers may see
// transient "database is closed" errors mid-swap — that is the documented
// availability blip and is deliberately ignored; the test only guards
// against memory-model violations (meaningful under -race).
func TestReopenConcurrentAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// The reader loops until stopped rather than a fixed count: each Reopen
	// takes long enough (schema migrations) that a bounded reader would
	// finish before the first swap and never exercise the race window.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = d.GetSetting("reopen_marker", "")
			}
		}
	}()
	for i := 0; i < 10; i++ {
		// Close first, as the real restore flow does. Skipping the Close
		// leaves the reader's connection holding a read lock on the file,
		// and Reopen's schema writes then fail with SQLITE_BUSY.
		_ = d.Close()
		if err := d.Reopen(); err != nil {
			t.Fatalf("Reopen #%d: %v", i, err)
		}
	}
	close(stop)
	<-done
}

// TestReopenError verifies that Reopen() returns an error when the
// database path is no longer openable (e.g. its parent directory was
// removed).
func TestReopenError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := d.Reopen(); err == nil {
		t.Fatal("Reopen() error = nil, want error for missing directory")
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

func TestConnectionPragmas(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var busy int
	if err := d.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy != 30000 {
		t.Errorf("busy_timeout = %d, want 30000", busy)
	}

	var mode string
	if err := d.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal (fresh DBs must not run in rollback mode)", mode)
	}

	var fk int
	if err := d.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}

	var jsl int
	if err := d.QueryRow("PRAGMA journal_size_limit").Scan(&jsl); err != nil {
		t.Fatal(err)
	}
	if jsl != 67108864 {
		t.Errorf("journal_size_limit = %d, want 67108864", jsl)
	}
}
