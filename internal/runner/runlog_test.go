package runner

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// openRunLogRunnerDB opens a fresh DB and a Runner wired to it with a nil
// hub — runLog must tolerate a nil hub (broadcast no-ops).
func openRunLogRunnerDB(t *testing.T) (*Runner, *db.DB) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return New(d, nil, nil), d
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		want string
	}{
		{name: "plain bytes", in: 512, want: "512 B"},
		{name: "two kib", in: 2048, want: "2.0 KiB"},
		{name: "fractional mib", in: 5*1024*1024 + 300*1024, want: "5.3 MiB"},
		{name: "one gib", in: 1024 * 1024 * 1024, want: "1.0 GiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := humanBytes(tt.in); got != tt.want {
				t.Fatalf("humanBytes(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRunSummaryMessage(t *testing.T) {
	tests := []struct {
		name       string
		kind       string
		jobName    string
		status     string
		done       int
		failed     int
		total      int
		sizeBytes  int64
		duration   time.Duration
		wantLevel  string
		wantPct    float64
		wantSubstr []string
	}{
		{
			name: "completed run is info with full percent", kind: "Backup", status: "completed",
			done: 3, failed: 0, total: 3, sizeBytes: 2048, duration: 90 * time.Second,
			wantLevel: runLogLevelInfo, wantPct: 100,
			wantSubstr: []string{"Backup finished", "status=completed", "items=3/3", "2 KB", "failed=0"},
		},
		{
			name: "partial run is warn with partial percent", kind: "Backup", status: "partial",
			done: 1, failed: 1, total: 4, sizeBytes: 1024, duration: time.Minute,
			wantLevel: runLogLevelWarn, wantPct: 25,
			wantSubstr: []string{"status=partial", "failed=1"},
		},
		{
			name: "failed run is error", kind: "Restore", status: "failed",
			done: 0, failed: 2, total: 2, sizeBytes: 0, duration: 5 * time.Second,
			wantLevel: runLogLevelError, wantPct: 0,
			wantSubstr: []string{"Restore finished", "status=failed"},
		},
		{
			name: "cancelled run is warn", kind: "Backup", status: "cancelled",
			done: 0, failed: 0, total: 0, sizeBytes: 0, duration: 2 * time.Second,
			wantLevel: runLogLevelWarn, wantPct: 0,
			wantSubstr: []string{"status=cancelled"},
		},
		{
			name: "completed run includes job name", kind: "Backup", jobName: "plugs", status: "completed",
			done: 1, failed: 0, total: 1, sizeBytes: 128, duration: time.Second,
			wantLevel: runLogLevelInfo, wantPct: 100,
			wantSubstr: []string{"Backup finished, job=\"plugs\"", "status=completed"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, msg, data := runSummaryMessage(tt.kind, tt.jobName, tt.status, tt.done, tt.failed, tt.total, tt.sizeBytes, tt.duration)
			if level != tt.wantLevel {
				t.Fatalf("level = %q, want %q", level, tt.wantLevel)
			}
			for _, sub := range tt.wantSubstr {
				if !stringsContains(msg, sub) {
					t.Fatalf("message %q missing substring %q", msg, sub)
				}
			}
			if tt.jobName != "" && data["job_name"] != tt.jobName {
				t.Fatalf("job_name = %v, want %q", data["job_name"], tt.jobName)
			}
			if data["percent_success"] != tt.wantPct {
				t.Fatalf("percent_success = %v, want %v", data["percent_success"], tt.wantPct)
			}
			if data["items_total"] != tt.total || data["items_done"] != tt.done || data["items_failed"] != tt.failed {
				t.Fatalf("item counters in data wrong: %+v", data)
			}
		})
	}
}

func TestRunLogPersistsEntry(t *testing.T) {
	tests := []struct {
		name     string
		data     map[string]any
		wantData string
	}{
		{name: "nil data stores empty string", data: nil, wantData: ""},
		{name: "map data stores json", data: map[string]any{"item_name": "web"}, wantData: `{"item_name":"web"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, d := openRunLogRunnerDB(t)
			jobID, err := d.CreateJob(db.Job{Name: "runlog-runner-" + tt.name})
			if err != nil {
				t.Fatalf("create job: %v", err)
			}
			runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "running", BackupType: "full"})
			if err != nil {
				t.Fatalf("create run: %v", err)
			}

			r.runLog(runID, runLogLevelInfo, "hello", tt.data)

			got, err := d.ListRunLogEntries(context.Background(), runID, 0, 100)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d entries, want 1 (nil hub must not break the write)", len(got))
			}
			if got[0].Message != "hello" || got[0].Level != runLogLevelInfo {
				t.Fatalf("entry = %+v", got[0])
			}
			if got[0].Data != tt.wantData {
				t.Fatalf("data = %q, want %q", got[0].Data, tt.wantData)
			}
		})
	}
}

func TestRunLogInvalidRunIDIgnored(t *testing.T) {
	tests := []struct {
		name  string
		runID int64
	}{
		{name: "zero run id means no run row yet", runID: 0},
		{name: "negative run id fails the insert without panicking", runID: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, d := openRunLogRunnerDB(t)
			// Must not panic or abort — invalid run ids are silently ignored
			// (a negative id trips the run_id foreign key, which runLog
			// treats as non-fatal, same as any other append failure).
			r.runLog(tt.runID, runLogLevelInfo, "noop", nil)
			got, err := d.ListRunLogEntries(context.Background(), tt.runID, 0, 100)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("got %d entries for run id %d, want 0", len(got), tt.runID)
			}
		})
	}
}

func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
