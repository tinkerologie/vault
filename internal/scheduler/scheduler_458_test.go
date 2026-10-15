package scheduler

import (
	"testing"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/tinkerologie/vault/internal/db"
)

func runEntry(s *Scheduler, id cron.EntryID) {
	s.cron.Entry(id).Job.Run()
}

// TestReloadReplacesEveryEntryKind_458 registers each entry kind via Start,
// invokes the registered callbacks directly (no waiting on the clock), then
// reloads to exercise every removal loop.
func TestReloadReplacesEveryEntryKind_458(t *testing.T) {
	d := testDB(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	lastDayID, err := d.CreateJob(db.Job{
		Name: "lastday", Enabled: true, Schedule: "0 2 L * *", StorageDestID: destID,
		VerifySchedule: "0 4 * * *", BackupTypeChain: "incremental", FullBackupSchedule: "0 1 L * *",
	})
	if err != nil {
		t.Fatal(err)
	}
	plainID, err := d.CreateJob(db.Job{
		Name: "plain", Enabled: true, Schedule: "0 3 * * *", StorageDestID: destID,
		BackupTypeChain: "incremental", FullBackupSchedule: "0 5 * * 0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting("replication_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	addReplSource(t, d)

	var ran, fulls, verifies, repls []int64
	s := New(d, func(id int64) { ran = append(ran, id) })
	s.SetVerifyRunner(func(id int64, _ string) { verifies = append(verifies, id) })
	s.SetFullBackupRunner(func(id int64) { fulls = append(fulls, id) })
	s.SetReplicationRunner(func(id int64) { repls = append(repls, id) })
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)

	if len(s.lastDayEntries) != 1 || len(s.entries) != 1 || len(s.verifyEntries) != 1 ||
		len(s.fullBackupEntries) != 2 || len(s.replEntries) != 1 {
		t.Fatalf("entries: std=%d last=%d verify=%d full=%d repl=%d",
			len(s.entries), len(s.lastDayEntries), len(s.verifyEntries), len(s.fullBackupEntries), len(s.replEntries))
	}

	s.mu.Lock()
	runEntry(s, s.entries[plainID])
	runEntry(s, s.lastDayEntries[lastDayID])
	runEntry(s, s.verifyEntries[lastDayID])
	runEntry(s, s.fullBackupEntries[plainID])
	runEntry(s, s.fullBackupEntries[lastDayID])
	for _, id := range s.replEntries {
		runEntry(s, id)
	}
	s.mu.Unlock()

	// The last-day callbacks only fire on the last day of the month.
	wantLast := 0
	if isLastDayOfMonth(time.Now()) {
		wantLast = 1
	}
	if len(ran) != 1+wantLast || len(fulls) != 1+wantLast || len(verifies) != 1 || len(repls) != 1 {
		t.Fatalf("callbacks: ran=%v fulls=%v verifies=%v repls=%v", ran, fulls, verifies, repls)
	}

	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if len(s.lastDayEntries) != 1 || len(s.fullBackupEntries) != 2 || len(s.replEntries) != 1 {
		t.Fatalf("reload lost entries: last=%d full=%d repl=%d", len(s.lastDayEntries), len(s.fullBackupEntries), len(s.replEntries))
	}
}

func TestBadLastDaySchedulesAreSkipped_458(t *testing.T) {
	d := testDB(t)
	s := New(d, func(int64) {})
	s.SetFullBackupRunner(func(int64) {})
	// "61" is not a valid minute, so the normalised daily spec fails to parse.
	s.addJob(db.Job{ID: 1, Name: "bad", Schedule: "61 2 L * *"})
	s.addFullBackupJob(db.Job{ID: 1, Name: "bad", FullBackupSchedule: "61 2 L * *"})
	if len(s.lastDayEntries) != 0 || len(s.fullBackupEntries) != 0 {
		t.Fatalf("bad last-day schedules registered: last=%d full=%d", len(s.lastDayEntries), len(s.fullBackupEntries))
	}
	s.SetReplicationRunner(func(int64) {})
	s.addReplicationSource(db.ReplicationSource{ID: 1, Name: "bad", Schedule: "not a cron"})
	if len(s.replEntries) != 0 {
		t.Fatal("bad replication schedule registered")
	}
}

func TestStartAndReloadFailOnClosedDB_458(t *testing.T) {
	d := testDB(t)
	s := New(d, func(int64) {})
	s.SetRetryDispatcher(func(int64, int64, int) {})
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err == nil {
		t.Error("Start should surface ListJobs error")
	}
	if err := s.Reload(); err == nil {
		t.Error("Reload should surface ListJobs error")
	}
	// ClaimDueRetries fails: logged, nothing dispatched.
	s.fireDueRetries()
	// GetSetting fails and the source listing fails too: replication off.
	if replicationEnabled(d) {
		t.Error("replicationEnabled should be false when the DB is unavailable")
	}
}

func TestReplicationSourceListFailureIsLogged_458(t *testing.T) {
	d := testDB(t)
	if err := d.SetSetting("replication_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec("DROP TABLE replication_sources"); err != nil {
		t.Fatal(err)
	}
	s := New(d, func(int64) {})
	s.SetReplicationRunner(func(int64) {})
	if err := s.loadReplicationSources(); err == nil {
		t.Fatal("expected list error")
	}
	// Start and Reload log the failure but still succeed.
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
}
