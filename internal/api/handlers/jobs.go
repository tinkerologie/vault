package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/ruaandeysel/vault/internal/db"
)

type JobHandler struct {
	db *db.DB
}

func NewJobHandler(database *db.DB) *JobHandler {
	return &JobHandler{db: database}
}

func (h *JobHandler) List(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.db.ListJobs()
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
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
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, item := range req.Items {
		item.JobID = id
		if _, err := h.db.AddJobItem(item); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	req.Job.ID = id
	respondJSON(w, http.StatusCreated, req.Job)
}

func (h *JobHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	job, err := h.db.GetJob(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	items, _ := h.db.GetJobItems(id)
	respondJSON(w, http.StatusOK, map[string]any{"job": job, "items": items})
}

func (h *JobHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
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
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.Items != nil {
		if err := h.db.DeleteJobItems(id); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, item := range req.Items {
			item.JobID = id
			if _, err := h.db.AddJobItem(item); err != nil {
				respondError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	respondJSON(w, http.StatusOK, req.Job)
}

func (h *JobHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := h.db.DeleteJob(id); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *JobHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil {
			limit = parsed
		}
	}
	runs, err := h.db.GetJobRuns(id, limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, runs)
}

func (h *JobHandler) GetRestorePoints(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	rps, err := h.db.ListRestorePoints(id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, rps)
}
