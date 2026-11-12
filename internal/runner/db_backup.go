package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/storage"
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

// backupDatabase publishes one standalone snapshot to the job destination
// and every opted-in destination. The caller reports errors separately from
// the workload backup outcome.
func (r *Runner) backupDatabase(ctx context.Context, jobDest db.StorageDestination) error {
	dbPath := r.db.Path()
	if dbPath == "" || dbPath == ":memory:" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "vault-db-backup-*")
	if err != nil {
		return fmt.Errorf("staging database snapshot: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("db_backup: warning — removing snapshot staging directory: %v", err)
		}
	}()
	snapshot := filepath.Join(dir, "vault.db")
	if err := r.backupDatabaseSnapshot(ctx, snapshot); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	stamp := time.Now().UTC().Format("2006-01-02T15-04-05")
	passphrase := r.resolvePassphrase()
	if passphrase == "" {
		log.Printf("db_backup: warning — no encryption passphrase configured; DB backup will be written in plaintext")
	}

	result := r.backupDatabaseToDest(ctx, jobDest, snapshot, stamp, passphrase)
	if err := ctx.Err(); err != nil {
		return errors.Join(result, err)
	}
	extras, err := r.db.ListDBBackupDestinations()
	if err != nil {
		return errors.Join(result, fmt.Errorf("listing opt-in destinations: %w", err))
	}
	for _, d := range extras {
		if ctx.Err() != nil {
			return errors.Join(result, ctx.Err())
		}
		if d.ID != jobDest.ID {
			result = errors.Join(result, r.backupDatabaseToDest(ctx, d, snapshot, stamp, passphrase))
		}
	}
	return result
}

// backupDatabaseToDest writes the DB file to a single destination.
// passphrase=="" means plaintext.
//
// Two paths are written: a timestamped historical copy and a stable
// "latest" pointer. Both are encrypted when passphrase is non-empty.
func (r *Runner) backupDatabaseToDest(ctx context.Context, dest db.StorageDestination, snapshot, stamp, passphrase string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	adapter, err := storage.NewAdapterWithOptions(dest.Type, dest.Config, storage.Options{WebDAVContext: ctx})
	if err != nil {
		return fmt.Errorf("destination %d (%q) adapter: %w", dest.ID, dest.Name, err)
	}
	defer storage.CloseAdapter(adapter)
	// Also close adapters that can cancel requests or connections on Close.
	stopOnCancel := context.AfterFunc(ctx, func() { storage.CloseAdapter(adapter) })
	defer stopOnCancel()

	ext := ".db"
	if passphrase != "" {
		ext = ".age"
	}

	timestampedPath := filepath.Join(dbBackupBaseDir, fmt.Sprintf("vault.db.%s%s", stamp, ext))
	if err := writeDBOnce(adapter, snapshot, timestampedPath, passphrase); err != nil {
		// Keep the previous latest copy when publishing the history failed.
		return fmt.Errorf("destination %d (%q) timestamped write: %w", dest.ID, dest.Name, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	latestPath := filepath.Join(dbBackupBaseDir, "vault.db.latest"+ext)
	if err := writeDBOnce(adapter, snapshot, latestPath, passphrase); err != nil {
		return fmt.Errorf("destination %d (%q) latest write: %w", dest.ID, dest.Name, err)
	}
	if ctx.Err() == nil {
		pruneDBBackups(adapter, dest.Name)
	}
	return nil
}

func (r *Runner) finalizeDatabaseBackup(ctx context.Context, dest db.StorageDestination, jobID, runID int64) {
	if err := r.backupDatabase(ctx, dest); err != nil {
		detail := redactStorageError(err.Error())
		message := "Database Backup failed: " + detail
		data := map[string]any{
			"job_id": jobID, "run_id": runID, "stage": "database-backup", "error": detail,
		}
		log.Printf("db_backup: error: run %d: %s", runID, detail)
		r.runLog(runID, runLogLevelWarn, message, data)
		r.logActivity("warn", "backup", "Database Backup failed", structuredDetails(data))
	}
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
func writeDBOnce(adapter storage.Adapter, snapshot, remotePath, passphrase string) error {
	f, err := os.Open(snapshot) // #nosec G304 — snapshot in private staging directory
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
