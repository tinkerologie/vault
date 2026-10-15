package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/storage"
)

func TestRedactStorageError(t *testing.T) {
	cases := []struct {
		in, mustNot, must string
	}{
		{`Get "https://admin:s3cr3t@s3.example.com/bucket/vault/job/secret-path/data.tar.zst": connection reset`, "s3cr3t", "https://"},
		{`Put "https://s3.example.com/bucket/private/key/name?X-Amz-Signature=abc": EOF`, "private/key", "s3.example.com"},
		{`dial tcp 192.0.2.10:22: i/o timeout`, "", "i/o timeout"},
	}
	for _, tc := range cases {
		got := redactStorageError(tc.in)
		if tc.mustNot != "" && strings.Contains(got, tc.mustNot) {
			t.Errorf("redactStorageError(%q) = %q, still contains %q", tc.in, got, tc.mustNot)
		}
		if !strings.Contains(got, tc.must) {
			t.Errorf("redactStorageError(%q) = %q, want it to keep %q", tc.in, got, tc.must)
		}
	}
	if got := redactStorageError(`Get "https://admin:s3cr3t@s3.example.com/bucket/obj": reset`); got != `Get "https://[redacted]@s3.example.com/…": reset` {
		t.Errorf("redactStorageError = %q", got)
	}
}

func TestDiagEventMessage(t *testing.T) {
	cases := []struct {
		ev   storage.DiagEvent
		want string
	}{
		{storage.DiagEvent{Op: "ReadRange", Object: "data.tar.zst", Attempt: 2, MaxAttempts: 5, Err: "reset"}, "Transient storage error on ReadRange data.tar.zst (attempt 2/5), retrying: reset"},
		{storage.DiagEvent{Op: "TestConnection", Object: ".", Attempt: 1, MaxAttempts: 5, Err: "x"}, "Transient storage error on TestConnection (attempt 1/5), retrying: x"},
		{storage.DiagEvent{Op: "resume", Object: "a.bin", Attempt: 1, MaxAttempts: 5, Err: "EOF"}, "Storage stream for a.bin dropped (attempt 1/5), resuming: EOF"},
		{storage.DiagEvent{Op: "part", Object: "a.bin", Attempt: 3, MaxAttempts: 5, Err: "reset"}, "Download part of a.bin failed (attempt 3/5), restarting the part: reset"},
	}
	for _, tc := range cases {
		msg, data := diagEventMessage(tc.ev)
		if msg != tc.want {
			t.Errorf("diagEventMessage(%+v) = %q, want %q", tc.ev, msg, tc.want)
		}
		if data["attempt"] != tc.ev.Attempt || data["operation"] != tc.ev.Op {
			t.Errorf("data = %v, missing attempt/operation", data)
		}
	}
}

func TestAddDiagTotalsAndDelta(t *testing.T) {
	data := map[string]any{}
	if msg := addDiagTotals("Backup finished", data, storage.DiagSnapshot{}); msg != "Backup finished" || len(data) != 0 {
		t.Fatalf("clean run: %q %v, want unchanged", msg, data)
	}
	before := storage.DiagSnapshot{Retries: 1, BytesRead: 100}
	after := storage.DiagSnapshot{Retries: 3, TransientErrors: 4, Resumes: 1, PartRestarts: 1, BytesRead: 2148, RefetchedBytes: 10, SuppressedEvents: 2}
	d := diagDelta(before, after)
	if d.Retries != 2 || d.BytesRead != 2048 || d.Resumes != 1 || d.SuppressedEvents != 2 {
		t.Fatalf("delta = %+v", d)
	}
	msg := addDiagTotals("Restore finished", data, d)
	if !strings.Contains(msg, "read=2 KB") || !strings.Contains(msg, "retries=4, transient_errors=4") {
		t.Errorf("message = %q", msg)
	}
	if data["bytes_read"] != int64(2048) || data["refetched_bytes"] != int64(10) {
		t.Errorf("data = %v", data)
	}
}

func TestRestoredItemSize(t *testing.T) {
	recorded := map[string]int64{"src": 4 << 20}
	tally := &engine.RestoreTally{}
	tally.Add(2048)

	if size, label, ok := restoredItemSize(RestoreTarget{Name: "src", Type: "folder", FilePaths: []string{"small.bin"}}, tally, recorded); !ok || size != 2048 || label != "restored" {
		t.Errorf("selective = (%d, %q, %v), want measured 2048", size, label, ok)
	}
	if size, label, ok := restoredItemSize(RestoreTarget{Name: "src", Type: "folder"}, tally, recorded); !ok || size != 4<<20 || label != "recorded item size" {
		t.Errorf("whole item = (%d, %q, %v), want recorded size", size, label, ok)
	}
	if size, label, ok := restoredItemSize(RestoreTarget{Name: "src", Type: "container", FilePaths: []string{"x"}}, &engine.RestoreTally{}, recorded); !ok || size != 0 || label != "restored" {
		t.Errorf("selection matching no files = (%d, %q, %v), want measured 0", size, label, ok)
	}
	if size, label, ok := restoredItemSize(RestoreTarget{Name: "src", Type: "vm", FilePaths: []string{"x"}}, &engine.RestoreTally{}, recorded); !ok || size != 4<<20 || label != "recorded item size" {
		t.Errorf("type without a tally = (%d, %q, %v), want recorded size", size, label, ok)
	}
	if _, _, ok := restoredItemSize(RestoreTarget{Name: "other"}, nil, recorded); ok {
		t.Error("unknown item reported a size")
	}
}

func TestRunDiagWritesCappedWarnLines(t *testing.T) {
	r, d := openRunLogRunnerDB(t)
	jobID, _ := d.CreateJob(db.Job{Name: "diag"})
	runID, _ := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "running"})

	diag := r.newRunDiag(runID)
	// Every stream delivers one byte and then drops, so reading N bytes
	// produces N resume events through the real resumable reader.
	size := int64(storage.DefaultDiagEventCap + 3)
	rr := storage.NewResumableReader(storage.WithDiagStats(context.Background(), diag), &droppingAdapter{}, "vault/job/obj.bin", size, storage.RetryPolicy{MaxAttempts: 3})
	n, err := io.Copy(io.Discard, rr)
	if err != nil || n != size {
		t.Fatalf("read %d bytes, err %v; want %d", n, err, size)
	}
	if s := diag.Snapshot(); s.BytesRead != size || s.Resumes != size-1 {
		t.Fatalf("snapshot = %+v, want %d bytes and %d resumes", s, size, size-1)
	}
	entries, err := d.ListRunLogEntries(context.Background(), runID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var warns, suppressed int
	for _, e := range entries {
		if e.Level == runLogLevelWarn {
			warns++
		}
		if strings.Contains(e.Message, "Further storage retry events suppressed") {
			suppressed++
		}
	}
	if warns != storage.DefaultDiagEventCap+1 || suppressed != 1 {
		_ = runID
		t.Fatalf("warn lines = %d (suppressed notices %d), want %d events + 1 notice", warns, suppressed, storage.DefaultDiagEventCap)
	}
}

// droppingAdapter serves one byte per ReadRange and then a transient drop.
type droppingAdapter struct{ stubAdapter }

func (droppingAdapter) ReadRange(_ string, _, _ int64) (io.ReadCloser, error) {
	return io.NopCloser(io.MultiReader(strings.NewReader("x"), errReader{io.ErrUnexpectedEOF})), nil
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestRunPhaseLines(t *testing.T) {
	r, d := openRunLogRunnerDB(t)
	jobID, _ := d.CreateJob(db.Job{Name: "phase"})
	runID, _ := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "running"})

	r.beginPhase(runID, "src", "download").end(nil, map[string]any{"bytes_read": int64(10)})
	r.beginPhase(runID, "src", "extract").end(errors.New(`read "https://user:pw@s3.example.com/b/secret/obj": disk full`), nil)

	entries, _ := d.ListRunLogEntries(context.Background(), runID, 0, 10)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if !strings.HasPrefix(entries[0].Message, "Phase download completed for src in <1s") || entries[0].Level != runLogLevelInfo {
		t.Errorf("success line = %q (%s)", entries[0].Message, entries[0].Level)
	}
	if !strings.HasPrefix(entries[1].Message, "Phase extract failed for src after <1s") || entries[1].Level != runLogLevelError {
		t.Errorf("failure line = %q (%s)", entries[1].Message, entries[1].Level)
	}
	var data map[string]any
	_ = json.Unmarshal([]byte(entries[0].Data), &data)
	if data["phase"] != "download" || data["bytes_read"] != float64(10) {
		t.Errorf("data = %v", data)
	}
	if strings.Contains(entries[1].Data, "pw@") || strings.Contains(entries[1].Data, "secret/obj") {
		t.Errorf("phase error not redacted: %s", entries[1].Data)
	}
}

// TestRunDiagnostics_BackupAndSelectiveRestore drives a classic backup with
// verification and a one-file restore end to end (#455).
func TestRunDiagnostics_BackupAndSelectiveRestore(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	src := t.TempDir()
	small := []byte("small file payload")
	if err := os.WriteFile(filepath.Join(src, "small.txt"), small, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "big.bin"), make([]byte, 256<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	storageDir := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "diag-dest", Type: "local", Config: string(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "diag-job", StorageDestID: destID, BackupTypeChain: "full", Compression: "none", Encryption: "none", Enabled: true, VerifyBackup: true})
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{"path": src})
	if _, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(settings)}); err != nil {
		t.Fatal(err)
	}

	r.RunJob(jobID)
	runs, _ := d.GetJobRuns(jobID, 1)
	backupLog := runLogText(t, d, runs[0].ID)
	for _, want := range []string{
		`Run details: job="diag-job"`, "(local), items=1",
		"Phase stage completed for src", "Phase upload completed for src", "Phase verify completed for src",
		"checksum OK, read=",
	} {
		if !strings.Contains(backupLog, want) {
			t.Errorf("backup log missing %q:\n%s", want, backupLog)
		}
	}

	rps, _ := d.ListRestorePoints(jobID)
	dst := t.TempDir()
	r.RunRestore(rps[0], []RestoreTarget{{Name: "src", Type: "folder", FilePaths: []string{"small.txt"}}}, dst, "")
	restore := latestRestoreRun(t, d, jobID)
	if restore.SizeBytes != int64(len(small)) {
		t.Errorf("restore size = %d, want %d (measured, not the whole item)", restore.SizeBytes, len(small))
	}
	restoreLog := runLogText(t, d, restore.ID)
	for _, want := range []string{
		"Run details: job=", "restore_point_id=",
		"Phase download completed for src", "Phase extract completed for src",
		"restored=18 B (1 file(s))", `destination="diag-dest"`,
	} {
		if !strings.Contains(restoreLog, want) {
			t.Errorf("restore log missing %q:\n%s", want, restoreLog)
		}
	}
}

// TestRunDiagnostics_DedupPhases covers the dedup backup and restore phases.
func TestRunDiagnostics_DedupPhases(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)
	r.serverKey = testServerKey()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("dedup payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]string{"path": t.TempDir()})
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "dedup-diag", Type: "local", Config: string(cfg), DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	jobID, _ := d.CreateJob(db.Job{Name: "dedup-diag-job", StorageDestID: destID, BackupTypeChain: "full", Compression: "none", Encryption: "none", Enabled: true, VerifyBackup: true})
	settings, _ := json.Marshal(map[string]any{"path": src})
	_, _ = d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(settings)})

	r.RunJob(jobID)
	runs, _ := d.GetJobRuns(jobID, 1)
	backupLog := runLogText(t, d, runs[0].ID)
	for _, want := range []string{"Phase dedup store completed for src", "Phase verify completed for src"} {
		if !strings.Contains(backupLog, want) {
			t.Errorf("backup log missing %q:\n%s", want, backupLog)
		}
	}

	rps, _ := d.ListRestorePoints(jobID)
	r.RunRestore(rps[0], []RestoreTarget{{Name: "src", Type: "folder"}}, t.TempDir(), "")
	restoreLog := runLogText(t, d, latestRestoreRun(t, d, jobID).ID)
	for _, want := range []string{"Phase open repository completed for src", "Phase restore completed for src", "read="} {
		if !strings.Contains(restoreLog, want) {
			t.Errorf("restore log missing %q:\n%s", want, restoreLog)
		}
	}
}

func runLogText(t *testing.T, d *db.DB, runID int64) string {
	t.Helper()
	entries, err := d.ListRunLogEntries(context.Background(), runID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Message)
		b.WriteByte('\n')
	}
	return b.String()
}
