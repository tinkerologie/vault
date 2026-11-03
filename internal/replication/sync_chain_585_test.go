package replication

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/runner"
	"github.com/tinkerologie/vault/internal/storage"
)

// servePeer answers the replication API from a peer's real database and local
// storage, the way the peer's handlers would.
func servePeer(t *testing.T, peer *db.DB, peerStorage string, destID int64) *httptest.Server {
	t.Helper()
	adapter := storage.NewLocalAdapter(peerStorage)
	idFrom := func(p, prefix, suffix string) int64 {
		id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(p, prefix), suffix), 10, 64)
		return id
	}
	writeJSON := func(w http.ResponseWriter, v any, err error) {
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(v)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/api/v1/health":
			writeJSON(w, HealthResponse{Status: "ok"}, nil)
		case p == "/api/v1/jobs":
			jobs, err := peer.ListJobs()
			out := make([]RemoteJob, 0, len(jobs))
			for _, j := range jobs {
				out = append(out, RemoteJob{ID: j.ID, Name: j.Name, BackupTypeChain: j.BackupTypeChain, StorageDestID: destID})
			}
			writeJSON(w, out, err)
		case strings.HasSuffix(p, "/restore-points"):
			rps, err := peer.ListRestorePoints(idFrom(p, "/api/v1/jobs/", "/restore-points"))
			writeJSON(w, rps, err)
		case strings.HasSuffix(p, "/history"):
			runs, err := peer.GetJobRuns(idFrom(p, "/api/v1/jobs/", "/history"), maxPeerRuns)
			writeJSON(w, runs, err)
		case p == fmt.Sprintf("/api/v1/storage/%d/list", destID):
			files, err := adapter.List(r.URL.Query().Get("prefix"))
			writeJSON(w, files, err)
		case p == fmt.Sprintf("/api/v1/storage/%d/files", destID):
			rc, err := adapter.Read(r.URL.Query().Get("path"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			defer rc.Close()
			_, _ = io.Copy(w, rc)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A replicated incremental restores with its full backup's files: its parent
// is the local copy of the peer's full, and the replicated job names the
// destination the sync auto-selected, so the replica can open its files.
func TestReplicatedIncrementalRestoresChain585(t *testing.T) {
	// The peer: a real full backup, then an incremental with one new file.
	peerDB := openTestDB(t)
	peerStorage, source := t.TempDir(), t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": peerStorage})
	peerDest, err := peerDB.CreateStorageDestination(db.StorageDestination{Name: "peer", Type: "local", Config: string(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := peerDB.CreateJob(db.Job{Name: "Docs", StorageDestID: peerDest, BackupTypeChain: "incremental", Enabled: true, Compression: "none"})
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{"path": source})
	if _, err := peerDB.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(settings)}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "from-full.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	peerRunner := runner.New(peerDB, nil, nil)
	peerRunner.RunJob(jobID)
	if err := os.WriteFile(filepath.Join(source, "from-increment.txt"), []byte("later"), 0o644); err != nil {
		t.Fatal(err)
	}
	peerRunner.RunJob(jobID)
	peerRPs, err := peerDB.ListRestorePoints(jobID)
	if err != nil {
		t.Fatal(err)
	}
	peerInc, peerFull := byType(peerRPs)
	if len(peerRPs) != 2 || peerInc.ParentRestorePointID != peerFull.ID || peerFull.ID == 0 {
		t.Fatalf("peer restore points = %+v; want an incremental on a full", peerRPs)
	}

	// The replica: no destination named on the source, so the sync picks one.
	replica := openTestDB(t)
	replicaStorage := t.TempDir()
	cfg, _ = json.Marshal(map[string]string{"path": replicaStorage})
	replicaDest, err := replica.CreateStorageDestination(db.StorageDestination{Name: "replica", Type: "local", Config: string(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	srv := servePeer(t, peerDB, peerStorage, peerDest)
	srcID, err := replica.CreateReplicationSource(db.ReplicationSource{Name: "Peer", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewSyncer(replica, nil).SyncSource(srcID, nil)
	if err != nil || res.RestorePointsNew != 2 || res.JobsFailed != 0 {
		t.Fatalf("SyncSource = %+v, %v; want both points", res, err)
	}

	src, err := replica.GetReplicationSource(srcID)
	if err != nil || src.StorageDestID != replicaDest {
		t.Errorf("source destination = %d, %v; want the auto-selected %d", src.StorageDestID, err, replicaDest)
	}
	job, err := replica.GetJobByName("[Peer] Docs")
	if err != nil {
		t.Fatal(err)
	}
	if job.StorageDestID != replicaDest {
		t.Errorf("replicated job destination = %d, want %d", job.StorageDestID, replicaDest)
	}
	rps, err := replica.ListRestorePoints(job.ID)
	if err != nil || len(rps) != 2 {
		t.Fatalf("replica restore points = %+v, %v", rps, err)
	}
	inc, full := byType(rps)
	if inc.ID == 0 || full.ID == 0 || inc.ParentRestorePointID != full.ID || full.ParentRestorePointID != 0 {
		t.Fatalf("replica chain = incremental %+v on %+v; want it linked to the local full", inc, full)
	}
	for _, rp := range rps {
		if run, err := replica.GetJobRun(rp.JobRunID); err != nil || run.Status != "success" {
			t.Errorf("run of %s = %+v, %v; want the peer's clean outcome", rp.BackupType, run, err)
		}
	}

	restoreDir := t.TempDir()
	if err := runner.New(replica, nil, nil).RestoreItem(inc, "src", "folder", restoreDir, ""); err != nil {
		t.Fatalf("restore replicated incremental: %v", err)
	}
	for name, want := range map[string]string{"from-full.txt": "base", "from-increment.txt": "later"} {
		if got, err := os.ReadFile(filepath.Join(restoreDir, name)); err != nil || string(got) != want {
			t.Errorf("restored %s = %q, %v; want %q", name, got, err, want)
		}
	}
}

// byType returns the incremental and the full restore point of a two-point chain.
func byType(rps []db.RestorePoint) (inc, full db.RestorePoint) {
	for _, rp := range rps {
		switch rp.BackupType {
		case "incremental":
			inc = rp
		case "full":
			full = rp
		}
	}
	return inc, full
}
