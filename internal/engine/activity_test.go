package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestActivitySampleActive(t *testing.T) {
	th := IdleThresholds{CPUPercent: 20, NetKbps: 500} // 500 kbps = 62500 B/s
	cases := []struct {
		name   string
		sample ActivitySample
		want   bool
	}{
		{"unknown is idle (fail-open)", ActivitySample{}, false},
		{"below both thresholds", ActivitySample{CPUPercent: 5, NetBytesPerSec: 1000, Known: true}, false},
		{"cpu above threshold", ActivitySample{CPUPercent: 45, Known: true}, true},
		{"network above threshold", ActivitySample{NetBytesPerSec: 200_000, Known: true}, true},
		{"exactly at thresholds is idle", ActivitySample{CPUPercent: 20, NetBytesPerSec: 62_500, Known: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sample.Active(th); got != tc.want {
				t.Fatalf("Active(%+v) = %v, want %v", tc.sample, got, tc.want)
			}
		})
	}
}

func TestProbeFolderActivity(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if s := ProbeFolderActivity(context.Background(), dir, nil, 5*time.Minute); s.Active(IdleThresholds{}) {
		t.Fatal("folder with only old files should be idle")
	}

	if err := os.WriteFile(filepath.Join(dir, "fresh.txt"), []byte("y"), 0o644); err != nil {
		t.Fatalf("write fresh: %v", err)
	}
	if s := ProbeFolderActivity(context.Background(), dir, nil, 5*time.Minute); !s.Active(IdleThresholds{CPUPercent: 99}) {
		t.Fatal("folder with a freshly-written file should be active")
	}

	// Excluded recent files must not count as activity.
	if s := ProbeFolderActivity(context.Background(), dir, []string{"fresh.txt"}, 5*time.Minute); s.Active(IdleThresholds{}) {
		t.Fatal("excluded fresh file should not make the folder active")
	}
}
