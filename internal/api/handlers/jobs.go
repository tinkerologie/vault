package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/engine"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// ScheduleReloader is called after job CRUD to reload the cron scheduler.
type ScheduleReloader = func() error

// NextRunResolver returns the next scheduled run time for a job.
type NextRunResolver = func(jobID int64) (string, bool)

type JobHandler struct {
	db             *db.DB
	runner         *runner.Runner
	schedReload    ScheduleReloader
	nextRun        NextRunResolver
	onConfigChange ConfigChangeHook
}

func NewJobHandler(database *db.DB, r *runner.Runner, reload ScheduleReloader) *JobHandler {
	return &JobHandler{db: database, runner: r, schedReload: reload}
}

// SetNextRunResolver sets the function used to look up the next scheduled run.
func (h *JobHandler) SetNextRunResolver(fn NextRunResolver) {
	h.nextRun = fn
}

// SetConfigChangeHook registers a function called after job mutations to flush
// the database to USB flash.
func (h *JobHandler) SetConfigChangeHook(fn ConfigChangeHook) {
	h.onConfigChange = fn
}

// notifyConfigChange calls the config change hook if set.
func (h *JobHandler) notifyConfigChange() {
	if h.onConfigChange != nil {
		h.onConfigChange()
	}
}

// broadcastConfigChange sends a `config_changed` WebSocket event so that
// dashboards / 3-2-1 compliance widgets / recovery plans re-fetch derived
// state without requiring a full page reload. The `entity` field tells
// the client what changed (e.g., "job", "storage", "replication").
func (h *JobHandler) broadcastConfigChange(entity string) {
	if h.runner == nil {
		return
	}
	h.runner.Broadcast(map[string]any{
		"type":   "config_changed",
		"entity": entity,
	})
}

// reloadScheduler triggers a scheduler reload, logging any errors.
func (h *JobHandler) reloadScheduler() {
	if h.schedReload != nil {
		if err := h.schedReload(); err != nil {
			log.Printf("Warning: scheduler reload failed: %v", err)
		}
	}
}

func (h *JobHandler) List(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.db.ListJobs()
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, jobs)
}

func (h *JobHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		db.Job
		Items []db.JobItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	id, err := h.db.CreateJob(req.Job)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	for _, item := range req.Items {
		item.JobID = id
		if _, err := h.db.AddJobItem(item); err != nil {
			respondInternalError(w, err)
			return
		}
	}
	req.Job.ID = id
	respondJSON(w, http.StatusCreated, req.Job)
	h.reloadScheduler()
	h.notifyConfigChange()
	h.broadcastConfigChange("job")
}

func (h *JobHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	job, err := h.db.GetJob(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	items, _ := h.db.GetJobItems(id)
	respondJSON(w, http.StatusOK, map[string]any{"job": job, "items": items})
}

func (h *JobHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		db.Job
		Items []db.JobItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Job.ID = id
	if err := h.db.UpdateJob(req.Job); err != nil {
		respondInternalError(w, err)
		return
	}
	if req.Items != nil {
		if err := h.db.DeleteJobItems(id); err != nil {
			respondInternalError(w, err)
			return
		}
		for _, item := range req.Items {
			item.JobID = id
			if _, err := h.db.AddJobItem(item); err != nil {
				respondInternalError(w, err)
				return
			}
		}
	}
	respondJSON(w, http.StatusOK, req.Job)
	h.reloadScheduler()
	h.notifyConfigChange()
	h.broadcastConfigChange("job")
}

func (h *JobHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	// Optionally delete backup files from storage.
	if r.URL.Query().Get("deleteFiles") == "true" {
		if err := h.runner.CleanupJobStorage(id); err != nil {
			log.Printf("Warning: failed to clean up storage for job %d: %s", id, err.Error()) // #nosec G706 //nolint:gosec // id is int64 from URL param
			// Continue with DB deletion even if storage cleanup fails.
		}
	}

	if err := h.db.DeleteJob(id); err != nil {
		respondInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	h.reloadScheduler()
	h.notifyConfigChange()
	h.broadcastConfigChange("job")
}

func (h *JobHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	const maxLimit = 1000
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		parsed, err := strconv.Atoi(l)
		if err != nil || parsed < 1 {
			respondError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if parsed > maxLimit {
			parsed = maxLimit
		}
		limit = parsed
	}
	runs, err := h.db.GetJobRuns(id, limit)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, runs)
}

func (h *JobHandler) GetRestorePoints(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	job, err := h.db.GetJob(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	rps, err := h.db.ListRestorePoints(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, runner.AnnotateRestorePoints(job, rps))
}

// RestorePointContents returns the list of files inside an archive at a
// restore point, sourced from the engine-side tar index sidecar.
//
//	GET /api/v1/jobs/{id}/restore-points/{rpid}/contents?item=<itemName>&file=<archiveName>
//
// `item` selects the per-item subdirectory under the restore point's storage
// path (e.g. "Flash Drive"). `file` is the archive basename — when omitted
// the handler scans for any "*.index.json[.age]" sidecar and uses the first
// it finds (so callers can omit the file parameter for single-archive items
// like folders / plugins).
//
// On encrypted jobs the sidecar is uploaded as `<archive>.index.json.age`
// and is decrypted on the fly using the runner's configured passphrase.
// Returns 404 when no index sidecar exists (e.g. backups produced before
// this feature was added); the restore wizard falls back to whole-archive
// extraction in that case.
func (h *JobHandler) RestorePointContents(w http.ResponseWriter, r *http.Request) {
	jobID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rpID, ok := parseID(w, r, "rpid")
	if !ok {
		return
	}

	rp, err := h.db.GetRestorePoint(rpID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "restore point not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	if rp.JobID != jobID {
		respondError(w, http.StatusNotFound, "restore point not found")
		return
	}

	itemName := strings.TrimSpace(r.URL.Query().Get("item"))
	if itemName == "" {
		respondError(w, http.StatusBadRequest, "item query parameter is required")
		return
	}
	archiveName := strings.TrimSpace(r.URL.Query().Get("file"))

	job, err := h.db.GetJob(jobID)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	dest, err := h.db.GetStorageDestination(job.StorageDestID)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	defer storage.CloseAdapter(adapter)

	itemPrefix := path.Join(rp.StoragePath, itemName)

	// Resolve which sidecar to read. When `file` is supplied, build both
	// candidates explicitly (with and without .age). When `file` is
	// omitted, list the per-item directory and pick the first index file.
	candidates, err := resolveIndexCandidates(adapter, itemPrefix, archiveName)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	if len(candidates) == 0 {
		respondError(w, http.StatusNotFound, "no tar index sidecar found for this item")
		return
	}

	var (
		indexReader io.ReadCloser
		sidecarPath string
	)
	for _, candidate := range candidates {
		rc, err := adapter.Read(candidate)
		if err != nil {
			continue
		}
		indexReader = rc
		sidecarPath = candidate
		break
	}
	if indexReader == nil {
		respondError(w, http.StatusNotFound, "tar index sidecar not readable at any candidate path")
		return
	}
	defer indexReader.Close()

	var src io.Reader = indexReader
	if strings.HasSuffix(sidecarPath, ".age") {
		pass := h.runner.ResolvePassphrase()
		if pass == "" {
			respondError(w, http.StatusFailedDependency, "index is encrypted but no passphrase is configured")
			return
		}
		dec, err := crypto.DecryptReader(pass, indexReader)
		if err != nil {
			respondInternalError(w, err)
			return
		}
		defer dec.Close()
		src = dec
	}

	idx, err := engine.ReadTarIndex(src)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, idx)
}

// resolveIndexCandidates returns the list of storage paths to probe for the
// requested archive's tar index sidecar. When `archiveName` is supplied the
// list is just the two encryption variants of `<itemPrefix>/<archive>.index.json`.
// Otherwise the item directory is listed and any `*.index.json[.age]` files
// found are returned in their natural order (alphabetical from List()).
func resolveIndexCandidates(adapter storage.Adapter, itemPrefix, archiveName string) ([]string, error) {
	if archiveName != "" {
		// Strip any user-supplied .age suffix so we always probe the plain
		// path first then the encrypted variant.
		base := strings.TrimSuffix(archiveName, ".age")
		stem := path.Join(itemPrefix, base+engine.IndexSuffix)
		return []string{stem, stem + ".age"}, nil
	}
	entries, err := adapter.List(itemPrefix)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		base := path.Base(e.Path)
		if strings.HasSuffix(base, engine.IndexSuffix) || strings.HasSuffix(base, engine.IndexSuffix+".age") {
			out = append(out, e.Path)
		}
	}
	return out, nil
}

// RetentionPreview returns the impact of a hypothetical GFS retention
// policy against the job's current restore points without actually
// applying it. Used by the Jobs wizard to show "would keep X of Y" as the
// user tunes the keep_* fields.
//
//	GET /api/v1/jobs/{id}/retention-preview?keep_latest=3&keep_daily=7&keep_weekly=4&keep_monthly=12&keep_yearly=5
//
//	Returns: {
//	  "total_restore_points": N,
//	  "kept_directly":        []int64,  // IDs the policy would keep outright
//	  "kept_with_ancestors":  []int64,  // IDs kept once chain protection is layered on
//	  "would_delete":         []int64,  // IDs that would be pruned
//	}
func (h *JobHandler) RetentionPreview(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	if _, err := h.db.GetJob(id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "job not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	q := r.URL.Query()
	parseN := func(key string) int {
		if s := q.Get(key); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n >= 0 {
				return n
			}
		}
		return 0
	}
	policy := runner.GFSPolicy{
		KeepLatest:  parseN("keep_latest"),
		KeepDaily:   parseN("keep_daily"),
		KeepWeekly:  parseN("keep_weekly"),
		KeepMonthly: parseN("keep_monthly"),
		KeepYearly:  parseN("keep_yearly"),
	}
	rps, err := h.db.ListRestorePoints(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	sorted := sortRestorePointsNewestFirst(rps)

	if !policy.IsActive() {
		respondJSON(w, http.StatusOK, map[string]any{
			"total_restore_points": len(rps),
			"kept_directly":        []int64{},
			"kept_with_ancestors":  []int64{},
			"would_delete":         []int64{},
			"policy_active":        false,
		})
		return
	}

	direct := runner.GFSDirectlyKept(sorted, policy, time.Local)
	protected := runner.GFSProtectedRestorePointIDs(sorted, policy, time.Local)
	directIDs := make([]int64, 0, len(direct))
	for k := range direct {
		directIDs = append(directIDs, k)
	}
	protectedIDs := make([]int64, 0, len(protected))
	for k := range protected {
		protectedIDs = append(protectedIDs, k)
	}
	deleteIDs := make([]int64, 0, len(rps))
	for _, rp := range rps {
		if _, ok := protected[rp.ID]; !ok {
			deleteIDs = append(deleteIDs, rp.ID)
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"total_restore_points": len(rps),
		"kept_directly":        directIDs,
		"kept_with_ancestors":  protectedIDs,
		"would_delete":         deleteIDs,
		"policy_active":        true,
	})
}

// sortRestorePointsNewestFirst is a local copy of the chain_health helper
// so the API handler can pre-sort without importing internal sorting.
func sortRestorePointsNewestFirst(points []db.RestorePoint) []db.RestorePoint {
	out := make([]db.RestorePoint, len(points))
	copy(out, points)
	for i := 0; i < len(out)-1; i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].CreatedAt.After(out[i].CreatedAt) ||
				(out[j].CreatedAt.Equal(out[i].CreatedAt) && out[j].ID > out[i].ID) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// VerifyRestorePoint kicks off a verification of a restore point.
//
//	POST /api/v1/jobs/{id}/restore-points/{rpid}/verify  {"mode": "quick"|"deep"}
//
// Returns 202 + {"verify_run_id": N} so the caller can poll
// GET /verify-runs/{vrid} or subscribe to WebSocket verify_progress events.
func (h *JobHandler) VerifyRestorePoint(w http.ResponseWriter, r *http.Request) {
	jobID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rpID, ok := parseID(w, r, "rpid")
	if !ok {
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Mode == "" {
		req.Mode = string(runner.VerifyModeQuick)
	}
	mode := runner.VerifyMode(strings.ToLower(req.Mode))
	if !mode.IsValid() {
		respondError(w, http.StatusBadRequest, "mode must be 'quick' or 'deep'")
		return
	}

	rp, err := h.db.GetRestorePoint(rpID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "restore point not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	if rp.JobID != jobID {
		respondError(w, http.StatusNotFound, "restore point not found")
		return
	}

	id, err := h.runner.RunVerify(rp, mode)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusAccepted, map[string]any{
		"verify_run_id":    id,
		"restore_point_id": rp.ID,
		"mode":             string(mode),
	})
}

// GetVerifyRun returns the current state of a verify run.
//
//	GET /api/v1/jobs/{id}/verify-runs/{vrid}
func (h *JobHandler) GetVerifyRun(w http.ResponseWriter, r *http.Request) {
	_, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	vrID, ok := parseID(w, r, "vrid")
	if !ok {
		return
	}
	vr, err := h.db.GetVerifyRun(vrID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "verify run not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, vr)
}

// ListRestorePointVerifyRuns returns recent verify runs for a restore
// point. Used by the UI to render the per-restore-point verify-history
// badge ("Verified Deep · 2h ago ✓").
//
//	GET /api/v1/jobs/{id}/restore-points/{rpid}/verify-runs?limit=10
func (h *JobHandler) ListRestorePointVerifyRuns(w http.ResponseWriter, r *http.Request) {
	jobID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rpID, ok := parseID(w, r, "rpid")
	if !ok {
		return
	}
	rp, err := h.db.GetRestorePoint(rpID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "restore point not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	if rp.JobID != jobID {
		respondError(w, http.StatusNotFound, "restore point not found")
		return
	}
	limit := 10
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, parseErr := strconv.Atoi(s); parseErr == nil && n > 0 {
			limit = n
		}
	}
	rows, err := h.db.ListVerifyRunsForRestorePoint(rpID, limit)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, rows)
}

// DeleteRestorePoint deletes a single restore point and its storage files.
//
//	DELETE /api/v1/jobs/{id}/restore-points/{rpid}
func (h *JobHandler) DeleteRestorePoint(w http.ResponseWriter, r *http.Request) {
	jobID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rpID, ok := parseID(w, r, "rpid")
	if !ok {
		return
	}

	rp, err := h.db.GetRestorePoint(rpID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "restore point not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	if rp.JobID != jobID {
		respondError(w, http.StatusNotFound, "restore point not found")
		return
	}

	// Delete the backup files from storage if a path is recorded.
	if rp.StoragePath != "" {
		job, err := h.db.GetJob(jobID)
		if err == nil {
			dest, err := h.db.GetStorageDestination(job.StorageDestID)
			if err == nil {
				adapter, err := storage.NewAdapter(dest.Type, dest.Config)
				if err == nil {
					defer storage.CloseAdapter(adapter)
					h.runner.DeleteStorageDir(adapter, rp.StoragePath)
				} else {
					log.Printf("handlers: failed to create adapter for restore point deletion (job %d, rp %d): %v", jobID, rpID, err) // #nosec G706 //nolint:gosec // jobID and rpID are int64 from URL params
				}
			}
		}
	}

	if err := h.db.DeleteRestorePoint(rpID); err != nil {
		respondInternalError(w, err)
		return
	}

	h.db.LogActivity("info", "system", fmt.Sprintf("Restore point #%d deleted", rpID),
		fmt.Sprintf(`{"restore_point_id":%d,"job_id":%d}`, rpID, jobID))
	w.WriteHeader(http.StatusNoContent)
}

// RunNow triggers an immediate backup run for a job.
//
//	POST /api/v1/jobs/{id}/run
func (h *JobHandler) RunNow(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	_, err := h.db.GetJob(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "job not found")
		return
	}

	// Run the backup asynchronously.
	go h.runner.RunJob(id)

	respondJSON(w, http.StatusAccepted, map[string]any{
		"message": "backup started",
		"job_id":  id,
	})
}

// Cancel requests cancellation of a currently running job.
//
//	POST /api/v1/jobs/{id}/cancel
func (h *JobHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	if err := h.runner.CancelJob(id); err != nil {
		respondError(w, http.StatusConflict, err.Error())
		return
	}
	respondJSON(w, http.StatusAccepted, map[string]any{
		"message": "cancellation requested",
		"job_id":  id,
	})
}

// RunnerStatus returns the current state of the backup/restore runner.
//
//	GET /api/v1/runner/status
func (h *JobHandler) RunnerStatus(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.runner.Status())
}

// Restore triggers a restore from a specific restore point.
//
//	POST /api/v1/jobs/{id}/restore
func (h *JobHandler) Restore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RestorePointID int64    `json:"restore_point_id"`
		Items          []string `json:"items"`
		ItemName       string   `json:"item_name"`
		ItemType       string   `json:"item_type"`
		Destination    string   `json:"destination"`
		Passphrase     string   `json:"passphrase"`
		// FilePaths is the optional per-item include-list used by the
		// partial-restore file picker. Keys are item names; values are
		// tar entry paths chosen from the index sidecar. Items absent
		// from this map (or with an empty slice) restore everything.
		FilePaths map[string][]string `json:"file_paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.RestorePointID == 0 {
		respondError(w, http.StatusBadRequest, "restore_point_id is required")
		return
	}

	// Find the restore point in the database.
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rps, err := h.db.ListRestorePoints(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}

	var found *db.RestorePoint
	for _, rp := range rps {
		if rp.ID == req.RestorePointID {
			found = &rp
			break
		}
	}
	if found == nil {
		respondError(w, http.StatusNotFound, "restore point not found")
		return
	}

	// Build the list of items to restore. Supports three modes:
	// 1. Legacy single item: item_name + item_type
	// 2. Named list: items array (types resolved from job_items)
	// 3. All items: no items/item_name → restore everything in the job
	type restoreTarget struct {
		Name string
		Type string
	}

	var targets []restoreTarget

	if req.ItemName != "" && req.ItemType != "" {
		// Legacy single-item restore.
		targets = append(targets, restoreTarget{Name: req.ItemName, Type: req.ItemType})
	} else {
		// Look up job items to resolve types.
		jobItems, itemsErr := h.db.GetJobItems(id)
		if itemsErr != nil {
			respondInternalError(w, fmt.Errorf("fetching job items: %w", itemsErr))
			return
		}
		itemTypeMap := make(map[string]string, len(jobItems))
		for _, ji := range jobItems {
			itemTypeMap[ji.ItemName] = ji.ItemType
		}

		if len(req.Items) > 0 {
			// Restore specific named items.
			for _, name := range req.Items {
				iType, ok := itemTypeMap[name]
				if !ok {
					respondError(w, http.StatusBadRequest, "item not found in job: "+name)
					return
				}
				targets = append(targets, restoreTarget{Name: name, Type: iType})
			}
		} else {
			// Restore all items from the job.
			for _, ji := range jobItems {
				targets = append(targets, restoreTarget{Name: ji.ItemName, Type: ji.ItemType})
			}
		}
	}

	if len(targets) == 0 {
		respondError(w, http.StatusBadRequest, "no items to restore")
		return
	}

	// Build runner targets and execute tracked restore asynchronously.
	runnerTargets := make([]runner.RestoreTarget, 0, len(targets))
	for _, t := range targets {
		runnerTargets = append(runnerTargets, runner.RestoreTarget{
			Name:      t.Name,
			Type:      t.Type,
			FilePaths: req.FilePaths[t.Name],
		})
	}

	go h.runner.RunRestore(*found, runnerTargets, req.Destination, req.Passphrase)

	respondJSON(w, http.StatusAccepted, map[string]any{
		"message":          "restore started",
		"restore_point_id": found.ID,
		"items":            len(targets),
	})
}

// NextRun returns the next scheduled run time for a single job.
//
//	GET /api/v1/jobs/{id}/next-run
func (h *JobHandler) NextRun(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	if h.nextRun == nil {
		respondJSON(w, http.StatusOK, map[string]any{"scheduled": false})
		return
	}
	next, ok := h.nextRun(id)
	if !ok {
		respondJSON(w, http.StatusOK, map[string]any{"scheduled": false})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"scheduled": true, "next_run": next})
}

// AllNextRuns returns next scheduled run times for all jobs.
//
//	GET /api/v1/jobs/next-runs
func (h *JobHandler) AllNextRuns(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.db.ListJobs()
	if err != nil {
		respondInternalError(w, err)
		return
	}
	result := make(map[string]any)
	for _, job := range jobs {
		if h.nextRun != nil {
			if next, ok := h.nextRun(job.ID); ok {
				result[strconv.FormatInt(job.ID, 10)] = next
			}
		}
	}
	respondJSON(w, http.StatusOK, result)
}
