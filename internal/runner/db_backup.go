package runner

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// dbBackupBaseDir is the directory layout under each storage root where
// the daemon's own database is copied after every successful backup.
const dbBackupBaseDir = "_vault"

// dbBackupsKept caps how many timestamped vault.db copies are retained per
// destination. One copy lands after every successful run, so without a cap
// the directory grew forever (477 MB / 136 copies observed — issue #184).
// 14 copies ≈ two weeks of daily runs; the vault.db.latest pointer is always
// kept in addition.
const dbBackupsKept = 14

// backupDatabase copies the working database to (a) the job's own
// destination AND (b) any other destination flagged with
// backup_database_enabled. Both timestamped and "latest" pointer
// files are written. Encrypted with the global passphrase when one
// is configured; plaintext + warning otherwise.
//
// All failures are logged but never propagated — DB backup is
// best-effort and must never fail the user's backup job.
func (r *Runner) backupDatabase(ctx context.Context, jobDest db.StorageDestination) {
	dbPath := r.db.Path()
	if dbPath == "" || dbPath == ":memory:" {
		return
	}

	// Checkpoint the WAL so the on-disk file is self-contained when we read
	// it byte-by-byte. Best-effort: a busy checkpoint is fine, the file is
	// still readable via SQLite's recovery on the destination side, just
	// slightly larger.
	if conn, err := r.db.Conn(context.Background()); err == nil {
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
	r.backupDatabaseToDest(ctx, jobDest, dbPath, passphrase)
	seen[jobDest.ID] = struct{}{}

	// (b) Fan out to opted-in destinations.
	extras, err := r.db.ListDBBackupDestinations()
	if err != nil {
		log.Printf("db_backup: listing opt-in destinations: %v", err)
		return
	}
	for _, d := range extras {
		if ctx.Err() != nil {
			log.Printf("db_backup: cancelled before destination %q; skipping remaining fan-out", d.Name)
			return
		}
		if _, dup := seen[d.ID]; dup {
			continue
		}
		r.backupDatabaseToDest(ctx, d, dbPath, passphrase)
		seen[d.ID] = struct{}{}
	}
}

// backupDatabaseToDest writes the DB file to a single destination.
// passphrase=="" means plaintext.
//
// Two paths are written: a timestamped historical copy and a stable
// "latest" pointer. Both are encrypted when passphrase is non-empty.
func (r *Runner) backupDatabaseToDest(ctx context.Context, dest db.StorageDestination, dbPath, passphrase string) {
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		log.Printf("db_backup: adapter for %q: %v", dest.Name, err)
		return
	}
	defer storage.CloseAdapter(adapter)
	// Closing the adapter on cancellation aborts an in-flight write so a
	// timed-out finalization step's goroutine exits instead of leaking.
	stopOnCancel := context.AfterFunc(ctx, func() { storage.CloseAdapter(adapter) })
	defer stopOnCancel()

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

	pruneDBBackups(adapter, dest.Name)
}

// pruneDBBackups deletes the oldest timestamped vault.db copies beyond
// dbBackupsKept. The filenames embed a sortable UTC timestamp, so a lexical
// sort is chronological. The vault.db.latest pointer and everything else in
// the shared _vault directory (dedup repo paths) are never touched.
// Best-effort: failures are logged and must never fail the backup job.
func pruneDBBackups(adapter storage.Adapter, destName string) {
	entries, err := adapter.List(dbBackupBaseDir)
	if err != nil {
		if !storage.IsNotExist(err) {
			log.Printf("db_backup: prune: list %s on %q: %v", dbBackupBaseDir, destName, err)
		}
		return
	}
	var hist []string
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		name := filepath.Base(e.Path)
		if !strings.HasPrefix(name, "vault.db.") || strings.HasPrefix(name, "vault.db.latest") {
			continue
		}
		hist = append(hist, name)
	}
	if len(hist) <= dbBackupsKept {
		return
	}
	sort.Strings(hist)
	for _, name := range hist[:len(hist)-dbBackupsKept] {
		p := filepath.Join(dbBackupBaseDir, name)
		if err := adapter.Delete(p); err != nil {
			log.Printf("db_backup: prune: delete %s on %q: %v", p, destName, err)
		}
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
