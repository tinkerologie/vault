package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/replication"
)

// testConnectionTimeout is the HTTP client timeout used for connectivity
// checks so that Test Connection / Test URL fail fast instead of blocking
// for the default 10-minute transfer timeout.
const testConnectionTimeout = 15 * time.Second

// SyncerProvider returns the replication syncer (resolved lazily).
type SyncerProvider = func() *replication.Syncer

// ReplicationHandler handles CRUD and sync operations for replication sources.
type ReplicationHandler struct {
	db          *db.DB
	getSyncer   SyncerProvider
	serverKey   []byte
	schedReload ScheduleReloader
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
	var src db.ReplicationSource
	if err := json.NewDecoder(r.Body).Decode(&src); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
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
	default:
		respondError(w, http.StatusBadRequest, "invalid type: must be remote_vault")
		return
	}

	id, err := h.db.CreateReplicationSource(src)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	src.ID = id

	h.reloadScheduler()
	respondJSON(w, http.StatusCreated, src)
}

// Get returns a single replication source.
func (h *ReplicationHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	src, err := h.db.GetReplicationSource(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	respondJSON(w, http.StatusOK, src)
}

// Update modifies a replication source.
func (h *ReplicationHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	var src db.ReplicationSource
	if err := json.NewDecoder(r.Body).Decode(&src); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
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
	}

	if err := h.db.UpdateReplicationSource(src); err != nil {
		respondInternalError(w, err)
		return
	}

	h.reloadScheduler()
	respondJSON(w, http.StatusOK, src)
}

// Delete removes a replication source and its replicated jobs.
func (h *ReplicationHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	// Delete replicated jobs first.
	if err := h.db.DeleteReplicatedJobs(id); err != nil {
		log.Printf("Warning: failed to delete replicated jobs for source %d: %v", id, err) //nolint:gosec // id is int64 from URL param
	}

	if err := h.db.DeleteReplicationSource(id); err != nil {
		respondInternalError(w, err)
		return
	}

	h.reloadScheduler()
	w.WriteHeader(http.StatusNoContent)
}

// TestConnection tests connectivity to a replication target.
func (h *ReplicationHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	src, err := h.db.GetReplicationSource(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}

	var cfg struct {
		APIKey string `json:"api_key"`
	}
	if src.Config != "" && src.Config != "{}" {
		if err := json.Unmarshal([]byte(src.Config), &cfg); err != nil {
			respondError(w, http.StatusBadRequest, "invalid config JSON: "+err.Error())
			return
		}
	}
	var client *replication.Client
	var clientErr error
	if cfg.APIKey != "" {
		client, clientErr = replication.NewClientWithAPIKey(src.URL, cfg.APIKey)
	} else {
		client, clientErr = replication.NewClient(src.URL)
	}
	if clientErr != nil {
		respondError(w, http.StatusBadRequest, "invalid url: "+clientErr.Error())
		return
	}
	client.SetTimeout(testConnectionTimeout)
	if _, connErr := client.TestConnection(); connErr != nil {
		respondError(w, http.StatusBadGateway, connErr.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// TestURL validates a remote Vault URL and performs a live connectivity check
// by hitting the /api/v1/health endpoint on the remote server. Returns the
// normalized URL and remote version on success, or 502 if the remote is
// unreachable. Accepts JSON body: {"url": "http://...", "api_key": "..."}.
func (h *ReplicationHandler) TestURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
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

	// Build client with or without API key and perform live connectivity test.
	var client *replication.Client
	var clientErr error
	if req.APIKey != "" {
		client, clientErr = replication.NewClientWithAPIKey(normalizedURL, req.APIKey)
	} else {
		client, clientErr = replication.NewClient(normalizedURL)
	}
	if clientErr != nil {
		respondError(w, http.StatusBadRequest, "invalid url: "+clientErr.Error())
		return
	}
	client.SetTimeout(testConnectionTimeout)
	health, connErr := client.TestConnection()
	if connErr != nil {
		respondError(w, http.StatusBadGateway, connErr.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"url":     normalizedURL,
		"version": health.Version,
		"message": "Connected successfully",
	})
}

// SyncNow triggers an immediate sync for a replication source.
func (h *ReplicationHandler) SyncNow(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	syncer := h.getSyncer()
	if syncer == nil {
		respondError(w, http.StatusServiceUnavailable, "replication syncer not available")
		return
	}

	// Run sync in background, return immediately.
	go func() {
		if _, err := syncer.SyncSource(id, nil); err != nil {
			log.Printf("Manual sync failed for source %d: %v", id, err) //nolint:gosec // G706: id is a validated int64, err is from internal syncer
		}
	}()

	respondJSON(w, http.StatusAccepted, map[string]string{"status": "sync_started"})
}

// ListReplicatedJobs returns jobs replicated from a specific source.
func (h *ReplicationHandler) ListReplicatedJobs(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	jobs, err := h.db.ListReplicatedJobs(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, jobs)
}
