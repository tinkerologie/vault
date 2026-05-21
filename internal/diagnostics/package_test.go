package diagnostics

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestPackageAsZip(t *testing.T) {
	t.Parallel()

	bundle := &DiagnosticBundle{
		GeneratedAt:   time.Now().UTC(),
		CorrelationID: "test-correlation-id",
		System: SystemInfo{
			Version:   "2026.3.0",
			GoVersion: "go1.26",
			OS:        "linux",
			Arch:      "amd64",
			Hostname:  "test-host",
		},
		Storage: []StorageInfo{
			{ID: 1, Name: "local", Type: "local", Config: `{"path":"/mnt/user/backups"}`},
		},
		Jobs: []JobInfo{
			{ID: 7, Name: "demo", Schedule: "0 2 * * *", Enabled: true, ItemCount: 1},
		},
		Activity: []ActivityInfo{
			{ID: 1, Level: "info", Message: "hello"},
		},
		Runs: []RunInfo{
			{ID: 1, JobID: 7, Status: "completed", RunType: "backup"},
		},
		LogTail: "2026/05/21 10:00:00 hello from logger\n",
	}

	reader, err := PackageAsZip(bundle)
	if err != nil {
		t.Fatalf("PackageAsZip() error = %v", err)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading zip: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("zip output is empty")
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("parsing zip: %v", err)
	}

	// The split layout produces a fixed set of files. Pin them so a
	// refactor that drops one (or accidentally renames diagnostics.json)
	// surfaces here.
	wantFiles := map[string]bool{
		"diagnostics.json":  false,
		"system.json":       false,
		"settings.json":     false,
		"storage.json":      false,
		"jobs.json":         false,
		"runs.json":         false,
		"activity.json":     false,
		"replication.json":  false,
		"runner.json":       false,
		"entries.json":      false,
		"verify_runs.json":  false,
		"dedup_stats.json":  false,
		"runtime.json":      false,
		"pools.json":        false,
		"connectivity.json": false,
		"scheduler.json":    false,
		"vault.log":         false,
	}
	for _, f := range zr.File {
		if _, ok := wantFiles[f.Name]; !ok {
			t.Errorf("unexpected file in zip: %s", f.Name)
			continue
		}
		wantFiles[f.Name] = true
	}
	for name, seen := range wantFiles {
		if !seen {
			t.Errorf("missing expected file in zip: %s", name)
		}
	}

	// diagnostics.json must contain the summary but NOT the log tail
	// (the log lives in vault.log to keep the summary scannable).
	top := readFromZip(t, zr, "diagnostics.json")
	var summary DiagnosticBundle
	if err := json.Unmarshal(top, &summary); err != nil {
		t.Fatalf("decoding diagnostics.json: %v", err)
	}
	if summary.CorrelationID != "test-correlation-id" {
		t.Errorf("diagnostics.json correlation_id = %q, want %q", summary.CorrelationID, "test-correlation-id")
	}
	if summary.LogTail != "" {
		t.Error("diagnostics.json must not embed the log tail (lives in vault.log instead)")
	}
	if len(summary.Activity) != 0 {
		t.Errorf("diagnostics.json must not embed activity; got %d entries", len(summary.Activity))
	}
	if len(summary.Runs) != 0 {
		t.Errorf("diagnostics.json must not embed runs; got %d entries", len(summary.Runs))
	}

	// activity.json carries the activity slice.
	act := readFromZip(t, zr, "activity.json")
	var acts []ActivityInfo
	if err := json.Unmarshal(act, &acts); err != nil {
		t.Fatalf("decoding activity.json: %v", err)
	}
	if len(acts) != 1 {
		t.Errorf("activity.json count = %d, want 1", len(acts))
	}

	// vault.log is plain text.
	logTail := string(readFromZip(t, zr, "vault.log"))
	if !strings.Contains(logTail, "hello from logger") {
		t.Errorf("vault.log missing expected line, got %q", logTail)
	}
}

func TestPackageAsZipOmitsLogFileWhenEmpty(t *testing.T) {
	t.Parallel()
	bundle := &DiagnosticBundle{GeneratedAt: time.Now().UTC()}
	reader, err := PackageAsZip(bundle)
	if err != nil {
		t.Fatalf("PackageAsZip() error = %v", err)
	}
	data, _ := io.ReadAll(reader)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("parsing zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name == "vault.log" {
			t.Fatal("vault.log should not be present when LogTail is empty")
		}
	}
}

func readFromZip(t *testing.T, zr *zip.Reader, name string) []byte {
	t.Helper()
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening %s: %v", name, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		return data
	}
	t.Fatalf("file %s not in zip", name)
	return nil
}
