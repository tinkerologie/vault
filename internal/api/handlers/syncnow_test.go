package handlers

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/replication"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// TestReplicationSyncNow_Accepted drives the 202 happy-path where a real
// (non-nil) syncer is constructed. The background goroutine fires
// SyncSource which fails harmlessly (URL is fake) — we don't assert on
// that, only on the synchronous 202 response.
func TestReplicationSyncNow_Accepted(t *testing.T) {
	t.Parallel()
	_, d := setupReplicationTest(t)

	srcID, err := d.CreateReplicationSource(db.ReplicationSource{
		Name:    "sync-stub-real",
		Type:    "remote_vault",
		URL:     "http://127.0.0.1:1",
		Config:  "{}",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}

	hub := ws.NewHub()
	go hub.Run()
	syncer := replication.NewSyncer(d, hub)

	h := NewReplicationHandler(d, func() *replication.Syncer { return syncer },
		make([]byte, 32), nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/replication/x/sync", nil)
	req = withURLParam(req, "id", strconv.FormatInt(srcID, 10))
	w := httptest.NewRecorder()
	h.SyncNow(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", w.Code, w.Body.String())
	}

	// Give the background goroutine a moment to run/fail so the t.Cleanup
	// of setupReplicationTest doesn't race-close the DB while the syncer
	// is mid-query. The sync will fail (URL is fake) but we don't care
	// about the result — only that we don't panic.
	time.Sleep(100 * time.Millisecond)
}

// TestReplicationDelete_DBClosed drives the DeleteReplicationSource error
// branch.
func TestReplicationDelete_DBClosed(t *testing.T) {
	t.Parallel()
	h, d := setupReplicationTest(t)
	srcID, err := d.CreateReplicationSource(db.ReplicationSource{
		Name: "to-be-deleted", Type: "remote_vault",
		URL: "http://x:24085", Config: "{}", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	_ = d.Close()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/replication/x", nil)
	req = withURLParam(req, "id", strconv.FormatInt(srcID, 10))
	w := httptest.NewRecorder()
	h.Delete(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}
