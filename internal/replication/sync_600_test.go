package replication

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
)

// peer600 serves one remote job with one restore point from run 11. history
// answers the job's history page and runs answers /api/v1/runs/11; nil
// answers 404 for either.
func peer600(t *testing.T, jobRunID int64, history, runs http.HandlerFunc, downloads *atomic.Int64) *httptest.Server {
	t.Helper()
	rps := []RemoteRestorePoint{{ID: 1, JobRunID: jobRunID, BackupType: "full", StoragePath: "J/run11", Metadata: `{"items":2}`}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/health":
			_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
		case r.URL.Path == "/api/v1/jobs":
			_ = json.NewEncoder(w).Encode([]RemoteJob{{ID: 5, Name: "J", StorageDestID: 9}})
		case r.URL.Path == "/api/v1/jobs/5/restore-points":
			_ = json.NewEncoder(w).Encode(rps)
		case r.URL.Path == "/api/v1/jobs/5/history" && history != nil:
			history(w, r)
		case r.URL.Path == "/api/v1/runs/11" && runs != nil:
			runs(w, r)
		case r.URL.Path == "/api/v1/storage/9/list":
			prefix := r.URL.Query().Get("prefix")
			_ = json.NewEncoder(w).Encode([]StorageFile{{Name: "f", Path: prefix + "/f", Size: 1}})
		case r.URL.Path == "/api/v1/storage/9/files":
			downloads.Add(1)
			_, _ = w.Write([]byte("x"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func listJSON(runs []RemoteRun) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(runs)
	}
}

func status600(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func runJSON(run map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(run)
	}
}

// A peer run outside the newest history page keeps its outcome on the
// replica: it is fetched on its own, and when the peer cannot describe it the
// copy is marked partial rather than claimed as a success.
func TestSyncSourceRunOutsideHistoryPage600(t *testing.T) {
	t.Parallel()
	// The newest page holds other runs only: run 11 has fallen off it.
	page := listJSON([]RemoteRun{{ID: 900, JobID: 5, Status: "completed"}})
	for _, tc := range []struct {
		name       string
		jobRunID   int64
		history    http.HandlerFunc
		runs       http.HandlerFunc
		wantStatus string
		wantLog    string
		wantFailed bool
	}{
		{name: "fetched partial with no failed items", jobRunID: 11, history: page,
			runs:       runJSON(map[string]any{"id": 11, "job_id": 5, "status": "partial", "items_total": 2, "items_done": 2}),
			wantStatus: "partial"},
		{name: "fetched clean run", jobRunID: 11, history: page,
			runs:       runJSON(map[string]any{"id": 11, "job_id": 5, "status": "completed", "items_total": 2, "items_done": 2}),
			wantStatus: "success"},
		{name: "peer cannot describe the run", jobRunID: 11, history: page,
			wantStatus: "partial", wantLog: "couldn't report how its run 11 ended"},
		{name: "peer answers with another job's run", jobRunID: 11, history: page,
			runs:       runJSON(map[string]any{"id": 11, "job_id": 6, "status": "completed"}),
			wantStatus: "partial", wantLog: "couldn't report"},
		{name: "peer keeps no run outcomes", jobRunID: 11,
			wantStatus: "success"},
		{name: "peer without history answers 405", jobRunID: 11, history: status600(http.StatusMethodNotAllowed),
			runs: status600(http.StatusMethodNotAllowed), wantStatus: "success"},
		// A history listing that failed says nothing about what the peer
		// supports, so a missing run is unconfirmed, never a success.
		{name: "history fails and the run lookup is missing", jobRunID: 11, history: status600(http.StatusInternalServerError),
			wantStatus: "partial", wantLog: "Outcome unconfirmed"},
		{name: "history body unreadable and the run lookup is missing", jobRunID: 11,
			history:    func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>") },
			wantStatus: "partial", wantLog: "Outcome unconfirmed"},
		{name: "history fails but the run is fetched", jobRunID: 11, history: status600(http.StatusBadGateway),
			runs:       runJSON(map[string]any{"id": 11, "job_id": 5, "status": "partial", "items_total": 2, "items_done": 2}),
			wantStatus: "partial"},
		{name: "restore point names no run", jobRunID: 0, history: page,
			runs: func(w http.ResponseWriter, _ *http.Request) {
				t.Error("looked up a run for a restore point without one")
			},
			wantStatus: "success"},
		{name: "lookup fails", jobRunID: 11, history: page, wantFailed: true,
			runs: status600(http.StatusBadGateway)},
		{name: "lookup returns garbage", jobRunID: 11, history: page, wantFailed: true,
			runs: func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var downloads atomic.Int64
			srv := peer600(t, tc.jobRunID, tc.history, tc.runs, &downloads)
			d, srcID := setup585(t, srv.URL)
			res, err := NewSyncer(d, nil).SyncSource(srcID, nil)
			if err != nil {
				t.Fatalf("SyncSource: %v", err)
			}
			if tc.wantFailed {
				if res.JobsFailed != 1 || res.RestorePointsNew != 0 || downloads.Load() != 0 {
					t.Fatalf("result = %+v with %d downloads, want the point failed before downloading", res, downloads.Load())
				}
				return
			}
			if res.RestorePointsNew != 1 || res.JobsFailed != 0 {
				t.Fatalf("result = %+v", res)
			}
			job, err := d.GetJobByName("[P] J")
			if err != nil {
				t.Fatal(err)
			}
			rps, err := d.ListRestorePoints(job.ID)
			if err != nil || len(rps) != 1 {
				t.Fatalf("restore points = %v, %v", rps, err)
			}
			run, err := d.GetJobRun(rps[0].JobRunID)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != tc.wantStatus || !strings.Contains(run.Log, tc.wantLog) {
				t.Errorf("run status %q log %q, want %q containing %q", run.Status, run.Log, tc.wantStatus, tc.wantLog)
			}
		})
	}
}

// The single-run lookup goes through the same client as every other peer call,
// so it carries the API key.
func TestGetRunSendsAPIKey600(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "k" || r.URL.Path != "/api/v1/runs/3" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(db.JobRun{ID: 3, JobID: 4, Status: "partial"})
	}))
	t.Cleanup(srv.Close)
	c, err := NewClientWithAPIKey(srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	run, err := c.GetRun(3)
	if err != nil || run.ID != 3 || run.JobID != 4 || run.Status != "partial" || run.unconfirmed {
		t.Fatalf("GetRun = %+v, %v", run, err)
	}
	c.baseURL = "http://127.0.0.1:1"
	if _, err := c.GetRun(3); err == nil {
		t.Error("GetRun against a closed port succeeded")
	}
}

// A peer timestamp may run at most maxPeerClockSkew ahead of this server's
// clock. A later one would sort ahead of every real backup and escape
// retention, so the copy time is used and the reason is given.
func TestPeerTimeClockSkew600(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	edge := now.Add(maxPeerClockSkew)
	for _, tc := range []struct {
		name, in string
		want     time.Time
		reason   bool
	}{
		{"just inside the window", edge.Add(-time.Nanosecond).Format(time.RFC3339Nano), edge.Add(-time.Nanosecond), false},
		{"at the edge", edge.Format(time.RFC3339Nano), edge, false},
		{"offset form of the edge", edge.In(time.FixedZone("x", 10*3600)).Format(time.RFC3339Nano), edge, false},
		{"just past the edge", edge.Add(time.Nanosecond).Format(time.RFC3339Nano), time.Time{}, true},
		{"far future", "9999-12-31T23:59:59Z", time.Time{}, true},
		{"long ago", "2001-01-01T00:00:00Z", time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"unreadable", "tomorrow", time.Time{}, false},
	} {
		got, reason := peerTime(tc.in, now)
		if !got.Equal(tc.want) || (reason != "") != tc.reason {
			t.Errorf("%s: peerTime(%q) = %v, %q", tc.name, tc.in, got, reason)
		}
	}
}

// A synced restore point dated far in the future is recorded at the copy time,
// with the reason in its run's log.
func TestSyncSourceFutureTimestamp600(t *testing.T) {
	t.Parallel()
	var downloads atomic.Int64
	srv := peer585(t, []RemoteRestorePoint{{ID: 1, BackupType: "full", StoragePath: "J/run1", CreatedAt: "9999-01-01T00:00:00Z"}}, &downloads)
	d, srcID := setup585(t, srv.URL)
	before := time.Now().Add(-time.Minute)
	if res, err := NewSyncer(d, nil).SyncSource(srcID, nil); err != nil || res.RestorePointsNew != 1 {
		t.Fatalf("SyncSource = %+v, %v", res, err)
	}
	job, _ := d.GetJobByName("[P] J")
	rps, err := d.ListRestorePoints(job.ID)
	if err != nil || len(rps) != 1 {
		t.Fatalf("restore points = %v, %v", rps, err)
	}
	if at := rps[0].CreatedAt; at.Before(before) || at.After(time.Now().Add(time.Minute)) {
		t.Errorf("created_at = %v, want about now", at)
	}
	run, err := d.GetJobRun(rps[0].JobRunID)
	if err != nil || !strings.Contains(run.Log, "ahead of this server's clock") {
		t.Errorf("run log = %q, %v", run.Log, err)
	}
}

// A destination the sync binds is configuration, so the daemon's flush hook
// runs once it commits, and only when something was bound.
func TestSyncSourceFlushesBoundDestination600(t *testing.T) {
	t.Parallel()
	var downloads atomic.Int64
	srv := peer585(t, []RemoteRestorePoint{{ID: 1, BackupType: "full", StoragePath: "J/run1"}}, &downloads)
	d := openTestDB(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "l", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	srcID, err := d.CreateReplicationSource(db.ReplicationSource{Name: "P", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	s := NewSyncer(d, nil)
	var flushes atomic.Int64
	s.SetConfigChangeHook(func() {
		// The binding is committed before the flush is asked for.
		if src, err := d.GetReplicationSource(srcID); err != nil || src.StorageDestID != destID {
			t.Errorf("flush before the binding committed: %+v, %v", src, err)
		}
		flushes.Add(1)
	})
	for range 2 {
		if _, err := s.SyncSource(srcID, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := flushes.Load(); got != 1 {
		t.Errorf("flushes = %d, want 1: one for the binding, none for a sync that changed none", got)
	}
}

// peerHook600 is peer585 with a hook run while each file is downloaded.
func peerHook600(t *testing.T, onDownload func()) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
		case "/api/v1/jobs":
			_ = json.NewEncoder(w).Encode([]RemoteJob{{ID: 5, Name: "J", StorageDestID: 9}})
		case "/api/v1/jobs/5/restore-points":
			_ = json.NewEncoder(w).Encode([]RemoteRestorePoint{{ID: 1, BackupType: "full", StoragePath: "J/run1"}})
		case "/api/v1/storage/9/list":
			_ = json.NewEncoder(w).Encode([]StorageFile{{Name: "f", Path: r.URL.Query().Get("prefix") + "/f", Size: 1}})
		case "/api/v1/storage/9/files":
			onDownload()
			_, _ = w.Write([]byte("x"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A source's destination cannot change while a sync of it runs, so no sync
// is left writing to the old destination after its jobs move.
func TestChangeSourceDestinationWhileSyncing600(t *testing.T) {
	t.Parallel()
	downloading, release := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	srv := peerHook600(t, func() {
		if once.CompareAndSwap(false, true) {
			close(downloading)
		}
		<-release
	})
	d, srcID := setup585(t, srv.URL)
	s := NewSyncer(d, nil)
	done := make(chan error, 1)
	go func() { _, err := s.SyncSource(srcID, nil); done <- err }()
	<-downloading

	applied := false
	if err := s.ChangeSourceDestination(srcID, func() error { applied = true; return nil }); !errors.Is(err, ErrSyncInProgress) || applied {
		t.Fatalf("change during a sync = %v (applied %v), want ErrSyncInProgress", err, applied)
	}
	// Another source is not held up.
	if err := s.ChangeSourceDestination(srcID+1, func() error { return nil }); err != nil {
		t.Fatalf("change of another source: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeSourceDestination(srcID, func() error { applied = true; return nil }); err != nil || !applied {
		t.Fatalf("change after the sync = %v (applied %v)", err, applied)
	}
}

// If a job's destination changes while its files download anyway, the restore
// point is not recorded against a destination that lacks them.
func TestSyncSourceJobMovedDuringDownload600(t *testing.T) {
	t.Parallel()
	var d *db.DB
	var other int64
	srv := peerHook600(t, func() {
		if _, err := d.Exec(`UPDATE jobs SET storage_dest_id = ? WHERE name = '[P] J'`, other); err != nil {
			t.Error(err)
		}
	})
	var srcID int64
	d, srcID = setup585(t, srv.URL)
	var err error
	other, err = d.CreateStorageDestination(db.StorageDestination{Name: "other", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewSyncer(d, nil).SyncSource(srcID, nil)
	if err != nil || res.JobsFailed != 1 || res.RestorePointsNew != 0 {
		t.Fatalf("SyncSource = %+v, %v; want the point refused", res, err)
	}
	if n := countRows(t, d, "restore_points"); n != 0 {
		t.Errorf("restore_points = %d, want 0", n)
	}
}
