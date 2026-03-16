package scheduler

import (
	"path/filepath"
	"sync/atomic"
	"testing"

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
