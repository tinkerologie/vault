package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/replication"
)

// TestServerForwardsConfigHookToSyncer600 pins that the daemon's flush hook
// reaches the replication syncer whichever is wired first, so a destination a
// sync binds is flushed to the hybrid snapshot.
func TestServerForwardsConfigHookToSyncer600(t *testing.T) {
	t.Setenv(replication.AllowLoopbackEnv, "1")
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			_ = json.NewEncoder(w).Encode(replication.HealthResponse{Status: "ok"})
		case "/api/v1/jobs":
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(peer.Close)

	for _, hookFirst := range []bool{true, false} {
		d := testDB(t)
		if _, err := d.CreateStorageDestination(db.StorageDestination{Name: "l", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`}); err != nil {
			t.Fatal(err)
		}
		srcID, err := d.CreateReplicationSource(db.ReplicationSource{Name: "P", URL: peer.URL})
		if err != nil {
			t.Fatal(err)
		}
		srv := NewServer(d, ServerConfig{Addr: ":0"})
		syncer := replication.NewSyncer(d, nil)
		flushes := 0
		hook := func() { flushes++ }
		if hookFirst {
			srv.SetConfigChangeHook(hook)
			srv.SetReplicationSyncer(syncer)
		} else {
			srv.SetReplicationSyncer(syncer)
			srv.SetConfigChangeHook(hook)
		}
		if _, err := syncer.SyncSource(srcID, nil); err != nil {
			t.Fatalf("hookFirst=%v: SyncSource: %v", hookFirst, err)
		}
		if flushes != 1 {
			t.Errorf("hookFirst=%v: flushes = %d, want 1", hookFirst, flushes)
		}
	}
}
