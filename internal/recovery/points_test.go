package recovery

import (
	"strings"
	"testing"
	"time"
)

// TestFindPointLatestRefusesLockedNewest checks "<job>/latest" never falls
// back to an older backup when the newest one cannot be opened.
func TestFindPointLatestRefusesLockedNewest(t *testing.T) {
	s := &Session{points: []Point{
		{Job: "J", StoragePath: "J/2026-10-01_020000", CreatedAt: time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)},
		{Job: "J", StoragePath: "J/2026-10-02_020000", CreatedAt: time.Date(2026, 10, 2, 2, 0, 0, 0, time.UTC), Locked: true, LockedBy: "age"},
		{Job: "K", StoragePath: "K/2026-10-02_020000", CreatedAt: time.Date(2026, 10, 2, 2, 0, 0, 0, time.UTC)},
	}}
	if _, err := s.FindPoint("J/latest"); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("J/latest = %v, want the locked-newest passphrase error", err)
	}
	p, err := s.FindPoint("K/latest")
	if err != nil || p.StoragePath != "K/2026-10-02_020000" {
		t.Fatalf("K/latest = %+v, %v", p, err)
	}
	if _, err := s.FindPoint("J/2026-10-01_020000"); err != nil {
		t.Fatalf("an explicit older point must still open: %v", err)
	}
	if _, err := s.FindPoint("missing/latest"); err == nil {
		t.Fatal("missing job resolved")
	}
}

// TestRunFolderTime checks both run-folder layouts parse, so locked runs
// (whose manifests are unreadable) still sort by time.
func TestRunFolderTime(t *testing.T) {
	for _, sp := range []string{"Job/2026-09-21_154834", "Job/35_2026-09-21_154834"} {
		got, ok := runFolderTime(sp)
		if !ok || got.Year() != 2026 || got.Minute() != 48 {
			t.Errorf("runFolderTime(%q) = %v, %v", sp, got, ok)
		}
	}
	if _, ok := runFolderTime("Job/latest"); ok {
		t.Error("non-timestamp folder parsed")
	}
	p := pointFromManifest(map[string]any{"storage_path": "Job/2026-09-21_154834", "encrypted": true, "key": "dedup"})
	if p.CreatedAt.IsZero() || !p.Locked || p.LockedBy != "dedup" {
		t.Fatalf("locked point = %+v", p)
	}
}
