package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

func TestScaleRestorePhaseProgressBoundsC458(t *testing.T) {
	cases := []struct {
		name                   string
		start, end             int
		completed, total, want int64
	}{
		{"empty phase", 40, 40, 1, 10, 40},
		{"inverted phase", 50, 40, 1, 10, 40},
		{"unknown total", 0, 40, 5, 0, 40},
		{"negative completed", 0, 40, -5, 10, 0},
		{"over-complete clamps", 0, 40, 50, 10, 40},
		{"tiny progress nudges past start", 0, 40, 1, 1000, 1},
		{"halfway", 0, 40, 5, 10, 20},
	}
	for _, tc := range cases {
		if got := scaleRestorePhaseProgress(tc.start, tc.end, tc.completed, tc.total); int64(got) != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestRunRestorePartialC458(t *testing.T) {
	r, d, jobID, rp := seedTwoItemBackup(t)
	// One real target and one the restore point never captured: the run
	// finishes partial.
	r.RunRestore(rp, []RestoreTarget{
		{Name: "small", Type: "folder"},
		{Name: "c458-missing", Type: "bogus-type"},
	}, t.TempDir(), "")
	run := latestRestoreRun(t, d, jobID)
	if run.Status != "partial" {
		t.Fatalf("status = %q, want partial (log: %s)", run.Status, run.Log)
	}
}

func TestRestoreSinglePointChunkedBogusAdapterC458(t *testing.T) {
	r, d, _ := setupTestRunner(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "c458-bogus-chunked", Type: "bogus", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "c458-bogus-chunked", StorageDestID: destID})
	if err != nil {
		t.Fatal(err)
	}
	rep := restoreProgressReporter{ItemName: "x", ItemType: "folder", ItemsTotal: 1}
	err = r.restoreSinglePointChunked(context.Background(), db.RestorePoint{JobID: jobID}, [32]byte{1}, "x", "folder", t.TempDir(), nil, false, rep)
	if err == nil || !strings.Contains(err.Error(), "creating storage adapter") {
		t.Fatalf("bogus adapter: %v", err)
	}
}
