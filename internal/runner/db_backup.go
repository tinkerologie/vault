package runner

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// dbBackupBaseDir is the directory layout under each storage root where
// the daemon's own database is copied after every successful backup.
const dbBackupBaseDir = "_vault"

// backupDatabase copies the working database to (a) the job's own
// destination AND (b) any other destination flagged with
// backup_database_enabled. Both timestamped and "latest" pointer
// files are written. Encrypted with the global passphrase when one
// is configured; plaintext + warning otherwise.
//
// All failures are logged but never propagated — DB backup is
// best-effort and must never fail the user's backup job.
func (r *Runner) backupDatabase(jobDest db.StorageDestination) {
	dbPath := r.db.Path()
	if dbPath == "" || dbPath == ":memory:" {
		return
	}

	// Checkpoint the WAL so the on-disk file is self-contained when we read
	// it byte-by-byte. Best-effort: a busy checkpoint is fine, the file is
	// still readable via SQLite's recovery on the destination side, just
	// slightly larger.
	if conn, err := r.db.DB.Conn(context.Background()); err == nil {
		if _, ckErr := conn.ExecContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`); ckErr != nil {
			log.Printf("db_backup: wal_checkpoint failed: %v (continuing)", ckErr)
		}
		_ = conn.Close()
	}

	passphrase := r.resolvePassphrase()
	if passphrase == "" {
		log.Printf("db_backup: warning — no encryption passphrase configured; DB backup will be written in plaintext")
	}

	seen := make(map[int64]struct{})

	// (a) Always write to the job's own destination.
	r.backupDatabaseToDest(jobDest, dbPath, passphrase)
	seen[jobDest.ID] = struct{}{}

	// (b) Fan out to opted-in destinations.
	extras, err := r.db.ListDBBackupDestinations()
	if err != nil {
		log.Printf("db_backup: listing opt-in destinations: %v", err)
		return
	}
	for _, d := range extras {
		if _, dup := seen[d.ID]; dup {
			continue
		}
		r.backupDatabaseToDest(d, dbPath, passphrase)
		seen[d.ID] = struct{}{}
	}
}

// backupDatabaseToDest writes the DB file to a single destination.
// passphrase=="" means plaintext.
//
// Two paths are written: a timestamped historical copy and a stable
// "latest" pointer. Both are encrypted when passphrase is non-empty.
func (r *Runner) backupDatabaseToDest(dest db.StorageDestination, dbPath, passphrase string) {
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		log.Printf("db_backup: adapter for %q: %v", dest.Name, err)
		return
	}
	defer storage.CloseAdapter(adapter)

	stamp := time.Now().UTC().Format("2006-01-02T15-04-05")
	ext := ".db"
	if passphrase != "" {
		ext = ".age"
	}

	timestampedPath := filepath.Join(dbBackupBaseDir, fmt.Sprintf("vault.db.%s%s", stamp, ext))
	if err := writeDBOnce(adapter, dbPath, timestampedPath, passphrase); err != nil {
		log.Printf("db_backup: write timestamped to %q: %v", dest.Name, err)
		// Don't return — still try the latest pointer.
	}

	latestPath := filepath.Join(dbBackupBaseDir, "vault.db.latest"+ext)
	if err := writeDBOnce(adapter, dbPath, latestPath, passphrase); err != nil {
		log.Printf("db_backup: write latest pointer to %q: %v", dest.Name, err)
	}
}

// writeDBOnce opens the DB file and streams it (optionally encrypted) to
// the adapter at remotePath. Each call opens a fresh handle so the reader
// can be consumed without seek tricks.
func writeDBOnce(adapter storage.Adapter, dbPath, remotePath, passphrase string) error {
	f, err := os.Open(dbPath) // #nosec G304 — dbPath from daemon startup
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer f.Close()

	var src io.Reader = f
	if passphrase != "" {
		enc, err := crypto.EncryptReader(passphrase, f)
		if err != nil {
			return fmt.Errorf("encrypt: %w", err)
		}
		defer enc.Close()
		src = enc
	}
	return adapter.Write(remotePath, src)
}
