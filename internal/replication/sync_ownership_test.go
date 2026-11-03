package replication

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/maintlock"
	"github.com/tinkerologie/vault/internal/storage"
)

// A job that merely shares the replicated name is not this source's job.
func TestEnsureLocalJobRejectsNameCollision(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	destA, _ := d.CreateStorageDestination(db.StorageDestination{Name: "a", Type: "local", Config: `{"path":"/tmp/a"}`})
	destB, _ := d.CreateStorageDestination(db.StorageDestination{Name: "b", Type: "local", Config: `{"path":"/tmp/b"}`})
	s := NewSyncer(d, nil)

	if _, err := d.CreateJob(db.Job{Name: "[S] local", BackupTypeChain: "full", StorageDestID: destA}); err != nil {
		t.Fatal(err)
	}
	src := db.ReplicationSource{ID: 1, Name: "S", StorageDestID: destA}
	if _, err := s.ensureLocalJob(src, src.StorageDestID, RemoteJob{Name: "local"}); err == nil || !strings.Contains(err.Error(), "not replicated from this source") {
		t.Fatalf("collision with a local job = %v", err)
	}
	if _, err := d.CreateReplicatedJob(db.Job{Name: "[S] peer2", StorageDestID: destA, SourceID: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ensureLocalJob(src, src.StorageDestID, RemoteJob{Name: "peer2"}); err == nil {
		t.Fatal("collision with another source's job accepted")
	}

	own, err := d.CreateReplicatedJob(db.Job{Name: "[S] own", StorageDestID: destA, SourceID: 1})
	if err != nil {
		t.Fatal(err)
	}
	moved := src
	moved.StorageDestID = destB
	if _, err := s.ensureLocalJob(moved, moved.StorageDestID, RemoteJob{Name: "own"}); err == nil || !strings.Contains(err.Error(), "storage destination") {
		t.Fatalf("destination mismatch = %v", err)
	}
	// A source on auto-select writes to the resolved destination, which is what
	// the job is compared against.
	auto := src
	auto.StorageDestID = 0
	if _, err := s.ensureLocalJob(auto, destB, RemoteJob{Name: "own"}); err == nil || !strings.Contains(err.Error(), "storage destination") {
		t.Fatalf("auto-selected destination mismatch = %v", err)
	}
	// An existing job from an older version with no destination keeps working.
	legacy, err := d.CreateReplicatedJob(db.Job{Name: "[S] legacy", SourceID: 1})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int64{"own": own, "legacy": legacy} {
		if got, err := s.ensureLocalJob(src, src.StorageDestID, RemoteJob{Name: name}); err != nil || got != want {
			t.Errorf("ensureLocalJob(%s) = %d, %v, want %d", name, got, err, want)
		}
	}
}

// A peer cannot write over, or take ownership of, another job's restore point.
func TestSyncRestorePointRejectsPathOfAnotherJob(t *testing.T) {
	t.Parallel()
	for _, peerPath := range []string{"victim/run", "victim/run/sub", "victim_x/../victim/run", "_vault/packs", `victim\run/sub`, `_vault\packs/aa`, "$vault/packs"} {
		t.Run(peerPath, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			d := openTestDB(t)
			destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"` + root + `"}`})
			if err != nil {
				t.Fatal(err)
			}
			victim, err := d.CreateJob(db.Job{Name: "victim", BackupTypeChain: "full", StorageDestID: destID})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.CreateRestorePoint(db.RestorePoint{JobRunID: runFor(t, d, victim), JobID: victim, BackupType: "full", StoragePath: "victim/run"}); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "victim/run"), 0o750); err != nil {
				t.Fatal(err)
			}
			keep := filepath.Join(root, "victim/run/data.bin")
			if err := os.WriteFile(keep, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}

			downloads := 0
			clean := filepath.ToSlash(filepath.Clean(peerPath))
			c := newListingServer(t, map[string][]StorageFile{clean: {{Path: clean + "/data.bin", Size: 3}}}, &downloads)
			src := db.ReplicationSource{ID: 1, Name: "S", StorageDestID: destID}
			rj := RemoteJob{ID: 8, StorageDestID: 3, Name: "J"}
			s := NewSyncer(d, nil)
			jobID, err := s.ensureLocalJob(src, src.StorageDestID, rj)
			if err != nil {
				t.Fatal(err)
			}
			rrp := RemoteRestorePoint{ID: 1, JobID: 8, BackupType: "full", StoragePath: peerPath}
			if _, _, err := s.syncRestorePoint(c, src, destID, rj, rrp, jobID, 0, nil, storage.NewLocalAdapter(root)); err == nil {
				t.Fatal("peer path accepted")
			}
			if downloads != 0 {
				t.Fatalf("downloaded %d file(s)", downloads)
			}
			if b, _ := os.ReadFile(keep); string(b) != "original" {
				t.Fatalf("victim file = %q", b)
			}
			if rps, _ := d.ListRestorePoints(jobID); len(rps) != 0 {
				t.Fatalf("created %d row(s)", len(rps))
			}
		})
	}
}

// A path another job holds is refused even when that job came from the same
// source: the peer could otherwise point one job at another's backup. A free
// path, and a retry for this job's own path, still sync.
func TestSyncRestorePointRefusesSameSourceOtherJob(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	d := openTestDB(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"` + root + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := d.CreateReplicatedJob(db.Job{Name: "[S] B", StorageDestID: destID, SourceID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateRestorePoint(db.RestorePoint{JobRunID: runFor(t, d, sibling), JobID: sibling, BackupType: "full", StoragePath: "B/1", SourceID: 1}); err != nil {
		t.Fatal(err)
	}
	downloads := 0
	c := newListingServer(t, map[string][]StorageFile{
		"B/1": {{Path: "B/1/a.bin", Size: 3}},
		"A/2": {{Path: "A/2/a.bin", Size: 3}},
		"A/3": {{Path: "A/3/a.bin", Size: 3}},
	}, &downloads)
	src := db.ReplicationSource{ID: 1, Name: "S", StorageDestID: destID}
	rj := RemoteJob{ID: 8, StorageDestID: 3, Name: "A"}
	s := NewSyncer(d, nil)
	jobID, err := s.ensureLocalJob(src, destID, rj)
	if err != nil {
		t.Fatal(err)
	}
	sync := func(p string) error {
		_, _, err := s.syncRestorePoint(c, src, destID, rj, RemoteRestorePoint{ID: 1, JobID: 8, BackupType: "full", StoragePath: p}, jobID, 0, nil, storage.NewLocalAdapter(root))
		return err
	}
	if err := sync("B/1"); !errors.Is(err, errPathRefused) {
		t.Fatalf("same-source sibling path = %v, want refused", err)
	}
	if downloads != 0 {
		t.Fatalf("downloaded %d file(s) over a sibling's backup", downloads)
	}
	if err := sync("A/2"); err != nil && errors.Is(err, errPathRefused) {
		t.Fatalf("free path refused: %v", err)
	}
	// This job's own row does not block a retry of the same path.
	if _, err := d.CreateRestorePoint(db.RestorePoint{JobRunID: runFor(t, d, jobID), JobID: jobID, BackupType: "full", StoragePath: "A/3", SourceID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := sync("A/3"); err != nil && errors.Is(err, errPathRefused) {
		t.Fatalf("retry of own path refused: %v", err)
	}
	if downloads != 2 {
		t.Fatalf("downloads = %d, want 2", downloads)
	}
}

// A sync claims its destination for as long as it runs.
func TestSyncHoldsDestinationDuringRun(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	var claimed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/restore-points"):
			rel, ok := maintlock.TryExclusiveDestination(d, destID)
			if ok {
				rel()
			}
			claimed.Store(!ok)
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			_, _ = w.Write([]byte(`[{"id":1,"name":"J"}]`))
		default:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer srv.Close()
	srcID, err := d.CreateReplicationSource(db.ReplicationSource{Name: "S", Type: "remote_vault", URL: srv.URL, StorageDestID: destID, Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSyncer(d, nil).SyncSource(srcID, nil); err != nil {
		t.Fatal(err)
	}
	if !claimed.Load() {
		t.Fatal("destination was not held while the sync ran")
	}
	rel, ok := maintlock.TryExclusiveDestination(d, destID)
	if !ok {
		t.Fatal("destination still held after the sync")
	}
	rel()
}

func runFor(t *testing.T, d *db.DB, jobID int64) int64 {
	t.Helper()
	id, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Two syncs cannot download to overlapping paths at once: neither has a row yet
// for the ownership check to see.
func TestSyncRestorePointRefusesPathInProgress(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	d := openTestDB(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"` + root + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	downloads := 0
	c := newListingServer(t, map[string][]StorageFile{"J/1": {{Path: "J/1/a.bin", Size: 3}}}, &downloads)
	src := db.ReplicationSource{ID: 1, Name: "S", StorageDestID: destID}
	rj := RemoteJob{ID: 8, StorageDestID: 3, Name: "J"}
	s := NewSyncer(d, nil)
	jobID, err := s.ensureLocalJob(src, destID, rj)
	if err != nil {
		t.Fatal(err)
	}
	release, ok := s.claimPath(destID, "J/1/sub")
	if !ok {
		t.Fatal("first claim refused")
	}
	if rel, ok := s.claimPath(destID+1, "J/1"); !ok {
		t.Fatal("another destination must be unaffected")
	} else {
		rel()
	}
	rrp := RemoteRestorePoint{ID: 1, JobID: 8, BackupType: "full", StoragePath: "J/1"}
	if _, _, err := s.syncRestorePoint(c, src, destID, rj, rrp, jobID, 0, nil, storage.NewLocalAdapter(root)); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("sync during another download = %v", err)
	}
	if downloads != 0 {
		t.Fatalf("downloaded %d file(s)", downloads)
	}
	release()
	if _, _, err := s.syncRestorePoint(c, src, destID, rj, rrp, jobID, 0, nil, storage.NewLocalAdapter(root)); err != nil && strings.Contains(err.Error(), "in progress") {
		t.Fatalf("claim not released: %v", err)
	}
	if downloads != 1 {
		t.Fatalf("downloads = %d, want 1 after release", downloads)
	}
	rel, ok := s.claimPath(destID, "J/1")
	if !ok {
		t.Fatal("claim leaked by a finished sync")
	}
	rel()
}

// A sync whose restore points are refused is not a success: every refusal is
// failed work, so the source reports failed rather than success.
func TestSyncReportsRejectedRestorePointsAsFailed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	d := openTestDB(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"` + root + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	victim, err := d.CreateJob(db.Job{Name: "victim", BackupTypeChain: "full", StorageDestID: destID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateRestorePoint(db.RestorePoint{JobRunID: runFor(t, d, victim), JobID: victim, BackupType: "full", StoragePath: "victim/run"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/restore-points"):
			_, _ = w.Write([]byte(`[{"id":1,"job_id":1,"backup_type":"full","storage_path":"victim/run"}]`))
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			_, _ = w.Write([]byte(`[{"id":1,"name":"J"}]`))
		default:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer srv.Close()
	srcID, err := d.CreateReplicationSource(db.ReplicationSource{Name: "S", Type: "remote_vault", URL: srv.URL, StorageDestID: destID, Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewSyncer(d, nil)
	res, err := s.SyncSource(srcID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.JobsFailed != 1 || res.RestorePointsNew != 0 {
		t.Fatalf("result = %+v, want the job counted as failed", res)
	}
	src, err := d.GetReplicationSource(srcID)
	if err != nil {
		t.Fatal(err)
	}
	if src.LastSyncStatus != "failed" {
		t.Fatalf("status = %q, want failed", src.LastSyncStatus)
	}
}

// A local job left without a destination (deleted with its files kept) still
// owns its paths: reusing that storage for replication must not overwrite them.
func TestSyncRestorePointRefusesPathOfOrphanedLocalJob(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	d := openTestDB(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"` + root + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := d.CreateJob(db.Job{Name: "kept", BackupTypeChain: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateRestorePoint(db.RestorePoint{JobRunID: runFor(t, d, orphan), JobID: orphan, BackupType: "full", StoragePath: "kept/run"}); err != nil {
		t.Fatal(err)
	}
	downloads := 0
	c := newListingServer(t, map[string][]StorageFile{
		"kept/run/": {{Path: "kept/run/a.bin", Size: 3}},
		"kept/run":  {{Path: "kept/run/a.bin", Size: 3}},
		"J/own":     {{Path: "J/own/a.bin", Size: 3}},
	}, &downloads)
	src := db.ReplicationSource{ID: 1, Name: "S", StorageDestID: destID}
	rj := RemoteJob{ID: 8, StorageDestID: 3, Name: "J"}
	s := NewSyncer(d, nil)
	jobID, err := s.ensureLocalJob(src, destID, rj)
	if err != nil {
		t.Fatal(err)
	}
	sync := func(p string) error {
		_, _, err := s.syncRestorePoint(c, src, destID, rj, RemoteRestorePoint{ID: 1, JobID: 8, BackupType: "full", StoragePath: p}, jobID, 0, nil, storage.NewLocalAdapter(root))
		return err
	}
	for _, p := range []string{"kept/run", "kept/run/", "kept//run"} {
		if err := sync(p); !errors.Is(err, errPathRefused) {
			t.Fatalf("orphaned job path %q = %v, want refused", p, err)
		}
	}
	if downloads != 0 {
		t.Fatalf("downloaded %d file(s) over a kept backup", downloads)
	}
	// The replicating job's own path still retries.
	if _, err := d.CreateRestorePoint(db.RestorePoint{JobRunID: runFor(t, d, jobID), JobID: jobID, BackupType: "full", StoragePath: "J/own", SourceID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := sync("J/own"); err != nil && errors.Is(err, errPathRefused) {
		t.Fatalf("own path retry refused: %v", err)
	}
	if downloads != 1 {
		t.Fatalf("downloads = %d, want 1", downloads)
	}
}
