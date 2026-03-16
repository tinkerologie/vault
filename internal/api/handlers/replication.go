package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/replication"
)

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
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Redact API keys before sending to client.
	for i := range sources {
		sources[i].APIKey = "••••••••"
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
	if src.Name == "" || src.URL == "" {
		respondError(w, http.StatusBadRequest, "name and url are required")
		return
	}
	if src.StorageDestID == 0 {
		respondError(w, http.StatusBadRequest, "storage_dest_id is required")
		return
	}
	normalizedURL, err := replication.NormalizeBaseURL(src.URL)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid url: "+err.Error())
		return
	}
	src.URL = normalizedURL

	// Seal the API key before storing (empty key = no auth needed on remote).
	if src.APIKey != "" {
		sealed, err := crypto.Seal(h.serverKey, src.APIKey)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to seal api key")
			return
		}
		src.APIKey = sealed
	}

	id, err := h.db.CreateReplicationSource(src)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	src.ID = id
	src.APIKey = "••••••••"

	h.reloadScheduler()
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
	src.APIKey = "••••••••"
	respondJSON(w, http.StatusOK, src)
}

// Update modifies a replication source.
func (h *ReplicationHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)

	var src db.ReplicationSource
	if err := json.NewDecoder(r.Body).Decode(&src); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	src.ID = id

	normalizedURL, err := replication.NormalizeBaseURL(src.URL)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid url: "+err.Error())
		return
	}
	src.URL = normalizedURL

	// If API key is the redacted placeholder, keep the existing one.
	// Empty string means "no API key" (clear it). Any other value = new key.
	if src.APIKey == "••••••••" {
		existing, err := h.db.GetReplicationSource(id)
		if err != nil {
			respondError(w, http.StatusNotFound, "not found")
			return
		}
		src.APIKey = existing.APIKey
	} else if src.APIKey != "" {
		// Seal the new API key.
		sealed, err := crypto.Seal(h.serverKey, src.APIKey)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to seal api key")
			return
		}
		src.APIKey = sealed
	}
	// else: empty string = no API key (for LAN setups without auth)

	if err := h.db.UpdateReplicationSource(src); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	src.APIKey = "••••••••"

	h.reloadScheduler()
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
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.reloadScheduler()
	w.WriteHeader(http.StatusNoContent)
}

// TestConnection tests connectivity to a remote Vault source.
func (h *ReplicationHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	src, err := h.db.GetReplicationSource(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}

	var apiKey string
	if src.APIKey != "" {
		apiKey, err = crypto.Unseal(h.serverKey, src.APIKey)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to unseal api key")
			return
		}
	}

	client, err := replication.NewClient(src.URL, apiKey)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid url: "+err.Error())
		return
	}
	if _, err := client.TestConnection(); err != nil {
		respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// TestURL validates and normalizes a remote Vault URL before it is saved.
// Accepts JSON body: {"url": "http://...", "api_key": "optional"}.
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
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, jobs)
}
