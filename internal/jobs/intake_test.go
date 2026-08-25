package jobs

import (
	"errors"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/runner"
)

// fakeRunner records what Job Intake asked the runner to do, and reports
// whatever run status a test sets up.
type fakeRunner struct {
	status     runner.RunStatus
	cancelled  []int64
	cleanups   int
	broadcasts []map[string]any
}

func (f *fakeRunner) Status() runner.RunStatus { return f.status }

func (f *fakeRunner) CancelJob(jobID int64) error {
	f.cancelled = append(f.cancelled, jobID)
	return nil
}

func (f *fakeRunner) CleanupJobStorageAsync(int64, string, db.StorageDestination, []string) {
	f.cleanups++
}

func (f *fakeRunner) Broadcast(data map[string]any) {
	f.broadcasts = append(f.broadcasts, data)
}

// effects counts each step of the chain a successful Job write must run.
type effects struct {
	reloads int
	flushes int
	runner  *fakeRunner
}

func newIntake(t *testing.T) (*Intake, *db.DB, *effects) {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	e := &effects{runner: &fakeRunner{}}
	i := New(d, e.runner,
		func() error { e.reloads++; return nil },
		func() { e.flushes++ },
		nil)
	return i, d, e
}

func validJob() db.Job {
	j := db.DefaultJob()
	j.Name = "nightly"
	j.Schedule = "0 3 * * *"
	return j
}

// TestCreateRunsTheWholeEffectChain is the regression test for the defect that
// motivated this module: a Job created outside the REST handler was persisted
// but never handed to the scheduler, so it silently never ran. The scheduler
// does not reload on its own, so a missed reload means the Job is dead until
// the daemon restarts.
func TestCreateRunsTheWholeEffectChain(t *testing.T) {
	i, _, e := newIntake(t)

	job, _, err := i.Create(validJob(), nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if job.ID == 0 {
		t.Error("Create returned a job with no ID")
	}
	if e.reloads != 1 {
		t.Errorf("scheduler reloads = %d, want 1 — the job would never run", e.reloads)
	}
	if e.flushes != 1 {
		t.Errorf("config flushes = %d, want 1 — the job would not survive a reboot", e.flushes)
	}
	if len(e.runner.broadcasts) != 1 {
		t.Fatalf("broadcasts = %d, want 1", len(e.runner.broadcasts))
	}
	if got := e.runner.broadcasts[0]["entity"]; got != "job" {
		t.Errorf("broadcast entity = %v, want \"job\"", got)
	}
}

// TestUpdateAndDeleteAlsoRunTheEffectChain pins the same guarantee on the other
// two writes. A schedule changed by Update, or a Job removed by Delete, is just
// as invisible to a stale scheduler as a newly created one.
func TestUpdateAndDeleteAlsoRunTheEffectChain(t *testing.T) {
	i, _, e := newIntake(t)
	job, _, err := i.Create(validJob(), nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	job.Schedule = "0 5 * * *"
	if _, _, err := i.Update(job.ID, job, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if e.reloads != 2 || e.flushes != 2 {
		t.Errorf("after Update: reloads=%d flushes=%d, want 2 and 2", e.reloads, e.flushes)
	}

	if _, err := i.Delete(job.ID, DeleteOptions{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if e.reloads != 3 || e.flushes != 3 {
		t.Errorf("after Delete: reloads=%d flushes=%d, want 3 and 3", e.reloads, e.flushes)
	}
}

// TestDeleteRefusesDuringRestore is the safety invariant that used to live only
// in the REST handler, leaving every other caller free to delete a Job while a
// restore for it was mid-write — which both interrupts the write and
// cascade-deletes the restore's own records.
func TestDeleteRefusesDuringRestore(t *testing.T) {
	i, d, e := newIntake(t)
	job, _, err := i.Create(validJob(), nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	e.runner.status = runner.RunStatus{Active: true, JobID: job.ID, RunType: "restore"}

	if _, err := i.Delete(job.ID, DeleteOptions{}); !errors.Is(err, ErrRestoreInProgress) {
		t.Fatalf("Delete during restore = %v, want ErrRestoreInProgress", err)
	}
	if _, err := d.GetJob(job.ID); err != nil {
		t.Errorf("job was deleted despite the refusal: %v", err)
	}
	if e.reloads != 1 {
		t.Errorf("a refused delete must not run the effect chain; reloads = %d, want 1", e.reloads)
	}
}

// TestDeleteCancelsARunningBackup covers the other half of that guard: a Job
// that is merely running is cancelled rather than refused, so its goroutine
// does not keep writing after the rows are gone (issue #235).
func TestDeleteCancelsARunningBackup(t *testing.T) {
	i, _, e := newIntake(t)
	job, _, err := i.Create(validJob(), nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	e.runner.status = runner.RunStatus{Active: true, JobID: job.ID, RunType: "backup"}

	if _, err := i.Delete(job.ID, DeleteOptions{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(e.runner.cancelled) == 0 {
		t.Error("running backup was not cancelled before the rows were removed")
	}
}

// TestRejectedWriteRunsNoEffects guards against the module reporting a change
// that never happened — a spurious reload or broadcast would make clients
// re-fetch state that did not move.
func TestRejectedWriteRunsNoEffects(t *testing.T) {
	i, _, e := newIntake(t)
	bad := validJob()
	bad.Name = "   "

	if _, _, err := i.Create(bad, nil); err == nil {
		t.Fatal("Create with a blank name succeeded, want rejection")
	}
	if e.reloads != 0 || e.flushes != 0 || len(e.runner.broadcasts) != 0 {
		t.Errorf("a rejected write ran effects: reloads=%d flushes=%d broadcasts=%d",
			e.reloads, e.flushes, len(e.runner.broadcasts))
	}
}

// TestValidationErrorsAreCallerFaults checks the error classification adapters
// depend on. A ValidationError becomes a 400 or an MCP tool error; anything
// else must stay a server fault, so misclassifying one sends the operator
// hunting for a config mistake during an outage.
func TestValidationErrorsAreCallerFaults(t *testing.T) {
	i, _, _ := newIntake(t)

	cases := []struct {
		name      string
		mutate    func(*db.Job)
		wantField string
	}{
		{"blank name", func(j *db.Job) { j.Name = "  " }, "name"},
		{"overlong name", func(j *db.Job) { j.Name = strings.Repeat("a", MaxJobNameLen+1) }, "name"},
		{"bad schedule", func(j *db.Job) { j.Schedule = "not a cron" }, "schedule"},
		{"bad enum", func(j *db.Job) { j.Encryption = "aes" }, "encryption"},
		{"negative retention", func(j *db.Job) { j.RetentionCount = -1 }, "retention_count"},
		{"missing dest", func(j *db.Job) { j.StorageDestID = 999999 }, "storage_dest_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job := validJob()
			tc.mutate(&job)
			_, _, err := i.Create(job, nil)
			ve, ok := IsValidation(err)
			if !ok {
				t.Fatalf("Create = %v, want a ValidationError", err)
			}
			if ve.Field != tc.wantField {
				t.Errorf("Field = %q, want %q", ve.Field, tc.wantField)
			}
		})
	}
}

// TestNormalizeClampsRatherThanRejects pins the fields that are deliberately
// corrected instead of refused. Job.EffectiveUploadConcurrency relies on this
// contract, so rejecting these would break it.
func TestNormalizeClampsRatherThanRejects(t *testing.T) {
	i, _, _ := newIntake(t)

	job := validJob()
	job.Schedule = "  0 3 * * *  "
	job.MaxParallelUploads = 999
	job.Compression = "none"
	job.CompressionLevel = "best"

	saved, _, err := i.Create(job, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if saved.MaxParallelUploads != maxParallelUploadsLimit {
		t.Errorf("MaxParallelUploads = %d, want clamped to %d", saved.MaxParallelUploads, maxParallelUploadsLimit)
	}
	if saved.CompressionLevel != "" {
		t.Errorf("CompressionLevel = %q, want cleared for non-gzip/zstd", saved.CompressionLevel)
	}
	if saved.Schedule != "0 3 * * *" {
		t.Errorf("Schedule = %q, want trimmed", saved.Schedule)
	}
}

// TestWhitespaceScheduleBecomesManualOnly is the specific normalisation that a
// caller cannot be trusted to do: a whitespace-only schedule passes
// ValidateSchedule (which trims internally) but, if persisted verbatim, makes
// the scheduler's `Schedule != ""` check try to cron-parse "   ", fail, and
// leave the Job marked scheduled while never running.
func TestWhitespaceScheduleBecomesManualOnly(t *testing.T) {
	i, _, _ := newIntake(t)
	job := validJob()
	job.Schedule = "   "

	saved, _, err := i.Create(job, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if saved.Schedule != "" {
		t.Errorf("Schedule = %q, want \"\" (manual-only)", saved.Schedule)
	}
}

// TestUpdateMissingJobIsNotFound pins the guard against a silent no-op:
// UpdateJob is an UPDATE … WHERE that does not error on zero rows.
func TestUpdateMissingJobIsNotFound(t *testing.T) {
	i, _, _ := newIntake(t)
	if _, _, err := i.Update(4242, validJob(), nil); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("Update of a missing job = %v, want ErrJobNotFound", err)
	}
}

// TestUpdateWithNilItemsKeepsPersistedItems pins the contract that a request
// omitting items edits the Job without destroying its items.
func TestUpdateWithNilItemsKeepsPersistedItems(t *testing.T) {
	i, _, _ := newIntake(t)
	job, items, err := i.Create(validJob(), []db.JobItem{
		{ItemType: "container", ItemName: "keep-me", ItemID: "keep-me"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("Create returned %d items, want 1", len(items))
	}

	job.Name = "renamed"
	_, after, err := i.Update(job.ID, job, nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(after) != 1 || after[0].ItemName != "keep-me" {
		t.Errorf("items after a nil-items update = %#v, want the persisted item untouched", after)
	}
}

// TestDefaultJobMatchesTheEnumAllowLists guards the shared defaults against
// drifting out of the sets Job Intake accepts — a default that fails
// validation would reject every Job that relied on it.
func TestDefaultJobMatchesTheEnumAllowLists(t *testing.T) {
	d := db.DefaultJob()
	for field, value := range map[string]string{
		"backup_type_chain": d.BackupTypeChain,
		"compression":       d.Compression,
		"encryption":        d.Encryption,
		"container_mode":    d.ContainerMode,
		"notify_on":         d.NotifyOn,
	} {
		if err := validateEnum(field, value); err != nil {
			t.Errorf("DefaultJob().%s: %v", field, err)
		}
	}
}

// failItemInserts makes any job_items insert carrying the sentinel name fail,
// so the partial-write recovery paths can be exercised through the module's
// own interface rather than by reaching past it.
func failItemInserts(t *testing.T, d *db.DB) {
	t.Helper()
	_, err := d.Exec(`CREATE TRIGGER fail_boom BEFORE INSERT ON job_items
		WHEN NEW.item_name = 'boom'
		BEGIN SELECT RAISE(ABORT, 'boom'); END;`)
	if err != nil {
		t.Fatalf("installing trigger: %v", err)
	}
}

func item(name string) db.JobItem {
	return db.JobItem{ItemType: "container", ItemName: name, ItemID: name}
}

// A Job and its items are one unit to the caller. A failed item insert used to
// leave the Job row behind holding only the items that made it in — a Job that
// looks configured in the UI but backs up the wrong set of things.
func TestCreateLeavesNothingBehindWhenAnItemFails(t *testing.T) {
	i, d, e := newIntake(t)
	failItemInserts(t, d)

	_, _, err := i.Create(validJob(), []db.JobItem{item("plex"), item("boom")})
	if err == nil {
		t.Fatal("expected the failing item insert to surface")
	}

	jobs, listErr := d.ListJobs()
	if listErr != nil {
		t.Fatalf("listing jobs: %v", listErr)
	}
	if len(jobs) != 0 {
		t.Fatalf("half-built job survived the failure: %+v", jobs)
	}
	if e.reloads != 0 || e.flushes != 0 || len(e.runner.broadcasts) != 0 {
		t.Fatalf("effects ran for a write that failed: %+v", e)
	}
}

// Replacing items is delete-then-insert, so a failure partway once left the
// Job with fewer items than either the old or the new set — silently dropping
// things from the backup.
func TestUpdateRestoresThePreviousItemsWhenAReplacementFails(t *testing.T) {
	i, d, e := newIntake(t)

	job, _, err := i.Create(validJob(), []db.JobItem{item("plex"), item("sonarr")})
	if err != nil {
		t.Fatalf("creating job: %v", err)
	}
	failItemInserts(t, d)
	before := e.reloads

	if _, _, err := i.Update(job.ID, job, []db.JobItem{item("radarr"), item("boom")}); err == nil {
		t.Fatal("expected the failing item insert to surface")
	}

	saved, err := d.GetJobItems(job.ID)
	if err != nil {
		t.Fatalf("reading items: %v", err)
	}
	var names []string
	for _, it := range saved {
		names = append(names, it.ItemName)
	}
	if strings.Join(names, ",") != "plex,sonarr" {
		t.Fatalf("previous items were not restored, got %v", names)
	}
	if e.reloads != before {
		t.Fatalf("effects ran for a write that failed: %d reloads", e.reloads-before)
	}
}
