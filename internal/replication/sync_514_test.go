package replication

import (
	"encoding/json"
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

// newListingServer serves a fixed remote listing per prefix and "abc" for
// every download, counting the downloads it served.
func newListingServer(t *testing.T, listings map[string][]StorageFile, downloads *int) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/storage/3/list":
			files, ok := listings[r.URL.Query().Get("prefix")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(files)
		case "/api/v1/storage/3/files":
			if downloads != nil {
				*downloads++
			}
			_, _ = io.WriteString(w, "abc")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A peer must not be able to direct writes outside the requested restore
// point, or loop the sync forever (#514).
func TestDownloadDirConfinement514(t *testing.T) {
	t.Parallel()
	cases := map[string]map[string][]StorageFile{
		"sibling job":        {"job/run": {{Path: "other-job/run/f.bin", Size: 3}}},
		"parent traversal":   {"job/run": {{Path: "job/run/../../escape.bin", Size: 3}}},
		"absolute path":      {"job/run": {{Path: "/etc/passwd", Size: 3}}},
		"root alias":         {"job/run": {{Path: ".", IsDir: true}}},
		"self listing":       {"job/run": {{Path: "job/run", IsDir: true}}},
		"prefix lookalike":   {"job/run": {{Path: "job/run-evil/f.bin", Size: 3}}},
		"listing cycle":      {"job/run": {{Path: "job/run/a", IsDir: true}}, "job/run/a": {{Path: "job/run", IsDir: true}}},
		"escaping subfolder": {"job/run": {{Path: "job/run/a", IsDir: true}}, "job/run/a": {{Path: "job/x.bin", Size: 3}}},
	}
	for name, listing := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			downloads := 0
			c := newListingServer(t, listing, &downloads)
			s := NewSyncer(openTestDB(t), nil)
			dir := t.TempDir()
			if _, err := s.downloadDir(c, 3, "job/run", storage.NewLocalAdapter(dir)); err == nil {
				t.Fatal("downloadDir accepted a listing outside the restore point")
			}
			if downloads != 0 {
				t.Fatalf("downloaded %d file(s) from an out-of-prefix listing", downloads)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatalf("files were written: %v", entries)
			}
		})
	}
}

func TestDownloadDirDepthLimit514(t *testing.T) {
	t.Parallel()
	listings := map[string][]StorageFile{}
	cur := "job/run"
	for range maxDownloadDepth + 2 {
		next := cur + "/d"
		listings[cur] = []StorageFile{{Path: next, IsDir: true}}
		cur = next
	}
	c := newListingServer(t, listings, nil)
	s := NewSyncer(openTestDB(t), nil)
	if _, err := s.downloadDir(c, 3, "job/run", storage.NewLocalAdapter(t.TempDir())); err == nil || !strings.Contains(err.Error(), "deeper") {
		t.Fatalf("want depth-limit error, got %v", err)
	}
}

// A remote restore point whose storage path is not <job>/<run> is skipped:
// nothing is downloaded and no local row is created for it.
func TestSyncRestorePointRejectsUnmanagedPaths514(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"", ".", "/", "job", "/abs/run", "../up/run", "job/../.."} {
		t.Run(p, func(t *testing.T) {
			t.Parallel()
			downloads := 0
			c := newListingServer(t, map[string][]StorageFile{p: {{Path: "f.bin", Size: 3}}, ".": {{Path: "f.bin", Size: 3}}}, &downloads)
			d := openTestDB(t)
			destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
			if err != nil {
				t.Fatal(err)
			}
			src := db.ReplicationSource{ID: 1, Name: "S", StorageDestID: destID}
			rj := RemoteJob{ID: 8, StorageDestID: 3, Name: "J"}
			s := NewSyncer(d, nil)
			jobID, err := s.ensureLocalJob(src, rj)
			if err != nil {
				t.Fatal(err)
			}
			rrp := RemoteRestorePoint{ID: 1, JobID: 8, BackupType: "full", StoragePath: p}
			if _, err := s.syncRestorePoint(c, src, rj, rrp, jobID, storage.NewLocalAdapter(t.TempDir())); err == nil {
				t.Fatal("syncRestorePoint accepted an unmanaged storage path")
			}
			if downloads != 0 {
				t.Fatalf("downloaded %d file(s)", downloads)
			}
			rps, _ := d.ListRestorePoints(jobID)
			if len(rps) != 0 {
				t.Fatalf("created %d restore point row(s)", len(rps))
			}
		})
	}
}

// A local adapter with an unsafe root refuses to write anything.
func TestDownloadDirUnsafeLocalRoot514(t *testing.T) {
	t.Parallel()
	c := newListingServer(t, map[string][]StorageFile{"job/run": {{Path: "job/run/f.bin", Size: 3}}}, nil)
	s := NewSyncer(openTestDB(t), nil)
	if _, err := s.downloadDir(c, 3, "job/run", storage.NewLocalAdapter("")); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("want root validation error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(".", "job")); !os.IsNotExist(err) {
		t.Fatalf("a relative root wrote into the working directory: %v", err)
	}
}

// A peer path that differs only in form ("job/run/") matches the stored,
// normalised row and is not downloaded again.
func TestSyncJobMatchesNormalisedPaths514(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/jobs/8/restore-points") {
			_ = json.NewEncoder(w).Encode([]RemoteRestorePoint{{ID: 1, JobID: 8, BackupType: "full", StoragePath: "./job/run/"}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	d := openTestDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
	src := db.ReplicationSource{ID: 1, Name: "S", StorageDestID: destID}
	rj := RemoteJob{ID: 8, Name: "J"}
	s := NewSyncer(d, nil)
	jobID, err := s.ensureLocalJob(src, rj)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	if _, err := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "job/run"}); err != nil {
		t.Fatal(err)
	}
	n, _, err := s.syncJob(c, src, rj, storage.NewLocalAdapter(t.TempDir()))
	if err != nil || n != 0 {
		t.Fatalf("syncJob = %d, %v; want the existing point to be skipped", n, err)
	}
}
