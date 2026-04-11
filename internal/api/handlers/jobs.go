package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/runner"
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
}

func (h *JobHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	// Optionally delete backup files from storage.
	if r.URL.Query().Get("deleteFiles") == "true" {
		if err := h.runner.CleanupJobStorage(id); err != nil {
			log.Printf("Warning: failed to clean up storage for job %d: %s", id, err.Error()) //nolint:gosec // id is int64 from URL param
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
}

func (h *JobHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	const maxLimit = 1000
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			if parsed > maxLimit {
				parsed = maxLimit
			}
			limit = parsed
		}
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
		runnerTargets = append(runnerTargets, runner.RestoreTarget{Name: t.Name, Type: t.Type})
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
