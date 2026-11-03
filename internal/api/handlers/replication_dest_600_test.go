package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/replication"
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
	srcID, err := d.CreateReplicationSource(db.ReplicationSource{Name: "peer", Type: "remote_vault", URL: "http://peer.invalid:24085", StorageDestID: 1})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateReplicatedJob(db.Job{Name: "[peer] J", SourceID: srcID, StorageDestID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.CreateReplicatedRestorePoint(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"},
		db.RestorePoint{JobID: jobID, BackupType: "full", StoragePath: "J/r1", SourceID: srcID}, time.Time{}, 0); err != nil {
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

// TestReplicationUpdateRefusesDestinationDuringSync600 pins the 409 for a
// destination change while a sync of the source runs, and that other edits
// still go through.
func TestReplicationUpdateRefusesDestinationDuringSync600(t *testing.T) {
	t.Setenv(replication.AllowLoopbackEnv, "1")
	listing, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/jobs":
			once.Do(func() { close(listing) })
			<-release
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(peer.Close)

	_, d := setupReplicationTest(t) // destination 1 exists
	other, err := d.CreateStorageDestination(db.StorageDestination{Name: "other", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	srcID, err := d.CreateReplicationSource(db.ReplicationSource{Name: "peer", Type: "remote_vault", URL: peer.URL, StorageDestID: other})
	if err != nil {
		t.Fatal(err)
	}
	syncer := replication.NewSyncer(d, nil)
	h := NewReplicationHandler(d, func() *replication.Syncer { return syncer }, make([]byte, 32), nil, nil)
	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/replication/x", strings.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", strconv.FormatInt(srcID, 10))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		h.Update(w, req)
		return w
	}

	done := make(chan struct{})
	go func() { _, _ = syncer.SyncSource(srcID, nil); close(done) }()
	<-listing
	if w := put(`{"storage_dest_id":1}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "sync") {
		t.Errorf("move during a sync = %d %s, want 409", w.Code, w.Body.String())
	}
	if w := put(`{"schedule":"0 4 * * *"}`); w.Code != http.StatusOK {
		t.Errorf("schedule edit during a sync = %d %s, want 200", w.Code, w.Body.String())
	}
	close(release)
	<-done
	if w := put(`{"storage_dest_id":1}`); w.Code != http.StatusOK {
		t.Errorf("move after the sync = %d %s, want 200", w.Code, w.Body.String())
	}
}

// TestReplicationUpdateReadsSourceUnderLock600 pins that a schedule-only
// update applies to the source row as it is once the source's lock is free,
// so an update that was waiting cannot write back the destination it saw
// before a move.
func TestReplicationUpdateReadsSourceUnderLock600(t *testing.T) {
	t.Parallel()
	_, d := setupReplicationTest(t) // destination 1 exists
	other, err := d.CreateStorageDestination(db.StorageDestination{Name: "other", Type: "local", Config: `{"path":"/tmp/other"}`})
	if err != nil {
		t.Fatal(err)
	}
	srcID, err := d.CreateReplicationSource(db.ReplicationSource{Name: "peer", Type: "remote_vault", URL: "http://peer.invalid:24085", StorageDestID: 1})
	if err != nil {
		t.Fatal(err)
	}
	syncer := replication.NewSyncer(d, nil)
	h := NewReplicationHandler(d, func() *replication.Syncer { return syncer }, make([]byte, 32), nil, nil)

	moving, finish, moved := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		moved <- syncer.UpdateSource(srcID, func(bool) error {
			close(moving)
			<-finish
			src, err := d.GetReplicationSource(srcID)
			if err != nil {
				return err
			}
			src.StorageDestID = other
			return d.UpdateReplicationSource(src)
		})
	}()
	<-moving

	answered := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPut, "/replication/x", strings.NewReader(`{"schedule":"0 4 * * *"}`))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", strconv.FormatInt(srcID, 10))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		h.Update(w, req)
		answered <- w.Code
	}()
	select {
	case code := <-answered:
		t.Fatalf("schedule edit answered %d while the source was being moved, want it to wait", code)
	case <-time.After(200 * time.Millisecond):
	}
	close(finish)
	if err := <-moved; err != nil {
		t.Fatal(err)
	}
	if code := <-answered; code != http.StatusOK {
		t.Fatalf("schedule edit = %d, want 200", code)
	}
	src, err := d.GetReplicationSource(srcID)
	if err != nil {
		t.Fatal(err)
	}
	if src.StorageDestID != other || src.Schedule != "0 4 * * *" {
		t.Errorf("source = destination %d schedule %q, want %d and the new schedule", src.StorageDestID, src.Schedule, other)
	}
}
