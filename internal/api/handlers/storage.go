package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/storage"
)

type StorageHandler struct {
	db             *db.DB
	runner         *runner.Runner
	serverKey      []byte
	schedReload    ScheduleReloader
	onConfigChange ConfigChangeHook

	// restoreMu serializes RestoreDB: the close→swap→reopen sequence must
	// never run concurrently. A field (not package-level) so this package's
	// parallel tests with separate handlers don't contend. Production shares
	// this instance with RecoveryHandler.PathRemap via MaintenanceLock so a
	// remap can never commit mid-swap and be lost.
	restoreMu sync.Mutex
}

// MaintenanceLock exposes the restore lifecycle lock so other handlers
// (PathRemap) can coordinate with in-flight database restores.
func (h *StorageHandler) MaintenanceLock() *sync.Mutex {
	return &h.restoreMu
}

// NewStorageHandler creates a storage destinations handler. serverKey is
// used to re-seal the encryption passphrase after a database restore.
func NewStorageHandler(database *db.DB, r *runner.Runner, serverKey []byte) *StorageHandler {
	return &StorageHandler{db: database, runner: r, serverKey: serverKey}
}

// SetScheduleReloadHook installs the scheduler reload used after a DB restore.
func (h *StorageHandler) SetScheduleReloadHook(reload ScheduleReloader) {
	h.schedReload = reload
}

// SetConfigChangeHook registers a function called after storage mutations
// (typically used by the daemon to flush the DB snapshot to USB flash).
func (h *StorageHandler) SetConfigChangeHook(fn ConfigChangeHook) {
	h.onConfigChange = fn
}

// notifyConfigChange invokes the persistence hook if one is registered.
func (h *StorageHandler) notifyConfigChange() {
	if h.onConfigChange != nil {
		h.onConfigChange()
	}
}

// broadcastConfigChange sends a `config_changed` WebSocket event so that
// dashboards / 3-2-1 compliance widgets re-fetch derived state without
// requiring a full page reload. Safe to call when the runner is nil
// (e.g., in tests).
func (h *StorageHandler) broadcastConfigChange(entity string) {
	if h.runner == nil {
		return
	}
	h.runner.Broadcast(map[string]any{
		"type":   "config_changed",
		"entity": entity,
	})
}

func (h *StorageHandler) List(w http.ResponseWriter, r *http.Request) {
	dests, err := h.db.ListStorageDestinations()
	if err != nil {
		respondInternalError(w, err)
		return
	}
	out := make([]map[string]any, len(dests))
	for i, d := range dests {
		out[i] = storageResponseWithCapacity(d)
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *StorageHandler) Create(w http.ResponseWriter, r *http.Request) {
	var dest db.StorageDestination
	if err := json.NewDecoder(r.Body).Decode(&dest); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	dest.Name = strings.TrimSpace(dest.Name)
	dest.Type = strings.TrimSpace(dest.Type)
	if dest.Name == "" {
		respondError(w, http.StatusBadRequest, "name is required")
		return
	}
	if dest.Type == "" {
		respondError(w, http.StatusBadRequest, "type is required")
		return
	}
	// Validate the config can construct a working adapter before persisting.
	// Catches typos like type:"bogus", empty configs, malformed JSON in the
	// config blob, and other misconfigurations that would otherwise sit in
	// the dropdown as a permanently-broken destination.
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	storage.CloseAdapter(adapter)

	id, err := h.db.CreateStorageDestination(dest)
	if err != nil {
		respondWriteError(w, err, "storage destination")
		return
	}
	// Re-fetch the row so the response includes server-assigned timestamps
	// and the canonical, redacted config blob (never the plaintext one the
	// caller just sent — would leak passwords and S3 secret keys via the
	// response body even though Get redacts).
	saved, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusCreated, storageResponseWithCapacity(saved))
	h.broadcastConfigChange("storage")
	h.notifyConfigChange()
}

func (h *StorageHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	respondJSON(w, http.StatusOK, storageResponseWithCapacity(dest))
}

func (h *StorageHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	existing, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	// Decode into a partial payload so we can distinguish "field omitted"
	// from "field explicitly set to empty". Previously the handler decoded
	// into a zero-valued db.StorageDestination and wrote the whole row,
	// which silently blanked name/type/dedup_enabled when the caller sent a
	// partial body (e.g. {config:"..."}) — orphaning every job that pointed
	// at the destination. dest.Type and dest.DedupEnabled are immutable
	// after creation; we reject attempts to change them rather than
	// silently ignore.
	var patch struct {
		Name                  *string `json:"name"`
		Type                  *string `json:"type"`
		Config                *string `json:"config"`
		DedupEnabled          *bool   `json:"dedup_enabled"`
		BackupDatabaseEnabled *bool   `json:"backup_database_enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if patch.Type != nil && strings.TrimSpace(*patch.Type) != existing.Type {
		respondError(w, http.StatusBadRequest, "type cannot be changed after creation")
		return
	}
	if patch.DedupEnabled != nil && *patch.DedupEnabled != existing.DedupEnabled {
		respondError(w, http.StatusBadRequest, "dedup_enabled cannot be changed after creation")
		return
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			respondError(w, http.StatusBadRequest, "name cannot be empty")
			return
		}
		existing.Name = name
	}
	if patch.Config != nil {
		// preserveRedactedSecrets carries forward credentials that the
		// caller (typically the UI's edit modal) round-tripped as the
		// "••••••••" marker rather than retyping. Without this, editing
		// any non-credential field on a destination with a password would
		// silently overwrite the password with the marker bytes and
		// every subsequent request would 401.
		merged, mErr := preserveRedactedSecrets(*patch.Config, existing.Config)
		if mErr != nil {
			respondInternalError(w, mErr)
			return
		}
		existing.Config = merged
		// Re-validate; the user may have broken the config blob.
		adapter, err := storage.NewAdapter(existing.Type, existing.Config)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		storage.CloseAdapter(adapter)
	}
	if patch.BackupDatabaseEnabled != nil {
		existing.BackupDatabaseEnabled = *patch.BackupDatabaseEnabled
	}
	if err := h.db.UpdateStorageDestination(existing); err != nil {
		respondWriteError(w, err, "storage destination")
		return
	}
	saved, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, storageResponseWithCapacity(saved))
	h.broadcastConfigChange("storage")
	h.notifyConfigChange()
}

func (h *StorageHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	// Check for dependent jobs.
	jobCount, err := h.db.CountJobsByStorageDestID(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	if jobCount > 0 && r.URL.Query().Get("force") != "true" {
		respondJSON(w, http.StatusConflict, map[string]any{
			"error":     "storage destination has dependent jobs",
			"job_count": jobCount,
		})
		return
	}

	// Optionally delete backup files from storage.
	if r.URL.Query().Get("deleteFiles") == "true" {
		dest, err := h.db.GetStorageDestination(id)
		if err == nil {
			if err := h.runner.CleanupStorageDestination(dest); err != nil {
				log.Printf("Warning: failed to clean up files for storage %d: %s", id, err.Error()) // #nosec G706 //nolint:gosec // id is int64 from URL param
			}
		}
	}

	if err := h.db.DeleteStorageDestination(id); err != nil {
		respondInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	h.broadcastConfigChange("storage")
	h.notifyConfigChange()
}

func (h *StorageHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer storage.CloseAdapter(adapter)
	if err := adapter.TestConnection(); err != nil {
		respondJSON(w, http.StatusOK, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}

// TestConfig handles POST /api/v1/storage/test — tests an unsaved config blob so
// the add/edit modal can validate a destination before persisting it (issue
// #206 / E8). Mirrors TestConnection but builds an ephemeral adapter from the
// posted {type, config} instead of loading a saved row.
func (h *StorageHandler) TestConfig(w http.ResponseWriter, r *http.Request) {
	var dest db.StorageDestination
	if err := json.NewDecoder(r.Body).Decode(&dest); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	dest.Type = strings.TrimSpace(dest.Type)
	if dest.Type == "" {
		respondError(w, http.StatusBadRequest, "type is required")
		return
	}
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{"success": false, "error": err.Error()})
		return
	}
	defer storage.CloseAdapter(adapter)
	if err := adapter.TestConnection(); err != nil {
		respondJSON(w, http.StatusOK, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}

// CloseBreaker handles POST /api/v1/storage/{id}/breaker/close.
// Forcibly resets the destination's circuit breaker to closed.
func (h *StorageHandler) CloseBreaker(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "destination not found")
		return
	}
	if h.runner == nil {
		respondError(w, http.StatusInternalServerError, "runner unavailable")
		return
	}
	if err := h.runner.Breaker().ManualClose(h.db, id); err != nil {
		respondInternalError(w, err)
		return
	}
	log.Printf("breaker: manually closed for dest id=%d", id) // #nosec G706 //nolint:gosec // id is parsed via strconv.ParseInt — already a validated int64
	_ = dest                                                  // dest fetched only to verify existence
	w.WriteHeader(http.StatusNoContent)
}

// HealthCheck is the manual-trigger sibling of the scheduler's daily
// storage-destination health sweep. Runs TestConnection synchronously,
// persists the result on the storage_destinations row, and returns it.
//
//	POST /api/v1/storage/{id}/health-check
func (h *StorageHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	status, errMsg := h.runner.CheckStorageDestination(dest)
	respondJSON(w, http.StatusOK, map[string]any{
		"status": status,
		"error":  errMsg,
	})
}

// ScanOrphans walks the storage destination and returns the list of paths
// that don't belong to any known restore point. Safe to call repeatedly:
// no state is modified. The user clicks DeleteOrphans to actually delete.
//
//	POST /api/v1/storage/{id}/scan-orphans
//
// Returns: {"orphans": ["path/a", "path/b"], "total_bytes": N}
func (h *StorageHandler) ScanOrphans(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	orphans, totalBytes, err := h.runner.ScanStorageOrphans(dest)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"orphans":     orphans,
		"total_bytes": totalBytes,
	})
}

// DeleteOrphans accepts the same orphan list the UI got from ScanOrphans
// and deletes those files. The body shape is {"paths":["a","b"]}; only
// paths actually in the current orphan set are deleted (so a stale list
// from a prior scan can't accidentally delete a fresh restore point).
//
//	POST /api/v1/storage/{id}/delete-orphans  {"paths":[...]}
func (h *StorageHandler) DeleteOrphans(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if len(req.Paths) == 0 {
		respondJSON(w, http.StatusOK, map[string]any{"deleted": 0, "errors": []string{}})
		return
	}
	deleted, errs := h.runner.DeleteStorageOrphans(dest, req.Paths)
	respondJSON(w, http.StatusOK, map[string]any{
		"deleted": deleted,
		"errors":  errs,
	})
}

// Scan discovers existing backups on a storage destination by reading
// manifest.json files from each backup run directory.
//
//	POST /api/v1/storage/{id}/scan
func (h *StorageHandler) Scan(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "storage destination not found")
		return
	}

	manifests, err := h.runner.ScanStorageManifests(dest)
	if err != nil {
		respondInternalError(w, err)
		return
	}

	// Also scan for appdata.backup (ab_*) directories.
	// Optional ?path= parameter allows scanning a custom subfolder.
	abManifests, err := h.runner.ScanAppdataBackups(dest, r.URL.Query().Get("path"))
	if err != nil {
		log.Printf("Warning: appdata.backup scan failed: %v", err)
	}
	manifests = append(manifests, abManifests...)

	if manifests == nil {
		manifests = []map[string]any{}
	}

	// Check for the centralized vault database backup at _vault/vault.db.
	var vaultDB map[string]any
	adapter, adapterErr := storage.NewAdapter(dest.Type, dest.Config)
	if adapterErr == nil {
		info, statErr := adapter.Stat("_vault/vault.db")
		storage.CloseAdapter(adapter)
		if statErr == nil {
			vaultDB = map[string]any{
				"path":        "_vault",
				"modified_at": info.ModTime,
			}
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"backups":  manifests,
		"vault_db": vaultDB,
	})
}

// Import creates job and restore point records from previously scanned
// backup manifests. Jobs are matched by name; new jobs are created if
// they don't already exist.
//
//	POST /api/v1/storage/{id}/import
func (h *StorageHandler) Import(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	// Verify the storage destination exists.
	if _, err := h.db.GetStorageDestination(id); err != nil {
		respondError(w, http.StatusNotFound, "storage destination not found")
		return
	}

	var req struct {
		Backups []map[string]any `json:"backups"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	imported, err := h.runner.ImportBackups(id, req.Backups)
	if err != nil {
		respondInternalError(w, err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"imported": imported,
		"total":    len(req.Backups),
	})
}

// RestoreDB downloads a vault.db snapshot from a backup directory on
// storage and replaces the current database file.
//
//	POST /api/v1/storage/{id}/restore-db
//
// sqliteMagic is the 16-byte header every SQLite 3 database file starts with.
const sqliteMagic = "SQLite format 3\x00"

func (h *StorageHandler) RestoreDB(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "storage destination not found")
		return
	}

	var req struct {
		StoragePath string `json:"storage_path"`
		Passphrase  string `json:"passphrase"`
		VerifyOnly  bool   `json:"verify_only"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.StoragePath == "" {
		respondError(w, http.StatusBadRequest, "storage_path is required")
		return
	}
	cleanedStoragePath := path.Clean("/" + req.StoragePath)
	if cleanedStoragePath == "/" || strings.Contains(req.StoragePath, "..") {
		respondError(w, http.StatusBadRequest, "invalid storage_path")
		return
	}

	// Serialize restores: the close→swap→reopen sequence must never race
	// another restore (verify_only requests also queue here — they're quick).
	if !h.restoreMu.TryLock() {
		respondError(w, http.StatusConflict, "a database restore is already in progress")
		return
	}
	defer h.restoreMu.Unlock()

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	defer storage.CloseAdapter(adapter)

	dbPath := strings.TrimPrefix(cleanedStoragePath, "/")
	if !strings.HasSuffix(dbPath, ".db") && !strings.HasSuffix(dbPath, ".age") {
		// Legacy form: storage_path is a directory containing a plaintext vault.db.
		dbPath += "/vault.db"
	}
	encrypted := strings.HasSuffix(dbPath, ".age")

	if encrypted && req.Passphrase == "" {
		respondError(w, http.StatusBadRequest, "this backup is encrypted — enter your backup password")
		return
	}
	// No pre-check against the CURRENT db's passphrase hash here: the age
	// header parse in DecryptReader below is the authoritative check, and a
	// pre-check would hard-lock restores after a passphrase rotation.

	rc, err := adapter.Read(dbPath)
	if err != nil {
		// Only a genuine not-found reads as "no backup" — a transport or
		// auth failure must not send a DR user hunting for a missing file.
		if storage.IsNotExist(err) {
			respondError(w, http.StatusNotFound, "no Vault backup found at "+dbPath+" — look for a folder named _vault on your backup storage")
			return
		}
		log.Printf("RestoreDB: reading %s from destination %d: %v", dbPath, id, err) // #nosec G706 //nolint:gosec // id is int64 from URL param, err is from an admin-configured adapter
		respondError(w, http.StatusBadGateway, "could not read your backup storage — check the connection and try again")
		return
	}
	defer rc.Close()

	var src io.Reader = rc
	if encrypted {
		dec, derr := crypto.DecryptReader(req.Passphrase, rc)
		if derr != nil {
			if crypto.IsWrongPassphrase(derr) {
				respondError(w, http.StatusBadRequest, "incorrect passphrase — check for typos; this is the encryption password you chose in Settings → Encryption on your old server")
				return
			}
			respondInternalError(w, derr)
			return
		}
		defer dec.Close()
		src = dec
	}

	if req.VerifyOnly {
		// A wrong passphrase on .age already errored above. Now confirm the
		// (decrypted) payload actually is a SQLite database — otherwise a
		// plaintext verify validates nothing.
		header := make([]byte, len(sqliteMagic))
		if _, rerr := io.ReadFull(src, header); rerr != nil || string(header) != sqliteMagic {
			respondError(w, http.StatusBadRequest, "that file doesn't look like a Vault database backup")
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"valid": true})
		return
	}

	// Write to a temporary file first.
	tmpFile, err := os.CreateTemp("", "vault-restore-*.db")
	if err != nil {
		respondInternalError(w, err)
		return
	}
	tmpPath := tmpFile.Name()

	if _, err := io.Copy(tmpFile, src); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		respondInternalError(w, err)
		return
	}
	_ = tmpFile.Close()

	// Validate the downloaded database. Open catches gross damage; the
	// integrity check catches corrupt pages that still open fine.
	testDB, err := db.Open(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		respondError(w, http.StatusBadRequest, "downloaded file is not a valid Vault database: "+err.Error())
		return
	}
	if err := testDB.IntegrityCheck(); err != nil {
		_ = testDB.Close()
		_ = os.Remove(tmpPath)
		log.Printf("RestoreDB: candidate DB failed integrity check: %v", err)
		respondError(w, http.StatusBadRequest, "that backup file failed the database integrity check")
		return
	}
	_ = testDB.Close()

	// Replace the current database.
	currentPath := h.db.Path()
	if currentPath == "" || currentPath == ":memory:" {
		_ = os.Remove(tmpPath)
		respondError(w, http.StatusInternalServerError, "cannot replace in-memory database")
		return
	}

	// Close current DB before swapping files. Aborting on close failure avoids
	// overwriting the live DB while another goroutine still holds it.
	if err := h.db.Close(); err != nil {
		_ = os.Remove(tmpPath)
		respondInternalError(w, fmt.Errorf("closing current DB before restore: %w", err))
		return
	}

	// Atomic-replace pattern: rename current → .bak, copy temp → current,
	// remove .bak on success or restore .bak on failure. This guarantees
	// we never end up without a usable DB on disk.
	backupPath := currentPath + ".bak"
	_ = os.Remove(backupPath) // clear any stale backup
	backupExists := false
	// restoreBackup undoes a failed swap: put the original file back (when we
	// got far enough to move it) AND reopen the handle — otherwise the daemon
	// keeps a closed DB and fails every request until a manual restart.
	restoreBackup := func() {
		if backupExists {
			_ = os.Rename(backupPath, currentPath)
		}
		if err := h.db.Reopen(); err != nil {
			log.Printf("RestoreDB: reopening DB after failed restore: %v — restart Vault from the plugin page", err)
		}
	}
	if _, statErr := os.Stat(currentPath); statErr == nil {
		if err := os.Rename(currentPath, backupPath); err != nil {
			restoreBackup()
			_ = os.Remove(tmpPath)
			respondInternalError(w, fmt.Errorf("backup current DB: %w", err))
			return
		}
		backupExists = true
	}

	srcFile, err := os.Open(tmpPath) // #nosec G304 — tmpPath is os.CreateTemp result, vault-controlled
	if err != nil {
		restoreBackup()
		_ = os.Remove(tmpPath)
		respondInternalError(w, err)
		return
	}

	dstFile, err := os.Create(currentPath) // #nosec G304 //nolint:gosec // currentPath is from h.db.Path(), set at daemon startup — not user input
	if err != nil {
		_ = srcFile.Close()
		restoreBackup()
		_ = os.Remove(tmpPath)
		respondInternalError(w, err)
		return
	}

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		_ = srcFile.Close()
		_ = dstFile.Close()
		_ = os.Remove(currentPath)
		restoreBackup()
		_ = os.Remove(tmpPath)
		respondInternalError(w, err)
		return
	}
	if err := dstFile.Sync(); err != nil {
		_ = srcFile.Close()
		_ = dstFile.Close()
		_ = os.Remove(currentPath)
		restoreBackup()
		_ = os.Remove(tmpPath)
		respondInternalError(w, err)
		return
	}
	if err := srcFile.Close(); err != nil {
		log.Printf("Warning: closing source temp DB: %v", err)
	}
	if err := dstFile.Close(); err != nil {
		_ = os.Remove(currentPath)
		restoreBackup()
		_ = os.Remove(tmpPath)
		respondInternalError(w, err)
		return
	}

	// Remove WAL and SHM files (stale after replacement).
	_ = os.Remove(currentPath + "-wal")
	_ = os.Remove(currentPath + "-shm")

	// Reopen in-process so the daemon keeps working without a restart. The
	// .bak safety copy is only dropped once the reopen succeeds — if it
	// fails, .bak is the pre-restore DB and the only rollback artifact left.
	if err := h.db.Reopen(); err != nil {
		log.Printf("RestoreDB: reopen after restore failed: %v — pre-restore DB kept at %s", err, backupPath)
		// The temp file is the decrypted DB copy — don't leave it in the
		// temp dir. The .bak safety copy stays.
		_ = os.Remove(tmpPath)
		respondError(w, http.StatusInternalServerError,
			"database restored, but the daemon could not reload it — restart Vault from the plugin page to finish")
		return
	}

	// Success — drop the safety copy and temp.
	if backupExists {
		_ = os.Remove(backupPath)
	}
	_ = os.Remove(tmpPath)

	// The restored DB's sealed passphrase used the old server's vault.key.
	// Re-seal with the current key so scheduled DB backups stay encrypted.
	resealed := false
	if req.Passphrase != "" && len(h.serverKey) > 0 {
		if hash, _ := h.db.GetSetting("encryption_passphrase_hash", ""); hash != "" &&
			crypto.VerifyPassphrase(req.Passphrase, hash) == nil {
			sealed, err := crypto.Seal(h.serverKey, req.Passphrase)
			if err == nil {
				err = h.db.SetSetting("encryption_passphrase_sealed", sealed)
			}
			if err == nil {
				resealed = true
			} else {
				// Not fatal, but future DB backups would use the stale seal
				// (plaintext fallback) — make the failure findable in logs.
				log.Printf("RestoreDB: re-sealing passphrase failed: %v", err)
			}
		}
	}

	// A plaintext restore can't re-seal: warn when the restored DB carries a
	// seal made with the OLD server's vault.key, which this server can't open.
	if req.Passphrase == "" && len(h.serverKey) > 0 {
		if sealed, _ := h.db.GetSetting("encryption_passphrase_sealed", ""); sealed != "" {
			if _, err := crypto.Unseal(h.serverKey, sealed); err != nil {
				log.Printf("RestoreDB: restored DB has a sealed passphrase that cannot be unsealed with this server's key — re-enter it in Settings → Encryption")
			}
		}
	}

	if h.schedReload != nil {
		if err := h.schedReload(); err != nil {
			log.Printf("RestoreDB: scheduler reload after restore failed: %v", err)
		}
	}

	// Flush the restored DB to the cache snapshot + USB shadow now — a power
	// loss before the next periodic flush would revert the restore at boot.
	h.notifyConfigChange()

	respondJSON(w, http.StatusOK, map[string]any{
		"message":  "Database restored successfully.",
		"resealed": resealed,
	})
}

type dbBackupEntry struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Timestamp time.Time `json:"timestamp"`
	Size      int64     `json:"size"`
	Encrypted bool      `json:"encrypted"`
	IsLatest  bool      `json:"is_latest"`
}

// ListDBBackups lists vault.db snapshots in _vault/ on a destination,
// newest first with the latest pointer on top. Absent _vault/ is an empty
// list, not an error — a destination that never held a DB backup is normal.
//
//	GET /api/v1/storage/{id}/db-backups
func (h *StorageHandler) ListDBBackups(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "storage destination not found")
		return
	}
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	defer storage.CloseAdapter(adapter)

	files, err := adapter.List("_vault")
	if err != nil {
		if storage.IsNotExist(err) {
			// A destination that never held a DB backup is normal.
			respondJSON(w, http.StatusOK, []dbBackupEntry{})
			return
		}
		log.Printf("ListDBBackups: listing _vault on destination %d: %v", id, err) // #nosec G706 //nolint:gosec // id is int64 from URL param, err is from an admin-configured adapter
		respondError(w, http.StatusBadGateway, "could not read your backup storage — check the connection and try again")
		return
	}

	entries := []dbBackupEntry{}
	for _, f := range files {
		name := path.Base(f.Path)
		if f.IsDir || !strings.HasPrefix(name, "vault.db.") {
			continue
		}
		e := dbBackupEntry{
			Path:      "_vault/" + name,
			Name:      name,
			Timestamp: f.ModTime,
			Size:      f.Size,
			Encrypted: strings.HasSuffix(name, ".age"),
			IsLatest:  strings.HasPrefix(name, "vault.db.latest"),
		}
		if !e.IsLatest {
			stamp := strings.TrimPrefix(name, "vault.db.")
			stamp = strings.TrimSuffix(strings.TrimSuffix(stamp, ".age"), ".db")
			if ts, perr := time.Parse("2006-01-02T15-04-05", stamp); perr == nil {
				e.Timestamp = ts
			}
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsLatest != entries[j].IsLatest {
			return entries[i].IsLatest
		}
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
	respondJSON(w, http.StatusOK, entries)
}

// DependentJobs returns the list of jobs that reference a storage destination
// plus a count for convenience. Front-ends can render which jobs would be
// orphaned by a delete.
//
//	GET /api/v1/storage/{id}/jobs
func (h *StorageHandler) DependentJobs(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	// 404 on an unknown destination instead of reporting zero dependents,
	// which read as "safe to delete" for an id that never existed.
	if _, err := h.db.GetStorageDestination(id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "storage not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	jobs, err := h.db.ListJobsByStorageDestID(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"jobs":      jobs,
		"job_count": len(jobs),
	})
}

// ListFiles lists files/directories at a given prefix on the storage.
//
//	GET /api/v1/storage/{id}/list?prefix=some/path
func (h *StorageHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "storage not found")
		return
	}

	// Traversal guard — adapters treat the prefix as relative to the
	// destination root; never let it climb out. Validate per path component
	// and across BOTH separator styles: the SMB adapter converts '/' to '\',
	// so a backslash-smuggled ".." would survive a POSIX-only path.Clean.
	prefix := r.URL.Query().Get("prefix")
	if prefix != "" {
		normalized := strings.ReplaceAll(prefix, "\\", "/")
		if strings.HasPrefix(normalized, "/") {
			respondError(w, http.StatusBadRequest, "invalid prefix")
			return
		}
		for _, seg := range strings.Split(normalized, "/") {
			if seg == "." || seg == ".." {
				respondError(w, http.StatusBadRequest, "invalid prefix")
				return
			}
		}
	}

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	defer storage.CloseAdapter(adapter)

	files, err := adapter.List(prefix)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, files)
}

// DownloadFile streams a file from the storage.
//
//	GET /api/v1/storage/{id}/files?path=some/file.tar.zst
func (h *StorageHandler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "storage not found")
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		respondError(w, http.StatusBadRequest, "path query parameter required")
		return
	}

	// Reject path traversal attempts.
	cleaned := path.Clean(filePath)
	if cleaned != filePath || strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "..") {
		respondError(w, http.StatusBadRequest, "invalid path")
		return
	}

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		respondInternalError(w, err)
		return
	}

	rc, err := adapter.Read(filePath)
	if err != nil {
		respondError(w, http.StatusNotFound, "file not found: "+err.Error())
		return
	}
	defer rc.Close()

	// Try to get file size for Content-Length.
	fi, statErr := adapter.Stat(filePath)
	if statErr == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(fi.Size, 10))
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		log.Printf("Warning: error streaming file %q: %v", filePath, err) // #nosec G706 //nolint:gosec // filePath is admin-configured storage path
	}
}

// GetDedupStats returns the dedup stats snapshot for a destination's chunk
// repository, computed from SQL aggregates and the latest persisted GC run.
// Returns 404 if the destination is not dedup-enabled — the field-level
// "enabled" key in the response body lets the UI render a friendly empty
// state on the same endpoint when 404 handling would be noisy.
//
//	GET /api/v1/storage/{id}/dedup-stats
func (h *StorageHandler) GetDedupStats(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "destination not found")
		return
	}
	if !dest.DedupEnabled {
		respondError(w, http.StatusNotFound, "destination is not dedup-enabled")
		return
	}
	stats, err := h.runner.GetDedupStats(dest)
	if err != nil {
		respondInternalError(w, err)
		return
	}

	out := map[string]any{
		"enabled":               true,
		"total_chunks":          stats.TotalChunks,
		"total_packs":           stats.TotalPacks,
		"logical_bytes":         stats.LogicalBytes,
		"physical_bytes":        stats.PhysicalBytes,
		"wasted_bytes_estimate": stats.WastedBytesEstimate,
		"last_gc_at":            stats.LastGCAt,
		"last_gc_freed_bytes":   stats.LastGCFreedBytes,
	}
	if stats.PhysicalBytes > 0 {
		out["dedup_ratio"] = float64(stats.LogicalBytes) / float64(stats.PhysicalBytes)
	} else {
		out["dedup_ratio"] = 1.0
	}
	respondJSON(w, http.StatusOK, out)
}

// RunDedupGC kicks off an asynchronous mark-and-sweep GC against a
// dedup-enabled destination. Returns 202 with a `gc_run_id` immediately;
// the result is broadcast over the WebSocket hub as `dedup_gc_complete`
// when the run finishes.
//
//	POST /api/v1/storage/{id}/gc
func (h *StorageHandler) RunDedupGC(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "destination not found")
		return
	}
	if !dest.DedupEnabled {
		respondError(w, http.StatusBadRequest, "destination is not dedup-enabled")
		return
	}
	if h.runner == nil {
		respondError(w, http.StatusInternalServerError, "runner unavailable")
		return
	}
	runID, err := newGCRunID()
	if err != nil {
		respondInternalError(w, fmt.Errorf("generate gc id: %w", err))
		return
	}
	go h.runner.RunDedupGC(dest, runID)
	respondJSON(w, http.StatusAccepted, map[string]string{"gc_run_id": runID})
}

// newGCRunID returns a short random hex identifier for a GC run. Used to
// correlate the 202-Accepted response with the eventual
// `dedup_gc_complete` WebSocket event.
func newGCRunID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// sensitiveConfigKeys are config field names that contain credentials.
var sensitiveConfigKeys = map[string]bool{
	"password":          true,
	"secret_key":        true,
	"secret_access_key": true,
	"passphrase":        true,
	"key_file":          true,
	"refresh_token":     true,
	"client_secret":     true,
}

// redactionMarker is the placeholder used by redactConfig to mask sensitive
// values. preserveRedactedSecrets uses it to detect round-tripped values
// on Update so the API does not silently overwrite the stored credential
// with the marker bytes when the caller (typically the UI) re-submits a
// partial edit without touching the password field.
const redactionMarker = "••••••••"

// preserveRedactedSecrets parses an incoming config JSON and, for any
// sensitiveConfigKey whose value equals the redactionMarker, replaces the
// value with the corresponding field from the existing stored config.
// Returns the (possibly rewritten) incoming JSON.
//
// Rationale: redactConfig masks credentials in every read path so the API
// never leaks them. The UI's edit modal populates its password field with
// whatever the GET returned — i.e. the literal marker — and Svelte's
// two-way bind:value posts that marker back verbatim when the user saves
// after changing an unrelated field (name, bandwidth limit, base path…).
// Without this helper the server stored the marker as the new password
// and every subsequent request 401'd. The fix is server-side because the
// API contract is the one anyone (UI, MCP tools, ansible automation,
// scripts) can hit; pushing the responsibility to every client is brittle.
//
// A non-marker string passes through unchanged, so users who DO want to
// rotate the credential simply type the new value as before. A marker on
// a key that has no corresponding value in the existing config is left
// alone — the adapter validator further down the Update path will reject
// the empty/marker credential with a clearer error.
func preserveRedactedSecrets(incoming, existing string) (string, error) {
	var inMap map[string]any
	if err := json.Unmarshal([]byte(incoming), &inMap); err != nil {
		// Not JSON; pass through unchanged. The adapter validator on the
		// Update path will surface the real parsing error in context.
		return incoming, nil
	}
	var exMap map[string]any
	if err := json.Unmarshal([]byte(existing), &exMap); err != nil {
		exMap = nil
	}
	changed := false
	for k := range sensitiveConfigKeys {
		v, ok := inMap[k]
		if !ok {
			continue
		}
		s, isStr := v.(string)
		if !isStr || s != redactionMarker {
			continue
		}
		if exMap == nil {
			continue
		}
		old, ok := exMap[k]
		if !ok {
			continue
		}
		inMap[k] = old
		changed = true
	}
	if !changed {
		return incoming, nil
	}
	out, err := json.Marshal(inMap)
	if err != nil {
		return "", fmt.Errorf("preserve secrets: %w", err)
	}
	return string(out), nil
}

// redactConfig parses a JSON config string and replaces sensitive field
// values with a redacted placeholder. Returns the original string if
// parsing fails.
func redactConfig(configJSON string) string {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return configJSON
	}

	redacted := false
	for k, v := range cfg {
		if sensitiveConfigKeys[k] {
			if s, ok := v.(string); ok && s != "" {
				cfg[k] = redactionMarker
				redacted = true
			}
		}
	}

	if !redacted {
		return configJSON
	}

	out, err := json.Marshal(cfg)
	if err != nil {
		return configJSON
	}
	return string(out)
}

// storageResponseWithCapacity adapts a StorageDestination into the
// public JSON shape: it (a) redacts the config blob and (b) hoists the
// flat capacity_* columns into a single nested "capacity" sub-object.
// When the destination has never been probed (capacity_probed_at IS NULL),
// "capacity" is null — the UI uses that to render a "Check now" prompt.
//
// The flat columns are still present on the StorageDestination struct
// (and would otherwise appear in the JSON via the existing tags), so
// this helper returns a map[string]any rather than mutating the struct
// in place — JSON-marshalling a map is the cleanest way to omit the
// flat fields in favour of the nested object.
func storageResponseWithCapacity(d db.StorageDestination) map[string]any {
	out := map[string]any{
		"id":                       d.ID,
		"name":                     d.Name,
		"type":                     d.Type,
		"config":                   redactConfig(d.Config),
		"dedup_enabled":            d.DedupEnabled,
		"last_health_check_at":     d.LastHealthCheckAt,
		"last_health_check_status": d.LastHealthCheckStatus,
		"last_health_check_error":  d.LastHealthCheckError,
		"consecutive_failures":     d.ConsecutiveFailures,
		"breaker_state":            d.BreakerState,
		"breaker_opened_at":        d.BreakerOpenedAt,
		"backup_database_enabled":  d.BackupDatabaseEnabled,
		"created_at":               d.CreatedAt,
		"updated_at":               d.UpdatedAt,
	}
	if d.CapacityProbedAt != nil {
		cap := map[string]any{
			"probed_at": *d.CapacityProbedAt,
			"source":    d.CapacitySource,
			"error":     d.CapacityError,
		}
		if d.CapacityTotalBytes != nil {
			cap["total_bytes"] = *d.CapacityTotalBytes
		} else {
			cap["total_bytes"] = int64(0)
		}
		if d.CapacityUsedBytes != nil {
			cap["used_bytes"] = *d.CapacityUsedBytes
		} else {
			cap["used_bytes"] = int64(0)
		}
		if d.CapacityFreeBytes != nil {
			cap["free_bytes"] = *d.CapacityFreeBytes
		} else {
			cap["free_bytes"] = int64(0)
		}
		out["capacity"] = cap
	} else {
		out["capacity"] = nil
	}
	return out
}

// RefreshCapacity runs an on-demand capacity probe against a destination
// and persists the result. 30s ceiling (tighter than the scheduler's 60s
// because a human is waiting). Failures store the error in capacity_error
// but still return a non-2xx so the UI can show a toast.
//
// On success, broadcasts storage_capacity_updated on the WebSocket so
// any open Storage page repaints without a full GET round-trip.
//
//	POST /api/v1/storage/{id}/capacity-check
//	→ 200 { "capacity": { ...Capacity... } }
//	→ 404 { "error": "not found" }
//	→ 502 { "error": "<adapter or probe error>" }
//	→ 504 { "error": "probe timed out" }
func (h *StorageHandler) RefreshCapacity(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer storage.CloseAdapter(adapter)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cap, capErr := adapter.GetCapacity(ctx)
	if capErr != nil {
		_ = h.db.UpdateStorageDestinationCapacity(id, db.CapacityRecord{}, capErr.Error())
		if errors.Is(capErr, context.DeadlineExceeded) {
			respondError(w, http.StatusGatewayTimeout, "probe timed out")
			return
		}
		respondError(w, http.StatusBadGateway, capErr.Error())
		return
	}
	if persistErr := h.db.UpdateStorageDestinationCapacity(id, db.CapacityRecord{
		TotalBytes: cap.TotalBytes,
		UsedBytes:  cap.UsedBytes,
		FreeBytes:  cap.FreeBytes,
		ProbedAt:   cap.ProbedAt,
		Source:     cap.Source,
	}, ""); persistErr != nil {
		respondInternalError(w, persistErr)
		return
	}
	if h.runner != nil {
		h.runner.BroadcastStorageCapacity(id, cap)
	}
	respondJSON(w, http.StatusOK, map[string]any{"capacity": cap})
}
