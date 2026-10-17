package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/memlimit"
)

func TestWarnUploadBudget(t *testing.T) {
	r, d := openRunLogRunnerDB(t)
	jobID, _ := d.CreateJob(db.Job{Name: "budget"})
	runID, _ := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "running"})

	warnings := func() int {
		t.Helper()
		entries, err := d.ListRunLogEntries(context.Background(), runID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range entries {
			if e.Level == runLogLevelWarn && strings.Contains(e.Message, "S3 upload buffers") {
				n++
			}
		}
		return n
	}
	s3 := db.StorageDestination{Name: "b2", Type: "s3", Config: `{"part_size_mb":64}`}

	// No limit: never warns.
	r.warnUploadBudget(runID, s3, 3)
	if n := warnings(); n != 0 {
		t.Fatalf("warned without a limit: %d", n)
	}

	memlimit.Apply(512)
	t.Cleanup(func() { memlimit.Apply(0) })

	r.warnUploadBudget(runID, s3, 1) // 320 MiB fits
	r.warnUploadBudget(runID, db.StorageDestination{Type: "local", Config: `{}`}, 16)
	if n := warnings(); n != 0 {
		t.Fatalf("warned within budget or for a non-S3 destination: %d", n)
	}

	r.warnUploadBudget(runID, db.StorageDestination{Name: "broken", Type: "s3", Config: `{`}, 16)
	if n := warnings(); n != 0 {
		t.Fatalf("warned for an unparseable S3 config: %d", n)
	}

	r.warnUploadBudget(runID, s3, 3) // 960 MiB > 512 MiB
	if n := warnings(); n != 1 {
		t.Fatalf("warnings = %d, want 1", n)
	}
}
