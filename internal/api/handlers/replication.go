package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/tinkerologie/vault/internal/credentials"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/replication"
)

// ServerWriteTimeout is the http.Server WriteTimeout used by the API server.
// It lives here (rather than in package api) so outbound-probe timeouts can be
// derived from it without an import cycle — package api imports handlers.
const ServerWriteTimeout = 15 * time.Second

// RestorePointContentsWriteTimeout is the extended per-request deadline for
// GET /jobs/{id}/restore-points/{rpid}/contents. Enumerating a large restore
// point on remote storage (B2/S3) can take well over ServerWriteTimeout
// (issue #449), so only that route gets a longer budget.
//
// The layers are ordered so each inner one expires first and the outer one
// can still relay its result: daemon 120 s < Unraid PHP proxy 125 s
// (VAULT_HTTP_TIMEOUT_CONTENTS in plugin/pages/include/api.php) < browser
// 130 s (RESTORE_POINT_CONTENTS_TIMEOUT_MS in web/src/lib/api.js). Update all
// three together.
const RestorePointContentsWriteTimeout = 120 * time.Second

// DedupRewrapWriteTimeout is the write deadline for the dedup key rewrap
// after a recovery (#451): each destination costs a scrypt derivation plus
// storage round trips, which can outlast ServerWriteTimeout.
const DedupRewrapWriteTimeout = 120 * time.Second

// RestorePointContentsHandlerTimeout bounds the contents handler itself, one
// probeTimeoutHeadroom ahead of RestorePointContentsWriteTimeout, so an
// over-long listing is answered with a clean timeout error instead of a
// dropped connection that the proxy would report as "daemon unavailable".
const RestorePointContentsHandlerTimeout = RestorePointContentsWriteTimeout - probeTimeoutHeadroom

// RestorePointContentsTimeoutBody is the JSON error body served when
// RestorePointContentsHandlerTimeout expires.
const RestorePointContentsTimeoutBody = `{"error":"listing the restore point contents timed out; try again"}`

// probeTimeoutHeadroom is how far every outbound connectivity probe must
// finish ahead of ServerWriteTimeout, leaving time to serialise and flush the
// error response.
const probeTimeoutHeadroom = 5 * time.Second

// probeTimeout returns the deadline every outbound connectivity/capacity probe
// must respect so its error response is still writable. Shared by the
// replication and storage handlers.
func probeTimeout() time.Duration {
	return ServerWriteTimeout - probeTimeoutHeadroom
}

// testConnectionTimeout is the HTTP client timeout used for connectivity
// checks so that Test Connection / Test URL fail fast instead of blocking
// for the default 10-minute transfer timeout.
//
// It MUST stay comfortably below ServerWriteTimeout. When the two are equal,
// probing a blackholed host (packets dropped, no RST) makes the probe deadline
// and the response write deadline expire at the same instant, so the 502 this
// handler writes is never flushed and the caller sees an empty reply instead
// of an error (QA: replication test-url returned HTTP 000 at exactly 15.002s).
const testConnectionTimeout = ServerWriteTimeout - probeTimeoutHeadroom

// SyncerProvider returns the replication syncer (resolved lazily).
type SyncerProvider = func() *replication.Syncer

// ReplicationHandler handles CRUD and sync operations for replication sources.
type ReplicationHandler struct {
	db             *db.DB
	getSyncer      SyncerProvider
	serverKey      []byte
	schedReload    ScheduleReloader
	broadcaster    Broadcaster
	onConfigChange ConfigChangeHook
}

// SetConfigChangeHook registers a function called after replication source
// mutations (typically used by the daemon to flush the DB snapshot to USB
// flash).
func (h *ReplicationHandler) SetConfigChangeHook(fn ConfigChangeHook) {
	h.onConfigChange = fn
}

// notifyConfigChange invokes the persistence hook if one is registered.
func (h *ReplicationHandler) notifyConfigChange() {
	if h.onConfigChange != nil {
		h.onConfigChange()
	}
}

// Broadcaster is the minimal interface ReplicationHandler needs to publish
// `config_changed` WebSocket events. *runner.Runner satisfies it via its
// public Broadcast method.
type Broadcaster interface {
	Broadcast(map[string]any)
}

// NewReplicationHandler creates a new ReplicationHandler.
func NewReplicationHandler(database *db.DB, getSyncer SyncerProvider, serverKey []byte, reload ScheduleReloader, b Broadcaster) *ReplicationHandler {
	return &ReplicationHandler{
		db:          database,
		getSyncer:   getSyncer,
		serverKey:   serverKey,
		schedReload: reload,
		broadcaster: b,
	}
}

// broadcastConfigChange sends a `config_changed` WebSocket event so that
// the Dashboard's 3-2-1 compliance widget and other derived UI state
// re-fetch without a manual page reload.
func (h *ReplicationHandler) broadcastConfigChange() {
	if h.broadcaster == nil {
		return
	}
	h.broadcaster.Broadcast(map[string]any{
		"type":   "config_changed",
		"entity": "replication",
	})
}

// reloadScheduler triggers a scheduler reload, logging any errors.
func (h *ReplicationHandler) reloadScheduler() {
	if h.schedReload != nil {
		if err := h.schedReload(); err != nil {
			log.Printf("Warning: scheduler reload failed: %v", err)
		}
	}
}

// replicationResponse is a source as the API shows it: the API key is masked,
// plus a warning when the source is reached over plain HTTP.
type replicationResponse struct {
	db.ReplicationSource
	CredentialsLocked bool   `json:"credentials_locked"`
	Warning           string `json:"warning,omitempty"`
}

func newReplicationResponse(src db.ReplicationSource) replicationResponse {
	out := replicationResponse{
		ReplicationSource: src,
		CredentialsLocked: credentials.IsLocked(src.Config),
		Warning:           replication.PlaintextWarning(src.URL),
	}
	out.Config = credentials.Mask(src.Config)
	return out
}

// mergeReplicationKey resolves the API key on an update. An omitted, blank or
// masked key keeps the stored one and a new value replaces it. The stored key
// only ever goes to the URL it was entered for: otherwise anyone who can edit a
// source could point it at their own server and collect the key.
func mergeReplicationKey(incoming db.ReplicationSource, stored db.ReplicationSource) (string, error) {
	var in, ex map[string]any
	if incoming.Config != "" {
		if err := json.Unmarshal([]byte(incoming.Config), &in); err != nil {
			return "", errors.New("invalid config JSON")
		}
	}
	if in == nil {
		in = map[string]any{}
	}
	if stored.Config != "" {
		// A stored config that does not parse has no key worth keeping.
		_ = json.Unmarshal([]byte(stored.Config), &ex)
	}
	if credentials.IsLocked(incoming.Config) {
		return "", errors.New("config must not contain sealed values")
	}
	// The sync decodes field names without regard to case, so treat them alike.
	for k, v := range in {
		if k != "api_key" && strings.EqualFold(k, "api_key") {
			in["api_key"] = v
			delete(in, k)
		}
	}
	key, _ := in["api_key"].(string)
	storedKey := ""
	for k, v := range ex {
		if strings.EqualFold(k, "api_key") {
			storedKey, _ = v.(string)
		}
	}
	if key != "" && key != credentials.RedactionMarker {
		out, err := json.Marshal(in)
		return string(out), err
	}
	if storedKey == "" {
		delete(in, "api_key")
		out, err := json.Marshal(in)
		return string(out), err
	}
	if incoming.URL != "" && incoming.URL != stored.URL {
		return "", errors.New("re-enter the API key when changing the URL")
	}
	in["api_key"] = storedKey
	out, err := json.Marshal(in)
	return string(out), err
}

// List returns all replication sources.
func (h *ReplicationHandler) List(w http.ResponseWriter, r *http.Request) {
	sources, err := h.db.ListReplicationSources()
	if err != nil {
		respondInternalError(w, err)
		return
	}
	out := make([]replicationResponse, len(sources))
	for i, src := range sources {
		out[i] = newReplicationResponse(src)
	}
	respondJSON(w, http.StatusOK, out)
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

	if credentials.IsLocked(src.Config) {
		respondError(w, http.StatusBadRequest, "config must not contain sealed values")
		return
	}

	id, err := h.db.CreateReplicationSource(src)
	if err != nil {
		respondWriteError(w, err, "replication source")
		return
	}
	src.ID = id

	h.reloadScheduler()
	respondJSON(w, http.StatusCreated, newReplicationResponse(src))
	h.broadcastConfigChange()
	h.notifyConfigChange()
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
	respondJSON(w, http.StatusOK, newReplicationResponse(src))
}

// Update modifies a replication source.
func (h *ReplicationHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	// Pointers tell "omitted" from "empty", so a partial body leaves the other
	// fields alone instead of blanking them.
	var patch struct {
		Name          *string `json:"name"`
		Type          *string `json:"type"`
		URL           *string `json:"url"`
		Config        *string `json:"config"`
		StorageDestID *int64  `json:"storage_dest_id"`
		Schedule      *string `json:"schedule"`
		Enabled       *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if patch.URL != nil && *patch.URL != "" {
		if _, err := replication.NormalizeBaseURL(*patch.URL); err != nil {
			respondError(w, http.StatusBadRequest, "invalid url: "+err.Error())
			return
		}
	}

	stored, err := h.db.GetReplicationSource(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	src := stored
	src.Config = ""
	if patch.Name != nil {
		src.Name = *patch.Name
	}
	if patch.Type != nil && *patch.Type != "" {
		src.Type = *patch.Type
	}
	if patch.URL != nil {
		src.URL = *patch.URL
	}
	if patch.Config != nil {
		src.Config = *patch.Config
	}
	if patch.StorageDestID != nil {
		src.StorageDestID = *patch.StorageDestID
	}
	if patch.Schedule != nil {
		src.Schedule = *patch.Schedule
	}
	if patch.Enabled != nil {
		src.Enabled = *patch.Enabled
	}

	if src.Type == "remote_vault" {
		normalizedURL, err := replication.NormalizeBaseURL(src.URL)
		if err != nil {
			respondError(w, http.StatusBadRequest, "invalid url: "+err.Error())
			return
		}
		src.URL = normalizedURL
	}

	src.Config, err = mergeReplicationKey(src, stored)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	update := func() error { return h.db.UpdateReplicationSource(src) }
	var syncer *replication.Syncer
	if h.getSyncer != nil {
		syncer = h.getSyncer()
	}
	if syncer != nil && src.StorageDestID != stored.StorageDestID {
		// Checked under the syncer's lock, so no sync of this source can be
		// writing to the old destination when its jobs move.
		apply := update
		update = func() error { return syncer.ChangeSourceDestination(id, apply) }
	}
	if err := update(); err != nil {
		if errors.Is(err, replication.ErrSyncInProgress) {
			respondError(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, db.ErrReplicationDestinationInUse) {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondWriteError(w, err, "replication source")
		return
	}

	h.reloadScheduler()
	respondJSON(w, http.StatusOK, newReplicationResponse(src))
	h.broadcastConfigChange()
	h.notifyConfigChange()
}

// Delete removes a replication source and its replicated jobs.
func (h *ReplicationHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	// Delete replicated jobs first.
	if err := h.db.DeleteReplicatedJobs(id); err != nil {
		log.Printf("Warning: failed to delete replicated jobs for source %d: %v", id, err) // #nosec G706 //nolint:gosec // id is int64 from URL param
	}

	if err := h.db.DeleteReplicationSource(id); err != nil {
		respondInternalError(w, err)
		return
	}

	h.reloadScheduler()
	w.WriteHeader(http.StatusNoContent)
	h.broadcastConfigChange()
	h.notifyConfigChange()
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

	if credentials.IsLocked(src.Config) {
		respondError(w, http.StatusConflict, credentials.ErrLocked.Error())
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
	respondJSON(w, http.StatusOK, map[string]string{"status": "ok", "warning": replication.PlaintextWarning(src.URL)})
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
	// Test URL never looks up a stored key, so the marker has no value to stand for.
	if req.APIKey == credentials.RedactionMarker {
		respondError(w, http.StatusBadRequest, "enter the API key to test a new URL")
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
		"warning": replication.PlaintextWarning(normalizedURL),
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
			log.Printf("Manual sync failed for source %d: %v", id, err) // #nosec G706 //nolint:gosec // G706: id is a validated int64, err is from internal syncer
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
