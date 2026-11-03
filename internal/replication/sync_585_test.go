package replication

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
		run := replicatedRun(7, "peer", rrp, tc.meta, 9, nil)
		if run.Status != tc.status || run.ItemsTotal != tc.total || run.ItemsDone != tc.done || run.ItemsFailed != tc.failedItems ||
			run.JobID != 7 || run.BackupType != "incremental" || run.SizeBytes != 9 || !strings.Contains(run.Log, "peer") {
			t.Errorf("%s: run = %+v", tc.meta, run)
		}
	}
}

func TestPeerTime585(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	if got, _ := peerTime("2026-10-15T13:25:59.5+10:00", now); got.IsZero() || got.UTC().Hour() != 3 {
		t.Errorf("peerTime = %v", got)
	}
	for _, bad := range []string{"", "2026-10-15 13:25:59"} {
		if got, _ := peerTime(bad, now); !got.IsZero() {
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

// peerWithRuns is peer585 plus a run history, so run outcomes can be checked.
func peerWithRuns(t *testing.T, rps []RemoteRestorePoint, runs []RemoteRun, failList string, downloads *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/health":
			_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
		case r.URL.Path == "/api/v1/jobs":
			_ = json.NewEncoder(w).Encode([]RemoteJob{{ID: 5, Name: "J", StorageDestID: 9}})
		case r.URL.Path == "/api/v1/jobs/5/restore-points":
			_ = json.NewEncoder(w).Encode(rps)
		case r.URL.Path == "/api/v1/jobs/5/history":
			_ = json.NewEncoder(w).Encode(runs)
		case r.URL.Path == "/api/v1/storage/9/list":
			prefix := r.URL.Query().Get("prefix")
			if prefix == failList {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
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

// A dependent point is linked to its parent's local copy whatever order the
// peer lists them in, and a dependent point whose parent has no local copy is
// a failure, not a standalone point that would restore without its base.
func TestSyncSourceParentMapping585(t *testing.T) {
	t.Parallel()
	full := RemoteRestorePoint{ID: 1, BackupType: "full", StoragePath: "J/full"}
	inc := RemoteRestorePoint{ID: 2, BackupType: "incremental", StoragePath: "J/inc", ParentRestorePointID: 1}
	orphan := RemoteRestorePoint{ID: 3, BackupType: "differential", StoragePath: "J/orphan", ParentRestorePointID: 99}

	t.Run("child listed first", func(t *testing.T) {
		t.Parallel()
		var downloads atomic.Int64
		srv := peerWithRuns(t, []RemoteRestorePoint{inc, full}, nil, "", &downloads)
		d, srcID := setup585(t, srv.URL)
		if res, err := NewSyncer(d, nil).SyncSource(srcID, nil); err != nil || res.RestorePointsNew != 2 {
			t.Fatalf("SyncSource = %+v, %v", res, err)
		}
		job, _ := d.GetJobByName("[P] J")
		rps, _ := d.ListRestorePoints(job.ID)
		byPath := map[string]db.RestorePoint{}
		for _, rp := range rps {
			byPath[rp.StoragePath] = rp
		}
		if byPath["J/inc"].ParentRestorePointID != byPath["J/full"].ID || byPath["J/full"].ID == 0 {
			t.Errorf("incremental parent = %d, want local full %d", byPath["J/inc"].ParentRestorePointID, byPath["J/full"].ID)
		}
	})

	t.Run("parent not listed", func(t *testing.T) {
		t.Parallel()
		var downloads atomic.Int64
		srv := peerWithRuns(t, []RemoteRestorePoint{orphan}, nil, "", &downloads)
		d, srcID := setup585(t, srv.URL)
		res, err := NewSyncer(d, nil).SyncSource(srcID, nil)
		if err != nil || res.RestorePointsNew != 0 || res.JobsFailed != 1 {
			t.Fatalf("SyncSource = %+v, %v; want the point failed", res, err)
		}
		if downloads.Load() != 0 || countRows(t, d, "restore_points") != 0 || countRows(t, d, "job_runs") != 0 {
			t.Errorf("downloads %d, rows recorded for a point with no parent", downloads.Load())
		}
		if src, _ := d.GetReplicationSource(srcID); src.LastSyncStatus != "failed" {
			t.Errorf("status = %q, want failed", src.LastSyncStatus)
		}
	})

	t.Run("parent not copied", func(t *testing.T) {
		t.Parallel()
		var downloads atomic.Int64
		srv := peerWithRuns(t, []RemoteRestorePoint{full, inc}, nil, "J/full", &downloads)
		d, srcID := setup585(t, srv.URL)
		res, err := NewSyncer(d, nil).SyncSource(srcID, nil)
		if err != nil || res.RestorePointsNew != 0 || res.JobsFailed != 1 {
			t.Fatalf("SyncSource = %+v, %v; want both points failed", res, err)
		}
		if downloads.Load() != 0 || countRows(t, d, "restore_points") != 0 {
			t.Errorf("the incremental was copied without its full (downloads %d)", downloads.Load())
		}
	})

	if msg := func() string {
		_, err := localParent(orphan, map[int64]RemoteRestorePoint{}, nil, func(s string) string { return s })
		return err.Error()
	}(); !strings.Contains(msg, "depends on restore point 99") {
		t.Errorf("missing-parent reason = %q", msg)
	}
}

func TestParentsFirst585(t *testing.T) {
	t.Parallel()
	rps := []RemoteRestorePoint{
		{ID: 3, ParentRestorePointID: 2}, {ID: 2, ParentRestorePointID: 1}, {ID: 1},
		{ID: 7, ParentRestorePointID: 8}, {ID: 8, ParentRestorePointID: 7}, // a loop
		{ID: 0}, {ID: 0},
	}
	var got []int64
	for _, rp := range parentsFirst(rps) {
		got = append(got, rp.ID)
	}
	if fmt.Sprint(got) != "[1 2 3 8 7 0 0]" {
		t.Errorf("order = %v", got)
	}
}

// A peer backup that finished partial, with files it could not read and no
// failed items, stays partial on the replica.
func TestSyncSourcePeerRunOutcome585(t *testing.T) {
	t.Parallel()
	var downloads atomic.Int64
	srv := peerWithRuns(t,
		[]RemoteRestorePoint{
			{ID: 1, JobRunID: 11, BackupType: "full", StoragePath: "J/skips", Metadata: `{"items":2,"items_failed":0}`},
			{ID: 2, JobRunID: 12, BackupType: "full", StoragePath: "J/clean", Metadata: `{"items":2}`},
		},
		[]RemoteRun{
			{ID: 11, Status: "partial", ItemsTotal: 2, ItemsDone: 2},
			{ID: 12, Status: "completed", ItemsTotal: 2, ItemsDone: 2},
		}, "", &downloads)
	d, srcID := setup585(t, srv.URL)
	if res, err := NewSyncer(d, nil).SyncSource(srcID, nil); err != nil || res.RestorePointsNew != 2 {
		t.Fatalf("SyncSource = %+v, %v", res, err)
	}
	job, _ := d.GetJobByName("[P] J")
	rps, _ := d.ListRestorePoints(job.ID)
	want := map[string]string{"J/skips": "partial", "J/clean": "success"}
	for _, rp := range rps {
		run, err := d.GetJobRun(rp.JobRunID)
		if err != nil || run.Status != want[rp.StoragePath] || run.ItemsDone != 2 || run.ItemsTotal != 2 {
			t.Errorf("%s: run = %+v, %v; want %s", rp.StoragePath, run, err, want[rp.StoragePath])
		}
	}

	for status, wantStatus := range map[string]string{"completed": "success", "success": "success", "partial": "partial", "cancelled": "partial"} {
		run := replicatedRun(1, "p", RemoteRestorePoint{}, "{}", 0, &RemoteRun{Status: status})
		if run.Status != wantStatus {
			t.Errorf("peer %s -> %s, want %s", status, run.Status, wantStatus)
		}
	}
	if run := replicatedRun(1, "p", RemoteRestorePoint{}, "{}", 0, &RemoteRun{Status: "completed", ItemsFailed: 1}); run.Status != "partial" {
		t.Errorf("completed with failed items -> %s, want partial", run.Status)
	}
}

// If the auto-selected destination cannot be saved, the sync stops before
// downloading anything it could not find again.
func TestSyncSourceAdoptDestinationFails585(t *testing.T) {
	t.Parallel()
	var downloads atomic.Int64
	srv := peer585(t, []RemoteRestorePoint{{ID: 1, BackupType: "full", StoragePath: "J/run1"}}, &downloads)
	d := openTestDB(t)
	if _, err := d.CreateStorageDestination(db.StorageDestination{Name: "l", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`}); err != nil {
		t.Fatal(err)
	}
	srcID, err := d.CreateReplicationSource(db.ReplicationSource{Name: "P", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`CREATE TRIGGER no_adopt BEFORE UPDATE OF storage_dest_id ON replication_sources
		BEGIN SELECT RAISE(ABORT, 'rejected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSyncer(d, nil).SyncSource(srcID, nil); err == nil || !strings.Contains(err.Error(), "record storage destination") {
		t.Fatalf("err = %v, want the adoption failure", err)
	}
	if downloads.Load() != 0 {
		t.Errorf("downloaded %d files without a recorded destination", downloads.Load())
	}
	if src, _ := d.GetReplicationSource(srcID); src.LastSyncStatus != "failed" {
		t.Errorf("status = %q, want failed", src.LastSyncStatus)
	}
}

// A restore point whose data is in the dedup chunk store is refused before
// anything is downloaded: replication does not copy the peer's _vault/packs,
// so the replica could not restore it. A dedup destination alone is not a
// reason: VM and ZFS backups there are written the classic way and replicate.
func TestSyncSourceDedupPoints585(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		rp      RemoteRestorePoint
		refused bool
	}{
		{"dedup container", RemoteRestorePoint{ID: 1, BackupType: "full", StoragePath: "J/run1", Metadata: `{"items":1,"item_manifests":{"plex":"ab12"}}`}, true},
		{"manifest id", RemoteRestorePoint{ID: 1, BackupType: "full", StoragePath: "J/run1", ManifestID: []byte{1, 2}}, true},
		{"classic VM on a dedup destination", RemoteRestorePoint{ID: 1, BackupType: "full", StoragePath: "J/run1", Metadata: `{"items":1,"vm_checkpoints":{"win11":"c1"}}`}, false},
		{"classic ZFS on a dedup destination", RemoteRestorePoint{ID: 1, BackupType: "full", StoragePath: "J/run1", Metadata: `{"items":1,"zfs_snapshots":{"tank/data":"s1"},"item_manifests":{"tank/data":""}}`}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var downloads, lists atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/health":
					_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
				case "/api/v1/jobs":
					_ = json.NewEncoder(w).Encode([]RemoteJob{{ID: 5, Name: "J", StorageDestID: 9}})
				case "/api/v1/jobs/5/restore-points":
					_ = json.NewEncoder(w).Encode([]RemoteRestorePoint{tc.rp})
				case "/api/v1/storage/9":
					// Every case sits on a dedup destination; only the
					// point's own data may decide.
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 9, "dedup_enabled": true})
				case "/api/v1/storage/9/list":
					lists.Add(1)
					_ = json.NewEncoder(w).Encode([]StorageFile{{Name: "f", Path: "J/run1/f", Size: 1}})
				case "/api/v1/storage/9/files":
					downloads.Add(1)
					_, _ = w.Write([]byte("x"))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(srv.Close)
			d := openTestDB(t)
			storageDir := t.TempDir()
			destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "l", Type: "local", Config: `{"path":"` + storageDir + `"}`})
			srcID, _ := d.CreateReplicationSource(db.ReplicationSource{Name: "P", URL: srv.URL, StorageDestID: destID})

			res, err := NewSyncer(d, nil).SyncSource(srcID, nil)
			if err != nil {
				t.Fatal(err)
			}
			src, _ := d.GetReplicationSource(srcID)
			if !tc.refused {
				if res.RestorePointsNew != 1 || res.JobsFailed != 0 || src.LastSyncStatus != "success" || downloads.Load() != 1 {
					t.Fatalf("classic point: result %+v, status %q, downloads %d; want it replicated", res, src.LastSyncStatus, downloads.Load())
				}
				return
			}
			if res.JobsFailed != 1 || res.RestorePointsNew != 0 || src.LastSyncStatus != "failed" {
				t.Fatalf("result %+v, status %q; want the point failed", res, src.LastSyncStatus)
			}
			if lists.Load()+downloads.Load() != 0 {
				t.Errorf("listed %d and downloaded %d for a refused point", lists.Load(), downloads.Load())
			}
			if n := countRows(t, d, "restore_points") + countRows(t, d, "job_runs"); n != 0 {
				t.Errorf("rows recorded = %d, want 0", n)
			}
			if entries, _ := os.ReadDir(storageDir); len(entries) != 0 {
				t.Errorf("files left on the replica: %v", entries)
			}
		})
	}
	if err := dedupUnsupported(RemoteRestorePoint{Metadata: `{"item_manifests":{"a":"b"}}`}); !errors.Is(err, errDedupUnsupported) || !strings.Contains(err.Error(), "deduplicated backups isn't supported yet") {
		t.Errorf("reason = %v", err)
	}
}

// A job replicated while its source had no destination is bound to the
// destination the source names now, so restore can open its files.
func TestSyncSourceBindsOlderUnboundJobs585(t *testing.T) {
	t.Parallel()
	var downloads atomic.Int64
	srv := peer585(t, []RemoteRestorePoint{{ID: 1, BackupType: "full", StoragePath: "J/run1"}}, &downloads)
	d, srcID := setup585(t, srv.URL)
	src, _ := d.GetReplicationSource(srcID)
	older, err := d.CreateReplicatedJob(db.Job{Name: "[P] J", SourceID: srcID})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := NewSyncer(d, nil).SyncSource(srcID, nil); err != nil || res.RestorePointsNew != 1 {
		t.Fatalf("SyncSource = %+v, %v", res, err)
	}
	if job, _ := d.GetJob(older); job.StorageDestID != src.StorageDestID || job.StorageDestID == 0 {
		t.Errorf("older job destination = %d, want the source's %d", job.StorageDestID, src.StorageDestID)
	}
}
