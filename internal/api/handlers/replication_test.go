package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ruaandeysel/vault/internal/crypto"
	"github.com/ruaandeysel/vault/internal/db"
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

	h := NewReplicationHandler(database, nil, serverKey, nil)
	return h, database
}

func TestReplicationHandlerCRUD(t *testing.T) {
	t.Parallel()
	h, database := setupReplicationTest(t)
	serverKey := h.serverKey

	// Seal an API key for the Create test.
	sealed, _ := crypto.Seal(serverKey, "test-api-key")
	_ = sealed

	// Create
	body := `{"name":"prod","url":"http://192.168.1.1:24085","api_key":"my-secret-key","storage_dest_id":1,"schedule":"0 3 * * *","enabled":true}`
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
	if created.APIKey != "••••••••" {
		t.Errorf("api_key should be redacted, got %q", created.APIKey)
	}

	// Verify the API key is actually sealed in the DB.
	src, _ := database.GetReplicationSource(created.ID)
	if _, err := crypto.Unseal(serverKey, src.APIKey); err != nil {
		t.Errorf("api key not properly sealed: %v", err)
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
	if sources[0].APIKey != "••••••••" {
		t.Errorf("List: api key not redacted")
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

	// Update (without changing API key)
	updateBody := `{"name":"prod-updated","url":"http://192.168.1.2:24085","api_key":"","storage_dest_id":1,"schedule":"0 4 * * *","enabled":false}`
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
		{"missing name", `{"url":"http://x","api_key":"k","storage_dest_id":1}`, http.StatusBadRequest},
		{"missing url", `{"name":"n","api_key":"k","storage_dest_id":1}`, http.StatusBadRequest},
		{"missing storage_dest_id", `{"name":"n","url":"http://x","api_key":"k"}`, http.StatusBadRequest},
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

func TestReplicationHandlerTestURLValidatesFormatOnly(t *testing.T) {
	t.Parallel()

	h, _ := setupReplicationTest(t)
	body := `{"url":"https://vault.example.com:24085"}`
	req := httptest.NewRequest(http.MethodPost, "/replication/test-url", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.TestURL(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("TestURL: got %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["url"] != "https://vault.example.com:24085" {
		t.Fatalf("normalized url = %q", resp["url"])
	}
}

func TestReplicationHandlerCreateRejectsURLWithPath(t *testing.T) {
	t.Parallel()

	h, _ := setupReplicationTest(t)
	body := `{"name":"prod","url":"https://vault.example.com/api/v1","api_key":"k","storage_dest_id":1}`
	req := httptest.NewRequest(http.MethodPost, "/replication", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("Create: got %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}
