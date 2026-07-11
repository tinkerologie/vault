package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/unraid"
)

// RecoveryHandler serves the disaster recovery plan.
type RecoveryHandler struct {
	db             *db.DB
	version        string
	onConfigChange ConfigChangeHook

	// maintMu guards PathRemap's writes against racing a database restore
	// (a remap committing mid-swap would be silently lost). Defaults to an
	// own lock; production shares StorageHandler's via SetMaintenanceLock.
	maintMu *sync.Mutex
}

// NewRecoveryHandler creates a RecoveryHandler.
func NewRecoveryHandler(database *db.DB, version string) *RecoveryHandler {
	return &RecoveryHandler{db: database, version: version, maintMu: &sync.Mutex{}}
}

// SetMaintenanceLock shares StorageHandler's restore lifecycle lock so
// PathRemap never writes while a database restore swaps the file.
func (h *RecoveryHandler) SetMaintenanceLock(mu *sync.Mutex) {
	h.maintMu = mu
}

// SetConfigChangeHook registers a function called after path remaps mutate
// persistent configuration (typically used by the daemon to flush the DB
// snapshot to USB flash).
func (h *RecoveryHandler) SetConfigChangeHook(fn ConfigChangeHook) {
	h.onConfigChange = fn
}

// GetPlan compiles a disaster recovery plan from existing data.
//
//	GET /api/v1/recovery/plan
func (h *RecoveryHandler) GetPlan(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.db.ListJobs()
	if err != nil {
		respondInternalError(w, err)
		return
	}

	storageDests, err := h.db.ListStorageDestinations()
	if err != nil {
		respondInternalError(w, err)
		return
	}

	// Build storage name lookup.
	storageNames := make(map[int64]string)
	for _, s := range storageDests {
		storageNames[s.ID] = s.Name
	}

	// Collect all items, latest restore points, and warnings.
	type stepItem struct {
		Name            string     `json:"name"`
		Type            string     `json:"type"` // container | vm | folder | zfs | plugin
		LastBackup      *time.Time `json:"last_backup"`
		StorageName     string     `json:"storage_name"`
		SizeBytes       int64      `json:"size_bytes"`
		HasRestorePoint bool       `json:"has_restore_point"`
	}

	type step struct {
		Step        int        `json:"step"`
		Title       string     `json:"title"`
		Description string     `json:"description"`
		Status      string     `json:"status"` // "ready", "warning", "not_available"
		Items       []stepItem `json:"items,omitempty"`
		TotalSize   int64      `json:"total_size,omitempty"`
	}

	var warnings []string
	var steps []step
	var totalProtected int

	containerItems := []stepItem{}
	vmItems := []stepItem{}
	folderItems := []stepItem{}

	for _, job := range jobs {
		// Include all jobs regardless of schedule-enabled state: a disabled
		// schedule does not mean past restore points should disappear from
		// the recovery plan. We surface protection based on actual restore
		// points, not on whether the next run is scheduled.
		items, err := h.db.GetJobItems(job.ID)
		if err != nil {
			continue
		}
		rps, err := h.db.ListRestorePoints(job.ID)
		if err != nil {
			rps = nil
		}

		var latestRP *db.RestorePoint
		if len(rps) > 0 {
			latestRP = &rps[0]
		}

		for _, item := range items {
			totalProtected++
			si := stepItem{
				Name:            item.ItemName,
				Type:            item.ItemType,
				StorageName:     storageNames[job.StorageDestID],
				HasRestorePoint: latestRP != nil,
			}
			if latestRP != nil {
				si.LastBackup = &latestRP.CreatedAt
				if len(items) > 0 {
					si.SizeBytes = latestRP.SizeBytes / int64(len(items)) // approximate per-item
				}
			}

			// Warn if last backup is older than 7 days.
			if latestRP == nil {
				warnings = append(warnings, item.ItemName+" has no restore points")
			} else if time.Since(latestRP.CreatedAt) > 7*24*time.Hour {
				warnings = append(warnings, item.ItemName+" last backed up "+latestRP.CreatedAt.Format("Jan 2")+" (>7 days ago)")
			}

			switch item.ItemType {
			case "container":
				containerItems = append(containerItems, si)
			case "vm":
				vmItems = append(vmItems, si)
			case "folder":
				folderItems = append(folderItems, si)
			}
		}
	}

	stepNum := 1

	// Step 1: Install Vault
	steps = append(steps, step{
		Step:        stepNum,
		Title:       "Install Vault Plugin",
		Description: "Install the Vault plugin from Community Applications and restore the database from your backup storage.",
		Status:      "ready",
	})
	stepNum++

	// stepStatus inspects every item in the step (not just the first) and
	// returns "warning" if any one of them has no restore point yet. The
	// original implementation only checked items[0], so a step rendered as
	// "ready" even when later items would fail the restore.
	stepStatus := func(items []stepItem) string {
		for _, it := range items {
			if !it.HasRestorePoint {
				return "warning"
			}
		}
		return "ready"
	}

	// Step 2: Restore Containers
	if len(containerItems) > 0 {
		var totalSize int64
		for _, c := range containerItems {
			totalSize += c.SizeBytes
		}
		steps = append(steps, step{
			Step:        stepNum,
			Title:       fmt.Sprintf("Restore Containers (%d)", len(containerItems)),
			Description: "Restore all Docker container appdata from backup.",
			Status:      stepStatus(containerItems),
			Items:       containerItems,
			TotalSize:   totalSize,
		})
		stepNum++
	}

	// Step 3: Restore VMs
	if len(vmItems) > 0 {
		var totalSize int64
		for _, v := range vmItems {
			totalSize += v.SizeBytes
		}
		steps = append(steps, step{
			Step:        stepNum,
			Title:       fmt.Sprintf("Restore Virtual Machines (%d)", len(vmItems)),
			Description: "Restore VM disk images and configurations from backup.",
			Status:      stepStatus(vmItems),
			Items:       vmItems,
			TotalSize:   totalSize,
		})
		stepNum++
	}

	// Step 4: Restore Folders
	if len(folderItems) > 0 {
		var totalSize int64
		for _, f := range folderItems {
			totalSize += f.SizeBytes
		}
		steps = append(steps, step{
			Step:        stepNum,
			Title:       fmt.Sprintf("Restore Folders (%d)", len(folderItems)),
			Description: "Restore custom folder backups (Flash Drive, shares, etc.).",
			Status:      stepStatus(folderItems),
			Items:       folderItems,
			TotalSize:   totalSize,
		})
	}

	result := map[string]any{
		"server_info": map[string]any{
			"vault_version":           h.version,
			"total_protected_items":   totalProtected,
			"total_unprotected_items": 0,
		},
		"steps":        steps,
		"warnings":     warnings,
		"last_updated": time.Now().UTC(),
	}
	respondJSON(w, http.StatusOK, result)
}

type pathAuditEntry struct {
	Kind   string `json:"kind"` // "storage" or "job_item"
	ID     int64  `json:"id"`
	JobID  int64  `json:"job_id,omitempty"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

func dirExists(p string) bool {
	// Only absolute, traversal-free paths are ever stat'ed. Mount points and
	// share paths are always absolute, so this rejects nothing legitimate —
	// and it is the sanitization barrier for remap's user-provided new_path.
	if !filepath.IsAbs(p) || strings.Contains(p, "..") {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// PathAudit reports which configured local paths exist on this system —
// after a DR restore the config references the OLD server's layout.
//
//	GET /api/v1/recovery/path-audit
func (h *RecoveryHandler) PathAudit(w http.ResponseWriter, _ *http.Request) {
	entries := []pathAuditEntry{}

	dests, err := h.db.ListStorageDestinations()
	if err != nil {
		respondInternalError(w, err)
		return
	}
	for _, d := range dests {
		if d.Type != "local" {
			continue
		}
		var cfg struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(d.Config), &cfg); err != nil {
			log.Printf("path-audit: skipping storage %d (%s): malformed config: %v", d.ID, d.Name, err)
			continue
		}
		if cfg.Path == "" {
			continue
		}
		entries = append(entries, pathAuditEntry{Kind: "storage", ID: d.ID, Name: d.Name, Path: cfg.Path, Exists: dirExists(cfg.Path)})
	}

	jobs, err := h.db.ListJobs()
	if err != nil {
		respondInternalError(w, err)
		return
	}
	for _, j := range jobs {
		items, ierr := h.db.GetJobItems(j.ID)
		if ierr != nil {
			log.Printf("path-audit: skipping job %d: %v", j.ID, ierr)
			continue
		}
		for _, it := range items {
			if it.ItemType != "folder" {
				continue
			}
			var s struct {
				Path string `json:"path"`
			}
			if json.Unmarshal([]byte(it.Settings), &s) != nil || s.Path == "" {
				continue
			}
			entries = append(entries, pathAuditEntry{Kind: "job_item", ID: it.ID, JobID: j.ID, Name: it.ItemName, Path: s.Path, Exists: dirExists(s.Path)})
		}
	}

	// Candidate mounts to pick a replacement path from. Empty off-Unraid.
	candidates := append([]string{}, unraid.DiscoverPools()...)
	if shares, rerr := os.ReadDir("/mnt/user"); rerr == nil {
		for _, sh := range shares {
			if sh.IsDir() {
				candidates = append(candidates, "/mnt/user/"+sh.Name())
			}
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{"entries": entries, "candidates": candidates})
}

// PathRemap applies user-chosen replacement paths. Per-row results — one
// bad row never blocks the others, and nothing is ever auto-rewritten.
//
//	POST /api/v1/recovery/path-remap
func (h *RecoveryHandler) PathRemap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Updates []struct {
			Kind    string `json:"kind"`
			ID      int64  `json:"id"`
			JobID   int64  `json:"job_id"`
			NewPath string `json:"new_path"`
		} `json:"updates"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// A remap committing while RestoreDB swaps the database file would be
	// silently lost — share the maintenance lock instead of racing it.
	if !h.maintMu.TryLock() {
		respondError(w, http.StatusConflict, "a database restore is in progress — try again when it finishes")
		return
	}
	defer h.maintMu.Unlock()
	type remapResult struct {
		Kind    string `json:"kind"`
		ID      int64  `json:"id"`
		Applied bool   `json:"applied"`
		Error   string `json:"error,omitempty"`
	}
	results := make([]remapResult, 0, len(req.Updates))
	anyApplied := false
	for _, u := range req.Updates {
		res := remapResult{Kind: u.Kind, ID: u.ID}
		switch {
		case !dirExists(u.NewPath):
			res.Error = "that path doesn't exist on this system — pick one of the suggested mounts or create it first"
		case u.Kind == "storage":
			res.Applied, res.Error = h.remapStorage(u.ID, u.NewPath)
		case u.Kind == "job_item":
			res.Applied, res.Error = h.remapJobItem(u.JobID, u.ID, u.NewPath)
		default:
			res.Error = "unknown kind"
		}
		anyApplied = anyApplied || res.Applied
		results = append(results, res)
	}
	if anyApplied && h.onConfigChange != nil {
		h.onConfigChange()
	}
	respondJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (h *RecoveryHandler) remapStorage(id int64, newPath string) (bool, string) {
	dest, err := h.db.GetStorageDestination(id)
	if err != nil {
		return false, "storage destination not found"
	}
	if dest.Type != "local" {
		return false, "only local destinations can be remapped"
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(dest.Config), &cfg); err != nil {
		log.Printf("path-remap: storage %d: malformed config: %v", id, err)
		return false, "could not read destination config"
	}
	cfg["path"] = newPath
	raw, err := json.Marshal(cfg)
	if err != nil {
		log.Printf("path-remap: storage %d: marshal config: %v", id, err)
		return false, "could not update destination config"
	}
	dest.Config = string(raw)
	if err := h.db.UpdateStorageDestination(dest); err != nil {
		log.Printf("path-remap: storage %d: save: %v", id, err)
		return false, "saving destination failed"
	}
	return true, ""
}

func (h *RecoveryHandler) remapJobItem(jobID, itemID int64, newPath string) (bool, string) {
	items, err := h.db.GetJobItems(jobID)
	if err != nil {
		return false, "job not found"
	}
	for _, it := range items {
		if it.ID != itemID {
			continue
		}
		if it.ItemType != "folder" {
			return false, "only folder items can be remapped"
		}
		var s map[string]any
		if err := json.Unmarshal([]byte(it.Settings), &s); err != nil {
			log.Printf("path-remap: job item %d: malformed settings: %v", itemID, err)
			return false, "could not read item settings"
		}
		s["path"] = newPath
		raw, err := json.Marshal(s)
		if err != nil {
			log.Printf("path-remap: job item %d: marshal settings: %v", itemID, err)
			return false, "could not update item settings"
		}
		if err := h.db.UpdateJobItemSettings(itemID, string(raw)); err != nil {
			log.Printf("path-remap: job item %d: save: %v", itemID, err)
			return false, "saving item failed"
		}
		return true, ""
	}
	return false, "job item not found"
}
