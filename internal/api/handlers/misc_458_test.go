package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/release"
)

// failingWriter458 is a ResponseWriter whose body writes always fail, used to
// drive the JSON encode-error logging branches.
type failingWriter458 struct {
	header http.Header
	status int
}

func newFailingWriter458() *failingWriter458 { return &failingWriter458{header: http.Header{}} }

func (f *failingWriter458) Header() http.Header         { return f.header }
func (f *failingWriter458) WriteHeader(code int)        { f.status = code }
func (f *failingWriter458) Write(p []byte) (int, error) { return 0, errors.New("client went away") }

func TestRespondJSON458_EncodeError(t *testing.T) {
	t.Parallel()
	w := newFailingWriter458()
	respondJSON(w, http.StatusTeapot, map[string]string{"k": "v"})
	if w.status != http.StatusTeapot {
		t.Errorf("status = %d", w.status)
	}
	if got := w.header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
}

type fixedLatest458 struct{ rel *release.LatestRelease }

func (f fixedLatest458) Latest(context.Context) (*release.LatestRelease, error) { return f.rel, nil }

func TestRelease458(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	for i := 60; i > 0; i-- {
		fmt.Fprintf(&b, "## [1.0.%d] - 2026-01-01\n\n### Added\n\n- item %d\n\n", i, i)
	}
	h := NewReleaseHandler(b.String(), fixedLatest458{rel: &release.LatestRelease{}})

	w := httptest.NewRecorder()
	h.Changelog(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	wantStatus458(t, w, http.StatusOK)
	if n := strings.Count(w.Body.String(), `"version"`); n != 50 {
		t.Errorf("releases = %d, want capped at 50", n)
	}

	// Encode failures are logged, not panicked on.
	h.Changelog(newFailingWriter458(), httptest.NewRequest(http.MethodGet, "/x", nil))
	h.Latest(newFailingWriter458(), httptest.NewRequest(http.MethodGet, "/x", nil))
}

func TestHistory458(t *testing.T) {
	t.Parallel()

	t.Run("empty list is an array", func(t *testing.T) {
		t.Parallel()
		h := NewHistoryHandler(newTestDB(t))
		w := httptest.NewRecorder()
		h.List(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		wantStatus458(t, w, http.StatusOK)
		if strings.TrimSpace(w.Body.String()) != "[]" {
			t.Errorf("body = %q", w.Body.String())
		}
	})

	t.Run("list error", func(t *testing.T) {
		t.Parallel()
		d := newTestDB(t)
		breakTable458(t, d, "job_runs")
		w := httptest.NewRecorder()
		NewHistoryHandler(d).List(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("trend jobs error", func(t *testing.T) {
		t.Parallel()
		d := newTestDB(t)
		breakTable458(t, d, "jobs")
		w := httptest.NewRecorder()
		NewHistoryHandler(d).Trend(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	for _, table := range []string{"job_items", "job_runs"} {
		t.Run("trend tolerates "+table+" error", func(t *testing.T) {
			t.Parallel()
			d := newTestDB(t)
			seedJob(t, d)
			breakTable458(t, d, table)
			w := httptest.NewRecorder()
			NewHistoryHandler(d).Trend(w, httptest.NewRequest(http.MethodGet, "/x?period=7d", nil))
			wantStatus458(t, w, http.StatusOK)
		})
	}
}

func TestHistoryHelpers458(t *testing.T) {
	t.Parallel()
	// 2026-10-15 is a Thursday; the week bucket snaps to Monday 2026-10-12.
	ts := time.Date(2026, 10, 15, 13, 0, 0, 0, time.UTC)
	if got := bucketKey(ts, "week"); !got.Equal(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("week bucket = %v", got)
	}
	out := bucketTrend([]trendRun{{Start: ts, Size: 5}}, "day")
	if len(out) != 1 || out[0].Categories["other"] != 5 {
		t.Errorf("bucketTrend = %+v", out)
	}
	for in, want := range map[string]string{"files": "folders", "boot": "flash", "weird": "other", "libvirt": "vms"} {
		if got := classifyItemType(in); got != want {
			t.Errorf("classifyItemType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunLog458(t *testing.T) {
	t.Parallel()
	d := newTestDB(t)
	jobID, err := d.CreateJob(db.Job{Name: "rl458"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "running", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"a", "b", "c"} {
		if _, err := d.AppendRunLog(context.Background(), db.RunLogEntry{RunID: runID, Level: "info", Message: m}); err != nil {
			t.Fatal(err)
		}
	}
	r := chi.NewRouter()
	r.Get("/runs/{runId}/logs", NewRunLogHandler(d).List)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/runs/%d/logs?tail=true&limit=2", runID), nil))
	wantStatus458(t, w, http.StatusOK)
	if strings.Contains(w.Body.String(), `"message":"a"`) || !strings.Contains(w.Body.String(), `"message":"c"`) {
		t.Errorf("tail body = %s", w.Body.String())
	}

	breakTable458(t, d, "run_log_entries")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/runs/%d/logs", runID), nil))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestHealthSummary458_TolerantOfTableErrors(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"job_items", "restore_points", "job_runs"} {
		t.Run(table, func(t *testing.T) {
			t.Parallel()
			d := newTestDB(t)
			jobID := seedJob(t, d)
			seedJobItem(t, d, jobID, "container", "plex")
			breakTable458(t, d, table)
			w := httptest.NewRecorder()
			NewHealthHandler(d).Summary(w, httptest.NewRequest(http.MethodGet, "/x", nil))
			wantStatus458(t, w, http.StatusOK)
		})
	}
}

func TestReplication458(t *testing.T) {
	t.Parallel()

	t.Run("update write error", func(t *testing.T) {
		t.Parallel()
		h, d := setupReplicationTest(t)
		id, err := d.CreateReplicationSource(db.ReplicationSource{Name: "r458", Type: "remote_vault", URL: "http://127.0.0.1:1", Schedule: "0 3 * * *"})
		if err != nil {
			t.Fatal(err)
		}
		failWrites458(t, d, "replication_sources", "UPDATE")
		w := httptest.NewRecorder()
		h.Update(w, withURLParam(httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"name":"r2","url":"http://127.0.0.1:2"}`)), "id", idStr(id)))
		if w.Code < 400 {
			t.Fatalf("status = %d; body %s", w.Code, w.Body.String())
		}
	})

	t.Run("test connection bad id", func(t *testing.T) {
		t.Parallel()
		h, _ := setupReplicationTest(t)
		w := httptest.NewRecorder()
		h.TestConnection(w, withURLParam(httptest.NewRequest(http.MethodPost, "/x", nil), "id", "bad"))
		wantStatus458(t, w, http.StatusBadRequest)
	})

	t.Run("test connection stored url invalid", func(t *testing.T) {
		t.Parallel()
		h, d := setupReplicationTest(t)
		id, err := d.CreateReplicationSource(db.ReplicationSource{Name: "bad-url", Type: "remote_vault", URL: "ftp://nope", Schedule: "0 3 * * *"})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.TestConnection(w, withURLParam(httptest.NewRequest(http.MethodPost, "/x", nil), "id", idStr(id)))
		wantStatus458(t, w, http.StatusBadRequest)
		if !strings.Contains(w.Body.String(), "invalid url") {
			t.Errorf("body = %s", w.Body.String())
		}
	})
}

func TestBrowse458(t *testing.T) {
	t.Parallel()

	t.Run("merge extra roots dedupes against existing", func(t *testing.T) {
		t.Parallel()
		h := NewBrowseHandler()
		h.mergeExtraRoots([]ZFSMountInfo{{Mountpoint: "/mnt/tank"}})
		h.mergeExtraRoots([]ZFSMountInfo{{Mountpoint: "/mnt/tank/"}, {Mountpoint: "/mnt/pool"}, {Mountpoint: "relative"}, {Mountpoint: "/"}})
		if len(h.extraAllowedRoots) != 2 {
			t.Errorf("extra roots = %v", h.extraAllowedRoots)
		}
	})

	t.Run("discover roots with zfs lister", func(t *testing.T) {
		t.Parallel()
		h := NewBrowseHandler()
		h.SetZFSLister(&mockZFSLister{mounts: []ZFSMountInfo{
			{Name: "tank", Mountpoint: "/mnt/tank458"},
			{Name: "dup", Mountpoint: "/mnt/tank458"},
		}})
		roots := h.discoverRoots(true)
		seen := map[string]int{}
		for _, r := range roots {
			seen[r.Path]++
			if seen[r.Path] > 1 {
				t.Errorf("duplicate root %q", r.Path)
			}
		}
	})

	t.Run("list entries sorts dirs then names", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		for _, n := range []string{"b", "a"} {
			if err := os.Mkdir(filepath.Join(dir, n), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for _, n := range []string{"z.txt", "y.txt", ".hidden"} {
			if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		got, err := NewBrowseHandler().listEntries(dir, true)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range got {
			names = append(names, e.Name)
		}
		if strings.Join(names, ",") != "a,b,y.txt,z.txt" {
			t.Errorf("order = %v", names)
		}
	})
}
