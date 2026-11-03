package replication

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/bounded"
	"github.com/tinkerologie/vault/internal/db"
)

// A peer sending JSON null used to panic on a goroutine and kill the daemon.
func TestRewriteMetadata_NullIsNotFatal(t *testing.T) {
	t.Parallel()
	s := &Syncer{}
	for _, in := range []string{"null", " null\n"} {
		if got := s.rewriteMetadata(in, "peer"); got != `{"replicated_from":"peer"}` {
			t.Errorf("%q -> %s", in, got)
		}
	}
}

func TestSyncSource_PanicBecomesFailedSync(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	dest, err := d.CreateStorageDestination(db.StorageDestination{Name: "l", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()
	id, err := d.CreateReplicationSource(db.ReplicationSource{Name: "peer", URL: srv.URL, StorageDestID: dest, Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewSyncer(d, nil)
	_, err = s.SyncSource(id, func(float64, string) { panic("boom") })
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	src, err := d.GetReplicationSource(id)
	if err != nil {
		t.Fatal(err)
	}
	if src.LastSyncStatus != "failed" {
		t.Errorf("status = %q", src.LastSyncStatus)
	}
}

func TestClient_OversizedJSONFails(t *testing.T) {
	prev := maxPeerJSONBytes
	maxPeerJSONBytes = 1 << 10
	t.Cleanup(func() { maxPeerJSONBytes = prev })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[" + strings.Repeat(`{"id":1},`, 1<<10) + `{"id":1}]`))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListJobs(); !errors.Is(err, bounded.ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
}
