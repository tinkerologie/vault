package scheduler

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

func testDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestSchedulerStartStop(t *testing.T) {
	d := testDB(t)
	var called int32
	runner := func(jobID int64) { atomic.AddInt32(&called, 1) }

	s := New(d, runner)
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	s.Stop()
}

func TestSchedulerWithJob(t *testing.T) {
	d := testDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "test", Type: "local", Config: "{}"})
	d.CreateJob(db.Job{Name: "test-job", Enabled: true, Schedule: "* * * * *", StorageDestID: destID})

	var called int32
	runner := func(jobID int64) { atomic.AddInt32(&called, 1) }

	s := New(d, runner)
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer s.Stop()

	if len(s.entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(s.entries))
	}
}

func TestSchedulerReload(t *testing.T) {
	d := testDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "test", Type: "local", Config: "{}"})

	runner := func(jobID int64) {}
	s := New(d, runner)
	s.Start()
	defer s.Stop()

	if len(s.entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(s.entries))
	}

	d.CreateJob(db.Job{Name: "new-job", Enabled: true, Schedule: "0 2 * * *", StorageDestID: destID})
	s.Reload()

	if len(s.entries) != 1 {
		t.Errorf("expected 1 entry after reload, got %d", len(s.entries))
	}
}

func TestParseLastDaySchedule(t *testing.T) {
	tests := []struct {
		name     string
		schedule string
		wantCron string
		wantOK   bool
	}{
		{"last day monthly", "0 2 L * *", "0 2 * * *", true},
		{"last day yearly", "30 14 L 6 *", "30 14 * 6 *", true},
		{"normal monthly", "0 2 15 * *", "", false},
		{"normal daily", "0 2 * * *", "", false},
		{"invalid fields", "0 2 * *", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseLastDaySchedule(tt.schedule)
			if ok != tt.wantOK {
				t.Errorf("parseLastDaySchedule(%q) ok = %v, want %v", tt.schedule, ok, tt.wantOK)
			}
			if got != tt.wantCron {
				t.Errorf("parseLastDaySchedule(%q) = %q, want %q", tt.schedule, got, tt.wantCron)
			}
		})
	}
}

func TestIsLastDayOfMonth(t *testing.T) {
	tests := []struct {
		name string
		date time.Time
		want bool
	}{
		{"Jan 31", time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC), true},
		{"Jan 30", time.Date(2026, 1, 30, 12, 0, 0, 0, time.UTC), false},
		{"Feb 28 non-leap", time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC), true},
		{"Feb 28 leap", time.Date(2028, 2, 28, 12, 0, 0, 0, time.UTC), false},
		{"Feb 29 leap", time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC), true},
		{"Apr 30", time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC), true},
		{"Apr 29", time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC), false},
		{"Dec 31", time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isLastDayOfMonth(tt.date)
			if got != tt.want {
				t.Errorf("isLastDayOfMonth(%v) = %v, want %v", tt.date, got, tt.want)
			}
		})
	}
}

func TestSchedulerLastDayJob(t *testing.T) {
	d := testDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "test", Type: "local", Config: "{}"})
	d.CreateJob(db.Job{Name: "last-day-job", Enabled: true, Schedule: "0 2 L * *", StorageDestID: destID})

	var called int32
	runner := func(jobID int64) { atomic.AddInt32(&called, 1) }

	s := New(d, runner)
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer s.Stop()

	// The L job should be in lastDayEntries, not entries.
	if len(s.lastDayEntries) != 1 {
		t.Errorf("expected 1 lastDayEntries, got %d", len(s.lastDayEntries))
	}
	if len(s.entries) != 0 {
		t.Errorf("expected 0 standard entries, got %d", len(s.entries))
	}
}
