package replication

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/storage"
)

func TestReplication458_NormalizeBaseURLErrors(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"http://[::1":           "parse url",
		"http://user:pw@host":   "credentials",
		"http://":               "host is required",
		"http://host/?q=1":      "query strings",
		"http://host/#fragment": "query strings",
	} {
		if _, err := NormalizeBaseURL(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("NormalizeBaseURL(%q) = %v, want %q", in, err, want)
		}
	}
}

func TestReplication458_RequestBuildAndTransportErrors(t *testing.T) {
	t.Parallel()
	bad := &Client{baseURL: "http://[::1", httpClient: http.DefaultClient}
	if _, err := bad.DownloadFile(1, "x"); err == nil || !strings.Contains(err.Error(), "build URL") {
		t.Errorf("bad base URL: %v", err)
	}

	srv := httptest.NewServer(http.NotFoundHandler())
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if _, err := c.DownloadFile(1, "x"); err == nil || !strings.Contains(err.Error(), "download file") {
		t.Errorf("closed server: %v", err)
	}
}

// failWriteAdapter is a local adapter whose writes fail.
type failWriteAdapter struct{ storage.Adapter }

func (failWriteAdapter) Write(string, io.Reader) error { return errors.New("disk full") }

func newTreeServer(t *testing.T, downloadStatus int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/storage/3/list" && r.URL.Query().Get("prefix") == "root":
			_ = json.NewEncoder(w).Encode([]StorageFile{{Name: "sub", Path: "root/sub", IsDir: true}})
		case r.URL.Path == "/api/v1/storage/3/list" && r.URL.Query().Get("prefix") == "root/sub":
			_ = json.NewEncoder(w).Encode([]StorageFile{{Name: "f.bin", Path: "root/sub/f.bin", Size: 3}})
		case r.URL.Path == "/api/v1/storage/3/files":
			w.WriteHeader(downloadStatus)
			_, _ = io.WriteString(w, "abc")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReplication458_DownloadDir(t *testing.T) {
	t.Parallel()
	s := NewSyncer(openTestDB(t), nil)

	srv := newTreeServer(t, http.StatusOK)
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	n, err := s.downloadDir(c, 3, "root", storage.NewLocalAdapter(dir))
	if err != nil || n != 3 {
		t.Fatalf("downloadDir = %d, %v", n, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "root", "sub", "f.bin")); err != nil || string(b) != "abc" {
		t.Fatalf("downloaded = %q, %v", b, err)
	}

	if _, err := s.downloadDir(c, 3, "root", failWriteAdapter{storage.NewLocalAdapter(dir)}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("write failure: %v", err)
	}

	srv500 := newTreeServer(t, http.StatusInternalServerError)
	c500, err := NewClient(srv500.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.downloadDir(c500, 3, "root", storage.NewLocalAdapter(dir)); err == nil || !strings.Contains(err.Error(), "download") {
		t.Errorf("download failure: %v", err)
	}
}

func TestReplication458_SyncSourceMissingDestination(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
		case "/api/v1/jobs":
			_ = json.NewEncoder(w).Encode([]RemoteJob{})
		}
	}))
	defer srv.Close()
	d := openTestDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{}`})
	id, err := d.CreateReplicationSource(db.ReplicationSource{Name: "src", URL: srv.URL, StorageDestID: destID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`ALTER TABLE storage_destinations RENAME TO sd_hidden`); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSyncer(d, nil).SyncSource(id, nil); err == nil || !strings.Contains(err.Error(), "get storage destination") {
		t.Errorf("SyncSource: %v", err)
	}
}

func TestReplication458_SyncJobLocalFailuresAndSkips(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/jobs/8/restore-points") {
			_ = json.NewEncoder(w).Encode([]RemoteRestorePoint{{ID: 1, JobID: 8, BackupType: "full", StoragePath: "already/here"}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	d := openTestDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
	src := db.ReplicationSource{ID: 1, Name: "S", StorageDestID: destID}
	rj := RemoteJob{ID: 8, Name: "J"}
	s := NewSyncer(d, nil)
	local := storage.NewLocalAdapter(t.TempDir())

	// A remote point whose storage path already exists locally is skipped.
	jobID, err := s.ensureLocalJob(src, rj)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "already/here"}); err != nil {
		t.Fatal(err)
	}
	n, _, err := s.syncJob(c, src, rj, local)
	if err != nil || n != 0 {
		t.Fatalf("syncJob with existing point = %d, %v", n, err)
	}

	if _, err := d.Exec(`ALTER TABLE restore_points RENAME TO rp_hidden`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.syncJob(c, src, rj, local); err == nil || !strings.Contains(err.Error(), "list local restore points") {
		t.Errorf("local listing failure: %v", err)
	}
	if _, err := d.Exec(`ALTER TABLE jobs RENAME TO jobs_hidden`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.syncJob(c, src, rj, local); err == nil || !strings.Contains(err.Error(), "ensure local job") {
		t.Errorf("ensure job failure: %v", err)
	}
}
