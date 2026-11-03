package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/replication"
)

// remoteVault serves the real router as seen from another machine, so nothing
// gets the local exemption.
func remoteVault(t *testing.T, database *db.DB) *httptest.Server {
	t.Helper()
	srv := NewServer(database, ServerConfig{Addr: ":0", Version: "1.2.3"})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr, r.Host = remotePeer, lanHost
		srv.router.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// The real replication client against the real boundary, both ways of holding
// the key: in memory, and sealed in the database as a saved source keeps it.
func TestReplicationClientAgainstBoundary(t *testing.T) {
	peerDB := testDB(t)
	key := setTestAPIKey(t, peerDB)
	peer := remoteVault(t, peerDB)

	t.Run("direct client", func(t *testing.T) {
		good, err := replication.NewClientWithAPIKey(peer.URL, key)
		if err != nil {
			t.Fatal(err)
		}
		health, err := good.TestConnection()
		if err != nil || health.Version != "1.2.3" {
			t.Fatalf("TestConnection = %+v, %v", health, err)
		}
		if _, err := good.ListJobs(); err != nil {
			t.Errorf("ListJobs: %v", err)
		}
		bad, err := replication.NewClientWithAPIKey(peer.URL, "wrong")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bad.TestConnection(); err == nil || !strings.Contains(err.Error(), "401") {
			t.Errorf("wrong key: %v, want 401", err)
		}
		anon, err := replication.NewClient(peer.URL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := anon.TestConnection(); err == nil || !strings.Contains(err.Error(), "401") {
			t.Errorf("no key: %v, want 401", err)
		}
	})

	t.Run("sealed source", func(t *testing.T) {
		sync := func(apiKey string) error {
			local := testDB(t)
			local.SetCredentialKey(make([]byte, crypto.ServerKeySize))
			id, err := local.CreateReplicationSource(db.ReplicationSource{
				Name: "peer", Type: "remote_vault", URL: peer.URL, Config: `{"api_key":"` + apiKey + `"}`,
			})
			if err != nil {
				t.Fatal(err)
			}
			var raw string
			if err := local.QueryRow("SELECT config FROM replication_sources WHERE id = ?", id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, apiKey) {
				t.Fatalf("source %s stored its key in the clear", strconv.FormatInt(id, 10))
			}
			syncer := replication.NewSyncer(local, nil)
			_, err = syncer.SyncSource(id, nil)
			return err
		}
		// The peer has no jobs and this side no storage: any failure past the
		// connection test is fine here, a refusal is not.
		if err := sync(key); err != nil && strings.Contains(err.Error(), "401") {
			t.Errorf("sealed key was refused: %v", err)
		}
		if err := sync("wrong-key-value"); err == nil || !strings.Contains(err.Error(), "401") {
			t.Errorf("wrong sealed key: %v, want 401", err)
		}
	})
}
