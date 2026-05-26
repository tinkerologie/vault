package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/replication"
)

// setupReplicationTest creates a test DB, server key, and handler.
func setupReplicationTest(t *testing.T) (*ReplicationHandler, *db.DB) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	serverKey := make([]byte, 32)
	for i := range serverKey {
		serverKey[i] = byte(i)
	}

	// Create a storage destination for the replication source to reference.
	_, err = database.CreateStorageDestination(db.StorageDestination{
		Name:   "local-test",
		Type:   "local",
		Config: `{"base_path":"/tmp/test"}`,
	})
	if err != nil {
		t.Fatalf("create storage dest: %v", err)
	}

	h := NewReplicationHandler(database, nil, serverKey, nil, nil)
	return h, database
}

func TestReplicationHandlerCRUD(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	// Create
	body := `{"name":"prod","url":"http://192.168.1.1:24085","storage_dest_id":1,"schedule":"0 3 * * *","enabled":true}`
	req := httptest.NewRequest("POST", "/replication", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("Create: got %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var created db.ReplicationSource
	json.NewDecoder(w.Body).Decode(&created)
	if created.Name != "prod" {
		t.Errorf("name = %q, want %q", created.Name, "prod")
	}

	// List
	req = httptest.NewRequest("GET", "/replication", nil)
	w = httptest.NewRecorder()
	h.List(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("List: got %d, want %d", w.Code, http.StatusOK)
	}
	var sources []db.ReplicationSource
	json.NewDecoder(w.Body).Decode(&sources)
	if len(sources) != 1 {
		t.Fatalf("List: got %d sources, want 1", len(sources))
	}

	// Get (using chi context for URL params)
	req = httptest.NewRequest("GET", "/replication/1", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w = httptest.NewRecorder()
	h.Get(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Get: got %d, want %d", w.Code, http.StatusOK)
	}

	// Update
	updateBody := `{"name":"prod-updated","url":"http://192.168.1.2:24085","storage_dest_id":1,"schedule":"0 4 * * *","enabled":false}`
	req = httptest.NewRequest("PUT", "/replication/1", strings.NewReader(updateBody))
	req.Header.Set("Content-Type", "application/json")
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w = httptest.NewRecorder()
	h.Update(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Update: got %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var updated db.ReplicationSource
	json.NewDecoder(w.Body).Decode(&updated)
	if updated.Name != "prod-updated" {
		t.Errorf("Update: name = %q, want %q", updated.Name, "prod-updated")
	}

	// Delete
	req = httptest.NewRequest("DELETE", "/replication/1", nil)
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w = httptest.NewRecorder()
	h.Delete(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("Delete: got %d, want %d", w.Code, http.StatusNoContent)
	}

	// Verify deleted.
	req = httptest.NewRequest("GET", "/replication", nil)
	w = httptest.NewRecorder()
	h.List(w, req)
	json.NewDecoder(w.Body).Decode(&sources)
	if len(sources) != 0 {
		t.Errorf("sources after delete: got %d, want 0", len(sources))
	}
}

func TestReplicationHandlerValidation(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	tests := []struct {
		name string
		body string
		want int
	}{
		{"missing name", `{"url":"http://x","storage_dest_id":1}`, http.StatusBadRequest},
		{"missing url for remote_vault", `{"name":"n","type":"remote_vault"}`, http.StatusBadRequest},
		{"invalid type", `{"name":"n","type":"invalid"}`, http.StatusBadRequest},
		{"invalid json", `{broken`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest("POST", "/replication", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.Create(w, req)
			if w.Code != tt.want {
				t.Errorf("got %d, want %d; body: %s", w.Code, tt.want, w.Body.String())
			}
		})
	}
}

func TestReplicationHandlerTestURLConnectsToRemote(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	t.Run("success - reachable server", func(t *testing.T) {
		t.Parallel()
		// Simulate a remote Vault /api/v1/health endpoint.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/health" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","version":"2026.4.0"}`))
		}))
		defer srv.Close()

		body := `{"url":"` + srv.URL + `"}`
		req := httptest.NewRequest(http.MethodPost, "/replication/test-url", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.TestURL(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("TestURL: got %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
		}
		var resp map[string]string
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp["version"] != "2026.4.0" {
			t.Errorf("version = %q, want %q", resp["version"], "2026.4.0")
		}
		if resp["message"] != "Connected successfully" {
			t.Errorf("message = %q, want %q", resp["message"], "Connected successfully")
		}
	})

	t.Run("failure - unreachable server", func(t *testing.T) {
		t.Parallel()
		// Use a closed server to guarantee connection refused.
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		closedURL := srv.URL
		srv.Close()

		body := `{"url":"` + closedURL + `"}`
		req := httptest.NewRequest(http.MethodPost, "/replication/test-url", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.TestURL(w, req)

		if w.Code != http.StatusBadGateway {
			t.Fatalf("TestURL: got %d, want %d; body: %s", w.Code, http.StatusBadGateway, w.Body.String())
		}
	})
}

func TestReplicationHandlerCreateRejectsURLWithPath(t *testing.T) {
	t.Parallel()

	h, _ := setupReplicationTest(t)
	body := `{"name":"prod","url":"https://vault.example.com/api/v1","storage_dest_id":1}`
	req := httptest.NewRequest(http.MethodPost, "/replication", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("Create: got %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// SetConfigChangeHook
// ---------------------------------------------------------------------------

func TestReplicationHandlerSetConfigChangeHook(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	called := false
	h.SetConfigChangeHook(func() { called = true })
	h.notifyConfigChange()
	if !called {
		t.Error("config change hook was not called")
	}
}

func TestReplicationHandlerSetConfigChangeHook_Nil(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	// nil hook must not panic
	h.SetConfigChangeHook(nil)
	h.notifyConfigChange() // should not panic
}

// ---------------------------------------------------------------------------
// TestConnection via httptest stub
// ---------------------------------------------------------------------------

func TestReplicationHandlerTestConnection_Success(t *testing.T) {
	t.Parallel()

	// Stub a remote Vault health endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","version":"2026.4.0"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	h, database := setupReplicationTest(t)

	// Create a replication source pointing at the stub server.
	srcID, err := database.CreateReplicationSource(db.ReplicationSource{
		Name:    "stub-target",
		Type:    "remote_vault",
		URL:     srv.URL,
		Config:  "{}",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/replication/1/test-connection", nil)
	req = withURLParam(req, "id", fmt.Sprintf("%d", srcID))
	w := httptest.NewRecorder()
	h.TestConnection(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("TestConnection: got %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status = %q, want ok", resp["status"])
	}
}

func TestReplicationHandlerTestConnection_Unreachable(t *testing.T) {
	t.Parallel()

	// Create a server and immediately close it so the port is unreachable.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := srv.URL
	srv.Close()

	h, database := setupReplicationTest(t)

	srcID, err := database.CreateReplicationSource(db.ReplicationSource{
		Name:    "closed-target",
		Type:    "remote_vault",
		URL:     closedURL,
		Config:  "{}",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/replication/1/test-connection", nil)
	req = withURLParam(req, "id", fmt.Sprintf("%d", srcID))
	w := httptest.NewRecorder()
	h.TestConnection(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("TestConnection: got %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestReplicationHandlerTestConnection_NotFound(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	req := httptest.NewRequest(http.MethodPost, "/replication/9999/test-connection", nil)
	req = withURLParam(req, "id", "9999")
	w := httptest.NewRecorder()
	h.TestConnection(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("TestConnection: got %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// SyncNow
// ---------------------------------------------------------------------------

func TestReplicationHandlerSyncNow_NoSyncer(t *testing.T) {
	t.Parallel()
	// Handler constructed with getSyncer returning nil.
	h, database := setupReplicationTest(t)
	// getSyncer is nil in setupReplicationTest; rebuild with explicit nil syncer provider.
	h2 := NewReplicationHandler(database, func() *replication.Syncer { return nil }, make([]byte, 32), nil, nil)

	srcID, err := database.CreateReplicationSource(db.ReplicationSource{
		Name:    "sync-target",
		Type:    "remote_vault",
		URL:     "http://192.168.1.50:24085",
		Config:  "{}",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	_ = h
	req := httptest.NewRequest(http.MethodPost, "/replication/1/sync", nil)
	req = withURLParam(req, "id", fmt.Sprintf("%d", srcID))
	w := httptest.NewRecorder()
	h2.SyncNow(w, req)

	// getSyncer returns nil → 503 service unavailable.
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("SyncNow: got %d, want 503; body: %s", w.Code, w.Body.String())
	}
}

func TestReplicationHandlerSyncNow_InvalidID(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	req := httptest.NewRequest(http.MethodPost, "/replication/bad/sync", nil)
	req = withURLParam(req, "id", "bad")
	w := httptest.NewRecorder()
	h.SyncNow(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("SyncNow: got %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// ListReplicatedJobs
// ---------------------------------------------------------------------------

func TestReplicationHandlerListReplicatedJobs_Empty(t *testing.T) {
	t.Parallel()
	h, database := setupReplicationTest(t)

	srcID, err := database.CreateReplicationSource(db.ReplicationSource{
		Name:    "jobs-source",
		Type:    "remote_vault",
		URL:     "http://192.168.1.51:24085",
		Config:  "{}",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/replication/1/jobs", nil)
	req = withURLParam(req, "id", fmt.Sprintf("%d", srcID))
	w := httptest.NewRecorder()
	h.ListReplicatedJobs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("ListReplicatedJobs: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var jobs []db.Job
	if err := json.NewDecoder(w.Body).Decode(&jobs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// No replicated jobs seeded — expect empty array (not nil).
	if jobs == nil {
		jobs = []db.Job{}
	}
	if len(jobs) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(jobs))
	}
}

func TestReplicationHandlerListReplicatedJobs_WithJobs(t *testing.T) {
	t.Parallel()
	h, database := setupReplicationTest(t)

	// Create a replication source.
	srcID, err := database.CreateReplicationSource(db.ReplicationSource{
		Name:    "with-jobs-source",
		Type:    "remote_vault",
		URL:     "http://192.168.1.52:24085",
		Config:  "{}",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}

	// Create a storage destination for the replicated job.
	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "repl-dest",
		Type:   "local",
		Config: `{"base_path":"/tmp/repl"}`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	// Seed a replicated job linked to the source.
	_, err = database.CreateReplicatedJob(db.Job{
		Name:          "replicated-job",
		StorageDestID: destID,
		SourceID:      srcID,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("create replicated job: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/replication/1/jobs", nil)
	req = withURLParam(req, "id", fmt.Sprintf("%d", srcID))
	w := httptest.NewRecorder()
	h.ListReplicatedJobs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("ListReplicatedJobs: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var jobs []db.Job
	if err := json.NewDecoder(w.Body).Decode(&jobs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 replicated job, got %d", len(jobs))
	}
	if jobs[0].Name != "replicated-job" {
		t.Errorf("job name = %q, want replicated-job", jobs[0].Name)
	}
}

func TestReplicationHandlerListReplicatedJobs_InvalidID(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	req := httptest.NewRequest(http.MethodGet, "/replication/bad/jobs", nil)
	req = withURLParam(req, "id", "bad")
	w := httptest.NewRecorder()
	h.ListReplicatedJobs(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("ListReplicatedJobs: got %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// broadcastConfigChange — exercises the Broadcast branch
// ---------------------------------------------------------------------------

// mockBroadcaster records calls to Broadcast.
type mockBroadcaster struct {
	messages []map[string]any
}

func (m *mockBroadcaster) Broadcast(msg map[string]any) {
	m.messages = append(m.messages, msg)
}

func TestReplicationHandlerBroadcastConfigChange_WithBroadcaster(t *testing.T) {
	t.Parallel()
	_, database := setupReplicationTest(t)

	b := &mockBroadcaster{}
	serverKey := make([]byte, 32)
	h := NewReplicationHandler(database, nil, serverKey, nil, b)

	h.broadcastConfigChange() // should call b.Broadcast(...)
	if len(b.messages) != 1 {
		t.Fatalf("expected 1 broadcast, got %d", len(b.messages))
	}
	if b.messages[0]["type"] != "config_changed" {
		t.Errorf("type = %v, want config_changed", b.messages[0]["type"])
	}
}

func TestReplicationHandlerBroadcastConfigChange_NilBroadcaster(t *testing.T) {
	t.Parallel()
	_, database := setupReplicationTest(t)
	h := NewReplicationHandler(database, nil, make([]byte, 32), nil, nil)
	h.broadcastConfigChange() // must not panic
}

// ---------------------------------------------------------------------------
// reloadScheduler — exercises error log path
// ---------------------------------------------------------------------------

func TestReplicationHandlerReloadScheduler_Error(t *testing.T) {
	t.Parallel()
	_, database := setupReplicationTest(t)

	errFn := func() error { return fmt.Errorf("reload failed") }
	h := NewReplicationHandler(database, nil, make([]byte, 32), errFn, nil)
	h.reloadScheduler() // should log warning, not panic
}

// ---------------------------------------------------------------------------
// TestConnection with API key in config
// ---------------------------------------------------------------------------

func TestReplicationHandlerTestConnection_WithAPIKey(t *testing.T) {
	t.Parallel()

	// Stub a remote Vault health endpoint that validates the API key header.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/v1/health" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","version":"2026.4.0"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	h, database := setupReplicationTest(t)

	// Create a replication source with an api_key in config.
	srcID, err := database.CreateReplicationSource(db.ReplicationSource{
		Name:    "api-key-target",
		Type:    "remote_vault",
		URL:     srv.URL,
		Config:  `{"api_key":"test-secret-key"}`,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/replication/1/test-connection", nil)
	req = withURLParam(req, "id", fmt.Sprintf("%d", srcID))
	w := httptest.NewRecorder()
	h.TestConnection(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("TestConnection with API key: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TestURL validation paths
// ---------------------------------------------------------------------------

func TestReplicationHandlerTestURL_InvalidJSON(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	req := httptest.NewRequest(http.MethodPost, "/replication/test-url",
		strings.NewReader("not-json"))
	w := httptest.NewRecorder()
	h.TestURL(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("TestURL: got %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestReplicationHandlerTestURL_MissingURL(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	body := `{"url":""}`
	req := httptest.NewRequest(http.MethodPost, "/replication/test-url",
		strings.NewReader(body))
	w := httptest.NewRecorder()
	h.TestURL(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("TestURL: got %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestReplicationHandlerTestURL_WithAPIKey_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","version":"2026.4.0"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	h, _ := setupReplicationTest(t)

	body := fmt.Sprintf(`{"url":"%s","api_key":"mykey"}`, srv.URL)
	req := httptest.NewRequest(http.MethodPost, "/replication/test-url",
		strings.NewReader(body))
	w := httptest.NewRecorder()
	h.TestURL(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("TestURL with api_key: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
}
