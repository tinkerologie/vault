package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/storage"
)

type StorageHandler struct {
	db             *db.DB
	runner         *runner.Runner
	onConfigChange ConfigChangeHook
}

func NewStorageHandler(database *db.DB, r *runner.Runner) *StorageHandler {
	return &StorageHandler{db: database, runner: r}
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
	for i := range dests {
		dests[i].Config = redactConfig(dests[i].Config)
	}
	respondJSON(w, http.StatusOK, dests)
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
		respondInternalError(w, err)
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
	saved.Config = redactConfig(saved.Config)
	respondJSON(w, http.StatusCreated, saved)
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
	dest.Config = redactConfig(dest.Config)
	respondJSON(w, http.StatusOK, dest)
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
		Name         *string `json:"name"`
		Type         *string `json:"type"`
		Config       *string `json:"config"`
		DedupEnabled *bool   `json:"dedup_enabled"`
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
		existing.Config = *patch.Config
		// Re-validate; the user may have broken the config blob.
		adapter, err := storage.NewAdapter(existing.Type, existing.Config)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		storage.CloseAdapter(adapter)
	}
	if err := h.db.UpdateStorageDestination(existing); err != nil {
		respondInternalError(w, err)
		return
	}
	saved, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	saved.Config = redactConfig(saved.Config)
	respondJSON(w, http.StatusOK, saved)
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

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		respondInternalError(w, err)
		return
	}

	dbPath := strings.TrimPrefix(cleanedStoragePath, "/") + "/vault.db"
	rc, err := adapter.Read(dbPath)
	if err != nil {
		respondError(w, http.StatusNotFound, "vault.db not found at "+dbPath)
		return
	}
	defer rc.Close()

	// Write to a temporary file first.
	tmpFile, err := os.CreateTemp("", "vault-restore-*.db")
	if err != nil {
		respondInternalError(w, err)
		return
	}
	tmpPath := tmpFile.Name()

	if _, err := io.Copy(tmpFile, rc); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		respondInternalError(w, err)
		return
	}
	_ = tmpFile.Close()

	// Validate the downloaded database.
	testDB, err := db.Open(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		respondError(w, http.StatusBadRequest, "downloaded file is not a valid Vault database: "+err.Error())
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
	if _, statErr := os.Stat(currentPath); statErr == nil {
		if err := os.Rename(currentPath, backupPath); err != nil {
			_ = os.Remove(tmpPath)
			respondInternalError(w, fmt.Errorf("backup current DB: %w", err))
			return
		}
		backupExists = true
	}
	restoreBackup := func() {
		if backupExists {
			_ = os.Rename(backupPath, currentPath)
		}
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

	// Success — drop backup and temp.
	if backupExists {
		_ = os.Remove(backupPath)
	}
	_ = os.Remove(tmpPath)

	// Remove WAL and SHM files (stale after replacement).
	_ = os.Remove(currentPath + "-wal")
	_ = os.Remove(currentPath + "-shm")

	respondJSON(w, http.StatusOK, map[string]any{
		"message": "Database restored successfully. Please restart the Vault daemon.",
	})
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

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		respondInternalError(w, err)
		return
	}

	prefix := r.URL.Query().Get("prefix")
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

// GetDedupStats returns the in-memory dedup stats snapshot for a
// destination's chunk repository. Returns 404 if the destination is not
// dedup-enabled — the field-level "enabled" key in the response body lets
// the UI render a friendly empty state on the same endpoint when
// 404 handling would be noisy.
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
				cfg[k] = "••••••••"
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
