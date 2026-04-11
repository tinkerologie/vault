package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ruaan-deysel/vault/internal/db"
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
