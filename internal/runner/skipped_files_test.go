package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/engine"
)

// TestMarkSkippedFiles covers the runner half of issue #393: files the engine
// could not archive intact must reach the per-item run entry, and a healthy
// backup must stay untouched.
func TestMarkSkippedFiles(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		result *engine.BackupResult
		want   []string
	}{
		{name: "nil result", result: nil},
		{name: "no meta at all", result: &engine.BackupResult{}},
		{
			name:   "meta without the key",
			result: &engine.BackupResult{Meta: map[string]any{engine.MetaUnchanged: true}},
		},
		{
			name:   "empty list is not a skip",
			result: &engine.BackupResult{Meta: map[string]any{engine.MetaSkippedFiles: []string{}}},
		},
		{
			name:   "in-process []string",
			result: &engine.BackupResult{Meta: map[string]any{engine.MetaSkippedFiles: []string{"config/a.txz"}}},
			want:   []string{"config/a.txz"},
		},
		{
			// A result that travelled through JSON decodes its Meta values as
			// []any; the warning must survive that trip.
			name:   "JSON-decoded []any",
			result: &engine.BackupResult{Meta: map[string]any{engine.MetaSkippedFiles: []any{"config/a.txz", "config/b.txz"}}},
			want:   []string{"config/a.txz", "config/b.txz"},
		},
		{
			name:   "non-string members are dropped",
			result: &engine.BackupResult{Meta: map[string]any{engine.MetaSkippedFiles: []any{"a", 7, ""}}},
			want:   []string{"a"},
		},
		{
			name:   "an unexpected value type is ignored",
			result: &engine.BackupResult{Meta: map[string]any{engine.MetaSkippedFiles: "a.txz"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resEntry := map[string]any{}
			got := markSkippedFiles(resEntry, tc.result)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("skipped = %v, want %v", got, tc.want)
			}
			_, recorded := resEntry["skipped_files"]
			if recorded != (len(tc.want) > 0) {
				t.Fatalf("resEntry recorded = %v, want %v", recorded, len(tc.want) > 0)
			}
		})
	}
}

// TestSkippedFilesMessage pins the run-log wording, including the cap that
// keeps a widely failing disk from flooding the run log.
func TestSkippedFilesMessage(t *testing.T) {
	t.Parallel()

	t.Run("names every file when there are few", func(t *testing.T) {
		msg := skippedFilesMessage("Flash Drive", "folder", []string{"a", "b"})
		if !strings.Contains(msg, "Skipped 2 unreadable file(s) in Flash Drive (folder)") {
			t.Errorf("unexpected message: %q", msg)
		}
		if !strings.Contains(msg, "a, b") {
			t.Errorf("message should name both files: %q", msg)
		}
		if strings.Contains(msg, "more") {
			t.Errorf("message should not claim more files: %q", msg)
		}
	})

	t.Run("caps the named files and counts the rest", func(t *testing.T) {
		msg := skippedFilesMessage("Flash Drive", "folder", []string{"a", "b", "c", "d", "e", "f", "g"})
		if !strings.Contains(msg, "Skipped 7 unreadable file(s)") {
			t.Errorf("unexpected message: %q", msg)
		}
		if !strings.Contains(msg, "and 2 more") {
			t.Errorf("message should count the unnamed files: %q", msg)
		}
		if strings.Contains(msg, "f, g") {
			t.Errorf("message should not name past the cap: %q", msg)
		}
	})
}

// TestRunJobReportsSkippedFilesAsPartial is the end-to-end half of #393: an
// item that succeeded but could not read every file must not be reported as a
// clean "completed" run, because that hides the gap from the operator who
// would only discover it at restore time.
func TestRunJobReportsSkippedFilesAsPartial(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000-mode file, so the unreadable-file case cannot be staged")
	}

	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "readable.txt"), []byte("fine"), 0o644); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(source, "locked.txt")
	if err := os.WriteFile(unreadable, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o644) })

	jobID, err := database.CreateJob(db.Job{
		Name: "skip-job", StorageDestID: dest.ID, BackupTypeChain: "full", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	settings, _ := json.Marshal(map[string]any{"path": source})
	if _, err := database.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(settings),
	}); err != nil {
		t.Fatalf("AddJobItem: %v", err)
	}

	r.RunJob(jobID)

	runs, err := database.GetJobRuns(jobID, 10)
	if err != nil {
		t.Fatalf("ListJobRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].Status != "partial" {
		t.Errorf("status = %q, want \"partial\" — an unreadable file must not read as a clean backup", runs[0].Status)
	}

	// The warning has to name the file, or the operator has no way to act.
	logs, err := database.TailRunLogEntries(context.Background(), runs[0].ID, 200)
	if err != nil {
		t.Fatalf("ListRunLogs: %v", err)
	}
	found := false
	for _, entry := range logs {
		if strings.Contains(entry.Message, "locked.txt") {
			found = true
		}
	}
	if !found {
		t.Error("no run-log entry named the unreadable file")
	}
}
