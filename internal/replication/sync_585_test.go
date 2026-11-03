package replication

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

// peer585 serves one remote job whose restore points each hold one file.
func peer585(t *testing.T, rps []RemoteRestorePoint, downloads *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/health":
			_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
		case r.URL.Path == "/api/v1/jobs":
			_ = json.NewEncoder(w).Encode([]RemoteJob{{ID: 5, Name: "J", StorageDestID: 9}})
		case r.URL.Path == "/api/v1/jobs/5/restore-points":
			_ = json.NewEncoder(w).Encode(rps)
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

func setup585(t *testing.T, url string) (*db.DB, int64) {
	t.Helper()
	d := openTestDB(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "l", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	srcID, err := d.CreateReplicationSource(db.ReplicationSource{Name: "P", URL: url, StorageDestID: destID})
	if err != nil {
		t.Fatal(err)
	}
	return d, srcID
}

func countRows(t *testing.T, d *db.DB, table string) int {
	t.Helper()
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// When a restore point cannot be recorded, the sync fails, the failure is
// logged as a warning, no run is left behind, and last_sync_success_at does
// not advance. Once the cause is gone, the next sync records it.
func TestSyncSourceRestorePointInsertFails585(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	var downloads atomic.Int64
	srv := peer585(t, []RemoteRestorePoint{{ID: 1, BackupType: "full", StoragePath: "J/run1"}}, &downloads)
	d, srcID := setup585(t, srv.URL)
	if _, err := d.Exec(`CREATE TRIGGER reject_rp BEFORE INSERT ON restore_points
		BEGIN SELECT RAISE(ABORT, 'rejected'); END`); err != nil {
		t.Fatal(err)
	}
	s := NewSyncer(d, nil)

	res, err := s.SyncSource(srcID, nil)
	if err != nil {
		t.Fatalf("SyncSource: %v", err)
	}
	if res.JobsFailed != 1 || res.RestorePointsNew != 0 || res.BytesTransferred != 0 {
		t.Errorf("result = %+v, want the job failed with nothing recorded", res)
	}
	src, err := d.GetReplicationSource(srcID)
	if err != nil {
		t.Fatal(err)
	}
	if src.LastSyncStatus != "failed" || src.LastSyncSuccessAt != nil || src.LastSyncError == "" {
		t.Errorf("source = status %q, success_at %v, error %q; want failed", src.LastSyncStatus, src.LastSyncSuccessAt, src.LastSyncError)
	}
	if n := countRows(t, d, "job_runs"); n != 0 {
		t.Errorf("job_runs = %d after a failed insert, want 0", n)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "J/run1") {
		t.Errorf("restore point failure not logged as a warning:\n%s", logs.String())
	}

	if _, err := d.Exec(`DROP TRIGGER reject_rp`); err != nil {
		t.Fatal(err)
	}
	if res, err = s.SyncSource(srcID, nil); err != nil || res.RestorePointsNew != 1 {
		t.Fatalf("retry = %+v, %v; want the point recorded", res, err)
	}
	if n := countRows(t, d, "job_runs"); n != 1 {
		t.Errorf("job_runs = %d after retry, want 1", n)
	}
	if src, _ := d.GetReplicationSource(srcID); src.LastSyncStatus != "success" || src.LastSyncSuccessAt == nil {
		t.Errorf("source after retry = %q, %v; want success", src.LastSyncStatus, src.LastSyncSuccessAt)
	}
}

// One recorded point and one failed point make a partial sync, and only the
// recorded point has a run.
func TestSyncSourceMixedRestorePoints585(t *testing.T) {
	t.Parallel()
	var downloads atomic.Int64
	srv := peer585(t, []RemoteRestorePoint{
		{ID: 1, BackupType: "full", StoragePath: "J/run1", Metadata: `{"items":1,"items_failed":1}`},
		{ID: 2, BackupType: "full", StoragePath: "J/run2"},
	}, &downloads)
	d, srcID := setup585(t, srv.URL)
	if _, err := d.Exec(`CREATE TRIGGER reject_rp BEFORE INSERT ON restore_points
		WHEN NEW.storage_path = 'J/run2' BEGIN SELECT RAISE(ABORT, 'rejected'); END`); err != nil {
		t.Fatal(err)
	}

	res, err := NewSyncer(d, nil).SyncSource(srcID, nil)
	if err != nil {
		t.Fatalf("SyncSource: %v", err)
	}
	if res.RestorePointsNew != 1 || res.JobsFailed != 1 {
		t.Errorf("result = %+v, want 1 point recorded and the job failed", res)
	}
	if src, _ := d.GetReplicationSource(srcID); src.LastSyncStatus != "partial" || src.LastSyncSuccessAt != nil {
		t.Errorf("status = %q, success_at = %v; want partial", src.LastSyncStatus, src.LastSyncSuccessAt)
	}
	job, err := d.GetJobByName("[P] J")
	if err != nil {
		t.Fatal(err)
	}
	runs, err := d.GetJobRuns(job.ID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, %v; want 1", runs, err)
	}
	// The peer's backup had a failed item, so its run is partial.
	if runs[0].Status != "partial" || runs[0].ItemsDone != 1 || runs[0].ItemsFailed != 1 || runs[0].ItemsTotal != 2 {
		t.Errorf("run = %+v", runs[0])
	}
}

// A peer listing the same restore point twice gets it recorded once.
func TestSyncSourceDuplicateListing585(t *testing.T) {
	t.Parallel()
	var downloads atomic.Int64
	rp := RemoteRestorePoint{ID: 1, BackupType: "full", StoragePath: "J/run1"}
	dup := rp
	dup.StoragePath = "J/run1/"
	srv := peer585(t, []RemoteRestorePoint{rp, dup}, &downloads)
	d, srcID := setup585(t, srv.URL)

	res, err := NewSyncer(d, nil).SyncSource(srcID, nil)
	if err != nil || res.RestorePointsNew != 1 || res.JobsFailed != 0 {
		t.Fatalf("SyncSource = %+v, %v", res, err)
	}
	if n := countRows(t, d, "restore_points"); n != 1 {
		t.Errorf("restore_points = %d, want 1", n)
	}
	if downloads.Load() != 1 {
		t.Errorf("downloads = %d, want 1", downloads.Load())
	}
}

func TestReplicatedRunCounts585(t *testing.T) {
	t.Parallel()
	rrp := RemoteRestorePoint{BackupType: "incremental"}
	for _, tc := range []struct {
		meta                     string
		status                   string
		total, done, failedItems int
	}{
		{`{"items":3}`, "success", 3, 3, 0},
		{`{"items":2,"items_failed":1}`, "partial", 3, 2, 1},
		{`{"items":-4,"items_failed":-1}`, "success", 0, 0, 0},
		{`{"items":[1,2]}`, "success", 0, 0, 0},
		{`not json`, "success", 0, 0, 0},
	} {
		run := replicatedRun(7, "peer", rrp, tc.meta, 9)
		if run.Status != tc.status || run.ItemsTotal != tc.total || run.ItemsDone != tc.done || run.ItemsFailed != tc.failedItems ||
			run.JobID != 7 || run.BackupType != "incremental" || run.SizeBytes != 9 || !strings.Contains(run.Log, "peer") {
			t.Errorf("%s: run = %+v", tc.meta, run)
		}
	}
}

func TestPeerTime585(t *testing.T) {
	t.Parallel()
	if got := peerTime("2026-10-15T13:25:59.5+10:00"); got.IsZero() || got.UTC().Hour() != 3 {
		t.Errorf("peerTime = %v", got)
	}
	for _, bad := range []string{"", "2026-10-15 13:25:59"} {
		if got := peerTime(bad); !got.IsZero() {
			t.Errorf("peerTime(%q) = %v, want zero", bad, got)
		}
	}
}

// Restore points recorded from a job that also failed make the sync partial.
func TestCompleteSyncStatusPointsFromFailedJob585(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
	id, err := d.CreateReplicationSource(db.ReplicationSource{Name: "s", URL: "http://x", StorageDestID: destID})
	if err != nil {
		t.Fatal(err)
	}
	NewSyncer(d, nil).completeSyncStatus(id, "s", &SyncResult{JobsFailed: 1, RestorePointsNew: 1}, func(float64, string) {})
	if src, _ := d.GetReplicationSource(id); src.LastSyncStatus != "partial" {
		t.Errorf("status = %q, want partial", src.LastSyncStatus)
	}
}
