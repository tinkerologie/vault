package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newFakeClock() *fakeClock               { return &fakeClock{t: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)} }
func transferring(done, total int64) engine.ProgressDetail {
	return engine.ProgressDetail{Phase: engine.PhaseTransferring, BytesDone: done, BytesTotal: total}
}

func TestProgressTracker_HighWaterOnRetry(t *testing.T) {
	clk := newFakeClock()
	tr := newProgressTracker(clk.now)
	tr.update("vm", transferring(600, 1000))
	clk.advance(time.Second)
	snap, _, _ := tr.update("vm", transferring(100, 1000)) // a retried attempt restarted its count
	if snap.BytesDone != 600 || snap.Percent != 60 {
		t.Fatalf("after retry = %d bytes / %d%%, want 600 / 60 (never backward)", snap.BytesDone, snap.Percent)
	}
}

func TestProgressTracker_ResetOnPhaseOrItemChange(t *testing.T) {
	clk := newFakeClock()
	tr := newProgressTracker(clk.now)
	tr.update("a", transferring(900, 1000))
	snap, changed, _ := tr.update("a", engine.ProgressDetail{Phase: engine.PhaseVerifying, BytesTotal: 1000})
	if !changed || snap.BytesDone != 0 || snap.Phase != engine.PhaseVerifying {
		t.Fatalf("phase change = %+v changed=%v, want reset counters", snap, changed)
	}
	clk.advance(time.Second)
	snap, changed, _ = tr.update("b", engine.ProgressDetail{Phase: engine.PhaseVerifying})
	if !changed || snap.BytesTotal != 0 || !snap.PhaseStartedAt.Equal(clk.t) {
		t.Fatalf("item change = %+v changed=%v, want fresh state", snap, changed)
	}
	if _, changed, _ := tr.update("b", engine.ProgressDetail{Phase: engine.PhaseVerifying}); changed {
		t.Fatal("same phase and item reported as changed")
	}
}

func TestProgressTracker_DoneBeyondTotalCapsAt99(t *testing.T) {
	tr := newProgressTracker(newFakeClock().now)
	snap, _, _ := tr.update("x", transferring(1200, 1000))
	if snap.BytesTotal != 1200 || snap.Percent != 99 || !snap.Determinate {
		t.Fatalf("overrun = %+v, want total raised to 1200 and 99%%", snap)
	}
}

func TestProgressTracker_Indeterminate(t *testing.T) {
	tr := newProgressTracker(newFakeClock().now)
	snap, _, _ := tr.update("x", engine.ProgressDetail{Phase: engine.PhaseScanning})
	if snap.Determinate || snap.Percent != 0 || snap.ETASeconds != 0 {
		t.Fatalf("no total = %+v, want indeterminate", snap)
	}
}

func TestProgressTracker_RateAndETAGate(t *testing.T) {
	clk := newFakeClock()
	tr := newProgressTracker(clk.now)
	const total = 100 << 20
	var snap progressSnapshot
	// 1 MiB/s for 10s: a rate exists, but the ETA is withheld (too early).
	for i := int64(0); i <= 10; i++ {
		snap, _, _ = tr.update("x", transferring(i<<20, total))
		clk.advance(time.Second)
	}
	if snap.Rate < 0.9*(1<<20) || snap.Rate > 1.1*(1<<20) {
		t.Fatalf("rate = %.0f, want ~1 MiB/s", snap.Rate)
	}
	if snap.ETASeconds != 0 {
		t.Fatalf("ETA after 10s = %d, want withheld", snap.ETASeconds)
	}
	for i := int64(11); i <= 40; i++ {
		snap, _, _ = tr.update("x", transferring(i<<20, total))
		clk.advance(time.Second)
	}
	if snap.ETASeconds < 55 || snap.ETASeconds > 65 {
		t.Fatalf("ETA after 40s at 1 MiB/s with 60 MiB left = %d, want ~60", snap.ETASeconds)
	}
	// Sub-interval updates do not resample the rate.
	snap, _, _ = tr.update("x", transferring(41<<20, total))
	before := snap.Rate
	snap, _, _ = tr.update("x", transferring(90<<20, total))
	if snap.Rate != before {
		t.Fatalf("rate resampled within %v: %.0f -> %.0f", rateSampleInterval, before, snap.Rate)
	}
}

func TestProgressTracker_FileRateWithoutBytes(t *testing.T) {
	clk := newFakeClock()
	tr := newProgressTracker(clk.now)
	tr.update("x", engine.ProgressDetail{Phase: engine.PhaseScanning, FilesDone: 10})
	clk.advance(2 * time.Second)
	snap, _, _ := tr.update("x", engine.ProgressDetail{Phase: engine.PhaseScanning, FilesDone: 110})
	if snap.Rate != 50 || snap.Determinate {
		t.Fatalf("file rate = %.1f determinate=%v, want 50 files/s indeterminate", snap.Rate, snap.Determinate)
	}
}

func TestProgressTracker_NilAndSnapshot(t *testing.T) {
	var nilTracker *progressTracker
	nilTracker.reset()
	if _, ok := nilTracker.snapshot("x"); ok {
		t.Fatal("nil tracker returned a snapshot")
	}
	if snap, changed, applied := nilTracker.update("x", transferring(1, 2)); changed || applied || snap.Phase != engine.PhaseTransferring {
		t.Fatalf("nil update = %+v %v", snap, changed)
	}
	tr := newProgressTracker(nil)
	tr.update("a", transferring(1, 2))
	if _, ok := tr.snapshot("b"); ok {
		t.Fatal("snapshot for another item")
	}
	tr.reset()
	if _, ok := tr.snapshot("a"); ok {
		t.Fatal("snapshot after reset")
	}
}

func TestRunStatusJSONCarriesProgressDetail(t *testing.T) {
	var s RunStatus
	clk := newFakeClock()
	applyProgressSnapshot(&s, progressSnapshot{
		Phase: engine.PhaseTransferring, PhaseStartedAt: clk.t, BytesDone: 50, BytesTotal: 200,
		FilesDone: 1, FilesTotal: 4, Rate: 10, ETASeconds: 15, Determinate: true, Percent: 25, LastProgressAt: clk.t,
	})
	raw, _ := json.Marshal(s)
	for _, field := range []string{`"phase":"transferring"`, `"bytes_done":50`, `"bytes_total":200`, `"files_done":1`, `"files_total":4`,
		`"rate_bytes_per_sec":10`, `"eta_seconds":15`, `"progress_determinate":true`, `"current_item_percent":25`, `"last_progress_at":"2026-10-15T00:00:00Z"`, `"phase_started_at"`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("RunStatus JSON missing %s: %s", field, raw)
		}
	}
	clearProgressDetail(&s)
	raw, _ = json.Marshal(s)
	if strings.Contains(string(raw), "phase") || strings.Contains(string(raw), "bytes_") {
		t.Errorf("cleared status still has detail: %s", raw)
	}
	// Files-only progress reports a file rate instead of a byte rate.
	applyProgressSnapshot(&s, progressSnapshot{Phase: engine.PhaseScanning, FilesDone: 3, Rate: 2})
	if s.RateFilesPerSec != 2 || s.RateBytesPerSec != 0 {
		t.Errorf("file rate = %v / byte rate %v", s.RateFilesPerSec, s.RateBytesPerSec)
	}
}

func TestReportItemDetailUpdatesStatusAndMilestonesYield(t *testing.T) {
	r, _ := newTestRunner(t)
	r.setRunStatus(&RunStatus{Active: true, JobID: 1, RunID: 2, RunType: "restore", CurrentItem: "vm"})
	r.reportItemDetail("vm", transferring(250, 1000))
	r.updateCurrentItemProgress("vm", 7, "Downloading") // legacy milestone must not clobber the phase percent
	st := r.Status()
	if st.Phase != "transferring" || st.BytesDone != 250 || st.CurrentItemPercent != 25 || st.CurrentItemMessage != "Downloading" {
		t.Fatalf("status = %+v, want transferring 250/1000 at 25%%", st)
	}
	// A report for an item that is not current leaves the status alone.
	r.reportItemDetail("other", transferring(999, 1000))
	if st := r.Status(); st.BytesDone != 250 {
		t.Fatalf("foreign item changed status: %+v", st)
	}
	// Moving to the next item clears the detail.
	r.updateRunProgress(1, 0, "next")
	if st := r.Status(); st.Phase != "" || st.BytesTotal != 0 || st.ProgressDeterminate {
		t.Fatalf("detail leaked to next item: %+v", st)
	}
}

func TestMergeProgressDetail(t *testing.T) {
	r, _ := newTestRunner(t)
	r.progress.update("vm", transferring(500, 1000))
	data := map[string]any{"type": "backup_progress", "item": "vm", "percent": 3}
	r.mergeProgressDetail(data)
	if data["phase"] != "transferring" || data["bytes_done"] != int64(500) || data["percent"] != 50 || data["progress_determinate"] != true {
		t.Fatalf("merged = %v", data)
	}
	other := map[string]any{"type": "job_run_started", "item": "vm"}
	r.mergeProgressDetail(other)
	if _, ok := other["phase"]; ok {
		t.Fatal("detail merged into a non-progress event")
	}
	stale := map[string]any{"type": "restore_progress", "item": "different"}
	r.mergeProgressDetail(stale)
	if _, ok := stale["phase"]; ok {
		t.Fatal("detail merged for another item")
	}
}

// TestUploadStagedFilesReportsItemLevelBytes guards #456: bytes from every
// staged file count against the item's combined size, measured in source
// bytes even when the upload is compressed and encrypted.
func TestUploadStagedFilesReportsItemLevelBytes(t *testing.T) {
	cases := []struct {
		name, itemType, compression, passphrase string
	}{
		{"plain folder", "folder", "none", ""},
		{"compressed and encrypted vm", "vm", "zstd", "correct horse battery staple"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newTestRunner(t)
			tmp := t.TempDir()
			for i, n := range []int{3000, 5000, 2000} {
				if err := os.WriteFile(filepath.Join(tmp, "f"+string(rune('a'+i))), make([]byte, n), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, _ := json.Marshal(map[string]string{"path": t.TempDir()})
			dest := db.StorageDestination{Name: "progress", Type: "local", Config: string(cfg)}
			r.setRunStatus(&RunStatus{Active: true, RunType: "backup", CurrentItem: "item"})

			var transfers []engine.ProgressDetail
			ctx := engine.WithDetailReporter(context.Background(), func(item string, det engine.ProgressDetail) {
				if det.Phase == engine.PhaseTransferring {
					transfers = append(transfers, det)
				}
				r.reportItemDetail(item, det)
			})
			if _, err := r.uploadStagedFilesN(ctx, 0, tmp, dest, "rp", true, tc.passphrase, tc.compression, tc.itemType, "item", 1); err != nil {
				t.Fatal(err)
			}
			if len(transfers) == 0 {
				t.Fatal("no transferring reports")
			}
			last := transfers[len(transfers)-1]
			if last.BytesTotal != 10000 || last.BytesDone != 10000 || last.FilesDone != 3 || last.FilesTotal != 3 {
				t.Fatalf("final transfer report = %+v, want 10000/10000 source bytes, 3/3 files", last)
			}
			for i := 1; i < len(transfers); i++ {
				if transfers[i].BytesDone < transfers[i-1].BytesDone {
					t.Fatalf("bytes went backward without a retry: %d -> %d", transfers[i-1].BytesDone, transfers[i].BytesDone)
				}
			}
			if st := r.Status(); st.Phase != "verifying" || st.FilesDone != 3 {
				t.Fatalf("status after verify = %+v, want verifying 3 files", st)
			}
		})
	}
}

func TestProgressTracker_ThrottlesSamePhase(t *testing.T) {
	clk := newFakeClock()
	tr := newProgressTracker(clk.now)
	if _, _, applied := tr.update("x", transferring(10, 1000)); !applied {
		t.Fatal("first report not applied")
	}
	first := clk.now()
	clk.advance(100 * time.Millisecond)
	// Throttled: not applied to the run status, but its counts fold into
	// the snapshot without moving the throttle clock.
	if snap, _, applied := tr.update("x", transferring(20, 1000)); applied || snap.BytesDone != 20 || snap.Percent != 2 || !snap.LastProgressAt.Equal(first) {
		t.Fatalf("report within %v: applied=%v snap=%+v, want throttled with counts folded", reportInterval, applied, snap)
	}
	if _, changed, applied := tr.update("x", engine.ProgressDetail{Phase: engine.PhaseVerifying}); !applied || !changed {
		t.Fatal("phase change was throttled")
	}
	if snap, _, applied := tr.update("x", engine.ProgressDetail{Phase: engine.PhaseVerifying, BytesDone: 50, BytesTotal: 50}); !applied || snap.BytesDone != 50 {
		t.Fatal("final report of a phase was throttled")
	}
	clk.advance(reportInterval)
	if _, _, applied := tr.update("x", engine.ProgressDetail{Phase: engine.PhaseVerifying, FilesDone: 1}); !applied {
		t.Fatal("report after the interval was dropped")
	}
}

// The last file of an indeterminate phase usually lands inside the throttle
// window; Status must still show it rather than the previous applied count.
func TestStatusShowsThrottledFileCount(t *testing.T) {
	r, _ := newTestRunner(t)
	r.setRunStatus(&RunStatus{Active: true, JobID: 1, RunID: 2, RunType: "backup", CurrentItem: "appdata"})
	r.reportItemDetail("appdata", engine.ProgressDetail{Phase: engine.PhaseProcessing, FilesDone: 1})
	r.reportItemDetail("appdata", engine.ProgressDetail{Phase: engine.PhaseProcessing, FilesDone: 2})
	r.reportItemDetail("appdata", engine.ProgressDetail{Phase: engine.PhaseProcessing, FilesDone: 3})
	if st := r.Status(); st.FilesDone != 3 || st.ProgressDeterminate {
		t.Fatalf("status = %+v, want 3 files in an indeterminate phase", st)
	}
}

func TestReportItemDetailIgnoresUnnamedCurrentItem(t *testing.T) {
	r, _ := newTestRunner(t)
	r.setRunStatus(&RunStatus{Active: true, RunType: "backup"}) // between items
	r.reportItemDetail("late-worker", transferring(5, 10))
	if st := r.Status(); st.Phase != "" || st.BytesDone != 0 {
		t.Fatalf("detail applied with no current item: %+v", st)
	}
}

func TestProgressTracker_FileTotalLearnedLate(t *testing.T) {
	clk := newFakeClock()
	tr := newProgressTracker(clk.now)
	tr.update("x", engine.ProgressDetail{Phase: engine.PhaseProcessing})
	clk.advance(time.Second)
	snap, _, _ := tr.update("x", engine.ProgressDetail{Phase: engine.PhaseProcessing, FilesDone: 5, FilesTotal: 20})
	if !snap.Determinate || snap.Percent != 25 {
		t.Fatalf("late file total = %+v, want determinate 25%%", snap)
	}
	clk.advance(2 * time.Second)
	snap, _, _ = tr.update("x", engine.ProgressDetail{Phase: engine.PhaseProcessing, FilesDone: 9, FilesTotal: 20})
	if snap.Rate != 2 {
		t.Fatalf("file rate = %.1f, want 2 files/s", snap.Rate)
	}
}

func TestReportItemDetailLateWorkerKeepsTracker(t *testing.T) {
	r, _ := newTestRunner(t)
	r.setRunStatus(&RunStatus{Active: true, RunType: "backup", CurrentItem: "b"})
	r.reportItemDetail("b", transferring(10, 100))
	r.reportItemDetail("a", transferring(99, 100)) // previous item's straggler
	if snap, ok := r.progress.snapshot("b"); !ok || snap.BytesDone != 10 {
		t.Fatalf("tracker = %+v ok=%v, want item b untouched", snap, ok)
	}
}

// TestStageRestorePointItemReportsFiles guards #456: classic restore downloads
// report files done/total next to bytes.
func TestStageRestorePointItemReportsFiles(t *testing.T) {
	raw := bytes.Repeat([]byte("compressed-content"), 100)
	compressed := writeZstd(t, raw)
	meta := buildChecksumsMetadata(t, "myitem", "archive.tar.zst", sha256hexBytes(compressed))
	r, rp := setupStagingTest(t, map[string][]byte{"staging-test-job/run1/myitem/archive.tar.zst": compressed}, meta)

	var last engine.ProgressDetail
	ctx := engine.WithDetailReporter(context.Background(), func(_ string, d engine.ProgressDetail) {
		if d.Phase == engine.PhaseTransferring {
			last = d
		}
	})
	if err := r.stageRestorePointItem(ctx, rp, "myitem", t.TempDir(), "", 0, 100, restoreProgressReporter{}); err != nil {
		t.Fatal(err)
	}
	if last.FilesTotal != 1 || last.FilesDone != 1 || last.BytesDone != int64(len(compressed)) {
		t.Fatalf("final download report = %+v, want 1/1 files and %d bytes", last, len(compressed))
	}
}
