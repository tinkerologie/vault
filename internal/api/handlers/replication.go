package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/replication"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// SyncerProvider returns the replication syncer (resolved lazily).
type SyncerProvider = func() *replication.Syncer

// replicationSourceRequest is the write-side DTO for creating/updating
// replication sources. It mirrors db.ReplicationSource but includes
// api_key so clients can supply it (the model uses json:"-" to prevent
// the key from leaking in responses).
type replicationSourceRequest struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	URL           string `json:"url"`
	Config        string `json:"config"`
	APIKey        string `json:"api_key"`
	StorageDestID int64  `json:"storage_dest_id"`
	Schedule      string `json:"schedule"`
	Enabled       bool   `json:"enabled"`
}

// toModel converts the request DTO to a db.ReplicationSource.
func (r *replicationSourceRequest) toModel() db.ReplicationSource {
	return db.ReplicationSource{
		Name:          r.Name,
		Type:          r.Type,
		URL:           r.URL,
		Config:        r.Config,
		APIKey:        r.APIKey,
		StorageDestID: r.StorageDestID,
		Schedule:      r.Schedule,
		Enabled:       r.Enabled,
	}
}

// ReplicationHandler handles CRUD and sync operations for replication sources.
type ReplicationHandler struct {
	db             *db.DB
	getSyncer      SyncerProvider
	serverKey      []byte
	schedReload    ScheduleReloader
	onConfigChange ConfigChangeHook
}

// NewReplicationHandler creates a new ReplicationHandler.
func NewReplicationHandler(database *db.DB, getSyncer SyncerProvider, serverKey []byte, reload ScheduleReloader) *ReplicationHandler {
	return &ReplicationHandler{
		db:          database,
		getSyncer:   getSyncer,
		serverKey:   serverKey,
		schedReload: reload,
	}
}

// reloadScheduler triggers a scheduler reload, logging any errors.
func (h *ReplicationHandler) reloadScheduler() {
	if h.schedReload != nil {
		if err := h.schedReload(); err != nil {
			log.Printf("Warning: scheduler reload failed: %v", err)
		}
	}
}

// SetConfigChangeHook registers a function called after replication mutations
// to flush the database to USB flash.
func (h *ReplicationHandler) SetConfigChangeHook(fn ConfigChangeHook) {
	h.onConfigChange = fn
}

// notifyConfigChange calls the config change hook if set.
func (h *ReplicationHandler) notifyConfigChange() {
	if h.onConfigChange != nil {
		h.onConfigChange()
	}
}

// List returns all replication sources.
func (h *ReplicationHandler) List(w http.ResponseWriter, r *http.Request) {
	sources, err := h.db.ListReplicationSources()
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, sources)
}

// Create adds a new replication source.
func (h *ReplicationHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req replicationSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	src := req.toModel()

	if src.Name == "" {
		respondError(w, http.StatusBadRequest, "name is required")
		return
	}
	// Default type to remote_vault if not provided.
	if src.Type == "" {
		src.Type = "remote_vault"
	}

	switch src.Type {
	case "remote_vault":
		if src.URL == "" {
			respondError(w, http.StatusBadRequest, "url is required for remote_vault targets")
			return
		}
		normalizedURL, err := replication.NormalizeBaseURL(src.URL)
		if err != nil {
			respondError(w, http.StatusBadRequest, "invalid url: "+err.Error())
			return
		}
		src.URL = normalizedURL
	case "gdrive", "onedrive":
		if src.Config == "" || src.Config == "{}" {
			respondError(w, http.StatusBadRequest, "config is required for cloud targets")
			return
		}
		src.StorageDestID = 0
	default:
		respondError(w, http.StatusBadRequest, "invalid type: must be remote_vault, gdrive, or onedrive")
		return
	}

	id, err := h.db.CreateReplicationSource(src)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	src.ID = id

	h.reloadScheduler()
	h.notifyConfigChange()
	respondJSON(w, http.StatusCreated, src)
}

// Get returns a single replication source.
func (h *ReplicationHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	src, err := h.db.GetReplicationSource(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	respondJSON(w, http.StatusOK, src)
}

// Update modifies a replication source.
func (h *ReplicationHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)

	var req replicationSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	src := req.toModel()
	src.ID = id

	if src.Type == "" {
		src.Type = "remote_vault"
	}

	switch src.Type {
	case "remote_vault":
		if src.URL != "" {
			normalizedURL, err := replication.NormalizeBaseURL(src.URL)
			if err != nil {
				respondError(w, http.StatusBadRequest, "invalid url: "+err.Error())
				return
			}
			src.URL = normalizedURL
		}
	case "gdrive", "onedrive":
		src.StorageDestID = 0
	}

	if err := h.db.UpdateReplicationSource(src); err != nil {
		respondInternalError(w, err)
		return
	}

	h.reloadScheduler()
	h.notifyConfigChange()
	respondJSON(w, http.StatusOK, src)
}

// Delete removes a replication source and its replicated jobs.
func (h *ReplicationHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)

	// Delete replicated jobs first.
	if err := h.db.DeleteReplicatedJobs(id); err != nil {
		log.Printf("Warning: failed to delete replicated jobs for source %d: %v", id, err) //nolint:gosec // id is int64 from URL param
	}

	if err := h.db.DeleteReplicationSource(id); err != nil {
		respondInternalError(w, err)
		return
	}

	h.reloadScheduler()
	h.notifyConfigChange()
	w.WriteHeader(http.StatusNoContent)
}

// TestConnection tests connectivity to a replication target.
func (h *ReplicationHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	src, err := h.db.GetReplicationSource(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}

	switch src.Type {
	case "gdrive", "onedrive":
		adapter, adapterErr := storage.NewAdapter(src.Type, src.Config)
		if adapterErr != nil {
			respondError(w, http.StatusBadRequest, "invalid config: "+adapterErr.Error())
			return
		}
		if connErr := adapter.TestConnection(); connErr != nil {
			respondError(w, http.StatusBadGateway, connErr.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default: // remote_vault
		var client *replication.Client
		var clientErr error
		if src.APIKey != "" {
			client, clientErr = replication.NewClientWithAPIKey(src.URL, src.APIKey)
		} else {
			client, clientErr = replication.NewClient(src.URL)
		}
		if clientErr != nil {
			respondError(w, http.StatusBadRequest, "invalid url: "+clientErr.Error())
			return
		}
		if _, connErr := client.TestConnection(); connErr != nil {
			respondError(w, http.StatusBadGateway, connErr.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// TestURL validates and normalizes a remote Vault URL before it is saved.
// Accepts JSON body: {"url": "http://..."}.
func (h *ReplicationHandler) TestURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.URL == "" {
		respondError(w, http.StatusBadRequest, "url is required")
		return
	}

	normalizedURL, err := replication.NormalizeBaseURL(req.URL)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid url: "+err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"url":     normalizedURL,
		"message": "URL format is valid. Save the source and use Test Connection to verify remote reachability.",
	})
}

// SyncNow triggers an immediate sync for a replication source.
func (h *ReplicationHandler) SyncNow(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	syncer := h.getSyncer()
	if syncer == nil {
		respondError(w, http.StatusServiceUnavailable, "replication syncer not available")
		return
	}

	// Run sync in background, return immediately.
	go func() {
		if _, err := syncer.SyncSource(id, nil); err != nil {
			log.Printf("Manual sync failed for source %d: %v", id, err)
		}
	}()

	respondJSON(w, http.StatusAccepted, map[string]string{"status": "sync_started"})
}

// ListReplicatedJobs returns jobs replicated from a specific source.
func (h *ReplicationHandler) ListReplicatedJobs(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	jobs, err := h.db.ListReplicatedJobs(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, jobs)
}
