package handlers

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"
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

// SetConfigChangeHook registers a function called after storage mutations to
// flush the database to USB flash.
func (h *StorageHandler) SetConfigChangeHook(fn ConfigChangeHook) {
	h.onConfigChange = fn
}

// notifyConfigChange calls the config change hook if set.
func (h *StorageHandler) notifyConfigChange() {
	if h.onConfigChange != nil {
		h.onConfigChange()
	}
}

func (h *StorageHandler) List(w http.ResponseWriter, r *http.Request) {
	dests, err := h.db.ListStorageDestinations()
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
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
	id, err := h.db.CreateStorageDestination(dest)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dest.ID = id
	respondJSON(w, http.StatusCreated, dest)
	h.notifyConfigChange()
}

func (h *StorageHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	dest.Config = redactConfig(dest.Config)
	respondJSON(w, http.StatusOK, dest)
}

func (h *StorageHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var dest db.StorageDestination
	if err := json.NewDecoder(r.Body).Decode(&dest); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	dest.ID = id
	if err := h.db.UpdateStorageDestination(dest); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, dest)
	h.notifyConfigChange()
}

func (h *StorageHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)

	// Check for dependent jobs.
	jobCount, err := h.db.CountJobsByStorageDestID(id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
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
				log.Printf("Warning: failed to clean up files for storage %d: %s", id, err.Error()) //nolint:gosec // id is int64 from URL param
			}
		}
	}

	if err := h.db.DeleteStorageDestination(id); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
	h.notifyConfigChange()
}

func (h *StorageHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
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

// Scan discovers existing backups on a storage destination by reading
// manifest.json files from each backup run directory.
//
//	POST /api/v1/storage/{id}/scan
func (h *StorageHandler) Scan(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "storage destination not found")
		return
	}

	manifests, err := h.runner.ScanStorageManifests(dest)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
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

	respondJSON(w, http.StatusOK, manifests)
}

// Import creates job and restore point records from previously scanned
// backup manifests. Jobs are matched by name; new jobs are created if
// they don't already exist.
//
//	POST /api/v1/storage/{id}/import
func (h *StorageHandler) Import(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)

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
		respondError(w, http.StatusInternalServerError, err.Error())
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
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
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

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to create storage adapter: "+err.Error())
		return
	}

	dbPath := req.StoragePath + "/vault.db"
	rc, err := adapter.Read(dbPath)
	if err != nil {
		respondError(w, http.StatusNotFound, "vault.db not found at "+dbPath)
		return
	}
	defer rc.Close()

	// Write to a temporary file first.
	tmpFile, err := os.CreateTemp("", "vault-restore-*.db")
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to create temp file: "+err.Error())
		return
	}
	tmpPath := tmpFile.Name()

	if _, err := io.Copy(tmpFile, rc); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		respondError(w, http.StatusInternalServerError, "failed to download database: "+err.Error())
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

	// Close current DB, swap files, re-open is handled by the caller
	// (daemon restart). For now, copy the temp file over the current DB.
	_ = h.db.Close()
	srcFile, err := os.Open(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		respondError(w, http.StatusInternalServerError, "failed to open restored database: "+err.Error())
		return
	}

	dstFile, err := os.Create(currentPath)
	if err != nil {
		_ = srcFile.Close()
		_ = os.Remove(tmpPath)
		respondError(w, http.StatusInternalServerError, "failed to replace database: "+err.Error())
		return
	}

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		_ = srcFile.Close()
		_ = dstFile.Close()
		_ = os.Remove(tmpPath)
		respondError(w, http.StatusInternalServerError, "failed to write database: "+err.Error())
		return
	}
	_ = srcFile.Close()
	_ = dstFile.Close()
	_ = os.Remove(tmpPath)

	// Remove WAL and SHM files (stale after replacement).
	_ = os.Remove(currentPath + "-wal")
	_ = os.Remove(currentPath + "-shm")

	respondJSON(w, http.StatusOK, map[string]any{
		"message": "Database restored successfully. Please restart the Vault daemon.",
	})
}

// DependentJobs returns the number of jobs that reference a storage destination.
//
//	GET /api/v1/storage/{id}/jobs
func (h *StorageHandler) DependentJobs(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	count, err := h.db.CountJobsByStorageDestID(id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"job_count": count})
}

// ListFiles lists files/directories at a given prefix on the storage.
//
//	GET /api/v1/storage/{id}/list?prefix=some/path
func (h *StorageHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "storage not found")
		return
	}

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "open storage: "+err.Error())
		return
	}

	prefix := r.URL.Query().Get("prefix")
	files, err := adapter.List(prefix)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "list files: "+err.Error())
		return
	}
	respondJSON(w, http.StatusOK, files)
}

// DownloadFile streams a file from the storage.
//
//	GET /api/v1/storage/{id}/files?path=some/file.tar.zst
func (h *StorageHandler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
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

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "open storage: "+err.Error())
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
		log.Printf("Warning: error streaming file %q: %v", filePath, err) //nolint:gosec // filePath is admin-configured storage path
	}
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
