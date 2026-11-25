package scheduler

import (
	"testing"
	"time"
)

func TestPreviewRuns(t *testing.T) {
	t.Parallel()

	loc := time.FixedZone("TEST", 2*60*60)
	from := time.Date(2026, time.January, 30, 12, 0, 0, 0, loc) // a Friday

	tests := []struct {
		name    string
		spec    string
		n       int
		want    []string
		wantErr bool
	}{
		{"manual only has no runs", "", 3, nil, false},
		{"daily keeps the minute", "15 2 * * *", 3, []string{
			"2026-01-31T02:15:00+02:00",
			"2026-02-01T02:15:00+02:00",
			"2026-02-02T02:15:00+02:00",
		}, false},
		{"weekdays", "0 2 * * 1,3", 2, []string{
			"2026-02-02T02:00:00+02:00",
			"2026-02-04T02:00:00+02:00",
		}, false},
		{"last day of month", "30 23 L * *", 3, []string{
			"2026-01-31T23:30:00+02:00",
			"2026-02-28T23:30:00+02:00",
			"2026-03-31T23:30:00+02:00",
		}, false},
		{"last day of a given month", "0 4 L 2 *", 1, []string{
			"2026-02-28T04:00:00+02:00",
		}, false},
		{"descriptor", "@daily", 1, []string{"2026-01-31T00:00:00+02:00"}, false},
		{"count is capped", "*/1 * * * *", 50, nil, false},
		{"invalid", "not a cron", 3, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := PreviewRuns(tt.spec, from, tt.n)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("PreviewRuns(%q) = nil error, want error", tt.spec)
				}
				return
			}
			if err != nil {
				t.Fatalf("PreviewRuns(%q): %v", tt.spec, err)
			}
			if tt.name == "count is capped" {
				if len(got) != MaxPreviewRuns {
					t.Fatalf("len = %d, want %d", len(got), MaxPreviewRuns)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("PreviewRuns(%q) = %v, want %v", tt.spec, got, tt.want)
			}
			for i, w := range tt.want {
				if g := got[i].Format(time.RFC3339); g != w {
					t.Errorf("run %d = %s, want %s", i, g, w)
				}
			}
		})
	}
}

func TestPreviewRunsNeverMatchingSchedule(t *testing.T) {
	t.Parallel()
	// The 31st of February never comes: the preview must give up rather than spin.
	got, err := PreviewRuns("0 2 31 2 *", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want no runs", got)
	}
}
