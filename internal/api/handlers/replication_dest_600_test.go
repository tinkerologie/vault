package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tinkerologie/vault/internal/db"
)

// TestReplicationUpdateRefusesStrandingRestorePoints600 pins that REST refuses
// moving a source to another destination while its replicated jobs have
// restore points on the old one, which would fail every later sync.
func TestReplicationUpdateRefusesStrandingRestorePoints600(t *testing.T) {
	t.Parallel()
	h, d := setupReplicationTest(t) // destination 1 exists
	other, err := d.CreateStorageDestination(db.StorageDestination{Name: "other", Type: "local", Config: `{"path":"/tmp/other"}`})
	if err != nil {
		t.Fatal(err)
	}
	srcID, err := d.CreateReplicationSource(db.ReplicationSource{Name: "peer", Type: "remote_vault", URL: "http://192.168.1.1:24085", StorageDestID: 1})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateReplicatedJob(db.Job{Name: "[peer] J", SourceID: srcID, StorageDestID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.CreateReplicatedRestorePoint(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"},
		db.RestorePoint{JobID: jobID, BackupType: "full", StoragePath: "J/r1", SourceID: srcID}, time.Time{}); err != nil {
		t.Fatal(err)
	}

	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/replication/x", strings.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", strconv.FormatInt(srcID, 10))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		h.Update(w, req)
		return w
	}
	w := put(`{"storage_dest_id":` + strconv.FormatInt(other, 10) + `}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "create a new source") {
		t.Fatalf("move = %d %s, want 400 naming the way out", w.Code, w.Body.String())
	}
	if src, _ := d.GetReplicationSource(srcID); src.StorageDestID != 1 {
		t.Fatalf("source destination = %d after a refused move", src.StorageDestID)
	}
	if w := put(`{"schedule":"0 4 * * *"}`); w.Code != http.StatusOK {
		t.Fatalf("schedule edit = %d %s, want 200", w.Code, w.Body.String())
	}
}
