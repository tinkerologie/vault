package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

func replicaRequest(srv *Server, method, path, body string) int {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// The shape of a request from the Unraid plugin proxy, which the API key
	// boundary (#578) lets through, so these tests reach the replica guards.
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "127.0.0.1:24085"
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	return w.Code
}

// Data the replica holds cannot be changed through maintenance routes, while
// the routes that set up and run replication stay reachable.
func TestReplicaGuards(t *testing.T) {
	d := testDB(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "j", BackupTypeChain: "full", StorageDestID: destID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: "gone", Settings: `{"path":"/nonexistent/vault-replica-test"}`}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(d, ServerConfig{Addr: ":0", ReadOnly: true})

	for _, tt := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/storage/1/delete-orphans"},
		{http.MethodPost, "/api/v1/storage/1/gc"},
		{http.MethodPost, "/api/v1/storage/1/breaker/close"},
		{http.MethodPost, "/api/v1/storage/1/import"},
		{http.MethodDelete, "/api/v1/storage/1?deleteFiles=true"},
		{http.MethodDelete, "/api/v1/storage/1?force=true"},
		{http.MethodDelete, "/api/v1/history"},
		{http.MethodPost, "/api/v1/runs/ack"},
		{http.MethodDelete, "/api/v1/activity"},
		{http.MethodPost, "/api/v1/anomalies/ack-bulk"},
		{http.MethodPost, "/api/v1/anomalies/1/ack"},
		{http.MethodDelete, "/api/v1/replication/1"},
	} {
		if got := replicaRequest(srv, tt.method, tt.path, `{}`); got != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", tt.method, tt.path, got)
		}
	}
	for _, tt := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/activity", ""},
		{http.MethodGet, "/api/v1/history", ""},
		{http.MethodGet, "/api/v1/anomalies", ""},
		{http.MethodGet, "/api/v1/replication", ""},
		{http.MethodPost, "/api/v1/replication", `{}`},
		{http.MethodPost, "/api/v1/storage/test", `{}`},
		{http.MethodPost, "/api/v1/storage/1/scan", `{}`},
		{http.MethodDelete, "/api/v1/storage/999", ""},
	} {
		if got := replicaRequest(srv, tt.method, tt.path, tt.body); got == http.StatusForbidden || got == http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want it reachable on a replica", tt.method, tt.path, got)
		}
	}

	// A stale-items read reports the scan but records nothing.
	if got := replicaRequest(srv, http.MethodGet, "/api/v1/jobs/1/stale-items", ""); got != http.StatusOK {
		t.Fatalf("stale-items = %d", got)
	}
	items, err := d.GetJobItems(jobID)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %v, %v", items, err)
	}
	if items[0].MissingSince != nil {
		t.Errorf("replica persisted missing_since = %s", *items[0].MissingSince)
	}
}

// The guards do nothing on a daemon, which keeps persisting stale marks.
func TestDaemonUnaffectedByReplicaGuards(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{Addr: ":0"})
	for _, tt := range []struct{ method, path string }{
		{http.MethodDelete, "/api/v1/activity"},
		{http.MethodDelete, "/api/v1/history"},
		{http.MethodDelete, "/api/v1/storage/999?deleteFiles=true"},
		{http.MethodDelete, "/api/v1/replication/999"},
	} {
		if got := replicaRequest(srv, tt.method, tt.path, ""); got == http.StatusForbidden || got == http.StatusUnauthorized {
			t.Errorf("%s %s = %d on a daemon, want it to reach the handler", tt.method, tt.path, got)
		}
	}
}
