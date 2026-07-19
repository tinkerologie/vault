package diagnostics

import (
	"archive/zip"
	"path/filepath"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/logbuf"
)

// openTestDB returns a freshly opened DB rooted in t.TempDir().
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// --- Trivial constructors / setters ---

func TestNewCollector(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	ring := logbuf.New(1024)
	c := NewCollector(d, func() RunnerStatus { return RunnerStatus{Active: false} }, "test-version", ring)
	if c == nil {
		t.Fatal("nil collector")
	}
	if c.db != d {
		t.Errorf("db not stored")
	}
	if c.version != "test-version" {
		t.Errorf("version = %q, want test-version", c.version)
	}
	if c.startTime.IsZero() {
		t.Errorf("startTime not initialized")
	}
}

func TestSetDedupStatsFunc(t *testing.T) {
	t.Parallel()
	c := NewCollector(openTestDB(t), nil, "v", nil)
	called := false
	c.SetDedupStatsFunc(func(db.StorageDestination) (chunks, packs, logical, physical, wasted int64, lastGCAt time.Time, lastGCFreed int64, err error) {
		called = true
		return
	})
	if c.dedupFn == nil {
		t.Fatal("dedupFn not set")
	}
	// Smoke-test the captured closure.
	c.dedupFn(db.StorageDestination{})
	if !called {
		t.Error("dedupFn not invoked")
	}
}

func TestSetNextRunFunc(t *testing.T) {
	t.Parallel()
	c := NewCollector(openTestDB(t), nil, "v", nil)
	c.SetNextRunFunc(func(int64) (string, bool) { return "2026-01-01 00:00:00", true })
	if c.nextRunFn == nil {
		t.Fatal("nextRunFn not set")
	}
	got, ok := c.nextRunFn(1)
	if !ok || got == "" {
		t.Errorf("nextRunFn returned (%q, %v)", got, ok)
	}
}

// --- Pure helpers ---

func TestDbDir(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"/var/lib/vault/vault.db", "/var/lib/vault"},
		{"/single", ""},
		{"no-separator", "no-separator"},
		{"/", ""},
	}
	for _, tc := range cases {
		got := dbDir(tc.in)
		if got != tc.want {
			t.Errorf("dbDir(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExtractRunErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in string
		wantN    int
	}{
		{"empty", "", 0},
		{"not-json", "plain text log line", 0},
		{"empty-array", "[]", 0},
		{"single-error", `[{"name":"x","error":"failed"}]`, 1},
		{"single-error-no-name", `[{"error":"oops"}]`, 1},
		{"mixed", `[{"name":"a","error":"e1"},{"name":"b"},{"name":"c","error":"e3"}]`, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractRunErrors(tc.in)
			if len(got) != tc.wantN {
				t.Errorf("extractRunErrors(%q) = %v, want %d entries", tc.in, got, tc.wantN)
			}
		})
	}

	// Spot-check formatting.
	out := extractRunErrors(`[{"name":"job1","error":"timeout"}]`)
	if len(out) != 1 || out[0] != "job1: timeout" {
		t.Errorf("formatted entry = %v", out)
	}
	out = extractRunErrors(`[{"error":"raw"}]`)
	if len(out) != 1 || out[0] != "raw" {
		t.Errorf("no-name entry = %v", out)
	}
}

func TestInfoWarnErrEntry(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ie := infoEntry(now, "cid", "h", "svc", "msg", map[string]string{"a": "b"})
	if ie.Level != LevelInfo {
		t.Errorf("infoEntry Level = %q", ie.Level)
	}
	if ie.Context["a"] != "b" {
		t.Errorf("infoEntry Context missing")
	}
	we := warnEntry(now, "cid", "h", "svc", "warn")
	if we.Level != LevelWarn {
		t.Errorf("warnEntry Level = %q", we.Level)
	}
	ee := errEntry(now, "cid", "h", "svc", "fail")
	if ee.Level != LevelError {
		t.Errorf("errEntry Level = %q", ee.Level)
	}
	// Common fields
	for _, e := range []DiagnosticEntry{ie, we, ee} {
		if e.CorrelationID != "cid" || e.Host != "h" || e.Service != "svc" {
			t.Errorf("common fields not propagated: %+v", e)
		}
		if e.Timestamp != now {
			t.Errorf("timestamp not propagated")
		}
	}
}

func TestProbeDisk(t *testing.T) {
	t.Parallel()
	// Valid path — TempDir always exists on a working filesystem.
	d := probeDisk(t.TempDir())
	if d.Path == "" {
		t.Error("Path not set")
	}
	if d.Error != "" {
		t.Errorf("probeDisk(tempdir) Error = %q, want empty", d.Error)
	}
	if d.TotalBytes == 0 {
		t.Errorf("TotalBytes = 0; expected real bytes from statfs")
	}
	// UsedPct must be in 0-100 range.
	if d.UsedPct < 0 || d.UsedPct > 100 {
		t.Errorf("UsedPct out of range: %d", d.UsedPct)
	}

	// Bogus path — should return Error set.
	bad := probeDisk("/this/path/does/not/exist/nope-12345")
	if bad.Error == "" {
		t.Error("probeDisk on missing path should set Error")
	}
}

// --- Collect() top-level integration ---

// TestCollectorCollectEmpty exercises Collect() on a freshly created DB.
// No jobs/runs/activity, but all the helper branches that handle the
// empty case still fire.
func TestCollectorCollectEmpty(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	c := NewCollector(d, nil, "v-test", nil)
	bundle, err := c.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if bundle.CorrelationID == "" {
		t.Error("CorrelationID empty")
	}
	if bundle.System.Version != "v-test" {
		t.Errorf("System.Version = %q, want v-test", bundle.System.Version)
	}
	if bundle.System.GoVersion == "" {
		t.Error("System.GoVersion empty")
	}
	if bundle.GeneratedAt.IsZero() {
		t.Error("GeneratedAt zero")
	}
	if bundle.Database.Path == "" {
		t.Error("Database.Path empty")
	}
	if len(bundle.Entries) == 0 {
		t.Error("Entries empty")
	}
	// Runtime info should always populate.
	if bundle.Runtime.NumCPU == 0 {
		t.Error("Runtime.NumCPU = 0")
	}
}

// TestCollectorCollectFull seeds the DB with rich data so most branches of
// every collect* helper fire, including:
//   - storage destinations (redacted config)
//   - jobs + job items (encrypted variant exercises HasEncryption flag)
//   - recent runs (including a failed run with JSON log → extractRunErrors)
//   - activity log entries
//   - replication sources (URL is redacted)
//   - settings with encryption_passphrase / api_key_hash / staging_dir
//   - a verify run
//   - log ring snapshot
//   - statusFn returning a non-zero RunnerStatus
//   - dedupFn returning per-destination stats
//   - nextRunFn returning a scheduled timestamp
func TestCollectorCollectFull(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)

	// Storage with sensitive-ish config to drive RedactJSON.
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:         "with-creds",
		Type:         "sftp",
		Config:       `{"host":"backups.example.com","user":"u","password":"secret"}`,
		DedupEnabled: true,
	})
	if err != nil {
		t.Fatalf("dest: %v", err)
	}

	// Job + items
	jobID, err := d.CreateJob(db.Job{
		Name:            "Backup Web",
		Schedule:        "0 2 * * *",
		Enabled:         true,
		StorageDestID:   destID,
		BackupTypeChain: "full",
		Compression:     "zstd",
		Encryption:      "aes",
		ContainerMode:   "one_by_one",
		NotifyOn:        "failure",
		VerifyBackup:    true,
		RetentionCount:  3,
		RetentionDays:   7,
	})
	if err != nil {
		t.Fatalf("job: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{
		JobID:    jobID,
		ItemType: "container",
		ItemName: "web",
		ItemID:   "abc",
		Settings: `{"path":"/data","apikey":"abcd"}`,
	}); err != nil {
		t.Fatalf("item: %v", err)
	}

	// Successful run
	if _, err := d.CreateImportedJobRun(db.JobRun{
		JobID:      jobID,
		Status:     "success",
		BackupType: "full",
		ItemsTotal: 1,
		ItemsDone:  1,
	}, time.Now().Add(-1*time.Hour)); err != nil {
		t.Fatalf("good run: %v", err)
	}
	// Failed run with JSON log so extractRunErrors fires. CreateImportedJobRun
	// doesn't persist the Log field, so UPDATE it directly afterward.
	failedID, err := d.CreateImportedJobRun(db.JobRun{
		JobID:       jobID,
		Status:      "failed",
		BackupType:  "full",
		ItemsTotal:  1,
		ItemsFailed: 1,
	}, time.Now())
	if err != nil {
		t.Fatalf("failed run: %v", err)
	}
	if _, err := d.Exec(`UPDATE job_runs SET log=? WHERE id=?`,
		`[{"name":"web","error":"timeout"}]`, failedID); err != nil {
		t.Fatalf("update log: %v", err)
	}

	// Activity log entries
	d.LogActivity("info", "system", "test entry 1", "")
	d.LogActivity("error", "backup", "test entry 2", `{"detail":"x"}`)

	// Replication source with URL containing credentials.
	if _, err := d.CreateReplicationSource(db.ReplicationSource{
		Name:          "Remote",
		URL:           "https://user:pass@vault.remote.com",
		StorageDestID: destID,
		Schedule:      "@hourly",
		Enabled:       true,
	}); err != nil {
		t.Fatalf("repl source: %v", err)
	}

	// Settings dump branches
	_ = d.SetSetting("encryption_passphrase", "shh")
	_ = d.SetSetting("api_key_hash", "deadbeef")
	_ = d.SetSetting("staging_dir_override", "/mnt/cache/staging")
	_ = d.SetSetting("snapshot_path_override", "/mnt/cache/snap/vault.db")
	_ = d.SetSetting("vm_backup_enabled", "false")
	_ = d.SetSetting("time_format", "24h")
	_ = d.SetSetting("notification_provider", "discord")

	// Verify run
	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:    1,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "/tmp/x",
		Metadata:    "{}",
	})
	if err != nil {
		t.Fatalf("rp: %v", err)
	}
	if _, err := d.CreateVerifyRun(rpID, "deep"); err != nil {
		t.Fatalf("verify run: %v", err)
	}

	// Log ring
	ring := logbuf.New(4096)
	_, _ = ring.Write([]byte("hello from ring\n"))

	// Status function
	statusFn := func() RunnerStatus {
		return RunnerStatus{Active: true, JobID: jobID, JobName: "Backup Web", RunType: "backup"}
	}

	c := NewCollector(d, statusFn, "v9.9.9", ring)

	// Dedup fn returning stats for the dedup-enabled destination.
	c.SetDedupStatsFunc(func(dest db.StorageDestination) (chunks, packs, logical, physical, wasted int64, lastGCAt time.Time, lastGCFreed int64, err error) {
		return 100, 5, 1024, 512, 10, time.Now().Add(-time.Hour), 256, nil
	})

	// Next-run resolver
	c.SetNextRunFunc(func(id int64) (string, bool) {
		return "2030-01-01 00:00:00", true
	})

	bundle, err := c.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Storage destinations were collected and config redacted.
	if len(bundle.Storage) != 1 {
		t.Fatalf("Storage len = %d, want 1", len(bundle.Storage))
	}
	if bundle.Storage[0].Config == `{"host":"backups.example.com","user":"u","password":"secret"}` {
		t.Errorf("Storage[0].Config not redacted: %s", bundle.Storage[0].Config)
	}

	// Jobs (with the encrypted job's HasEncryption flag set).
	if len(bundle.Jobs) != 1 {
		t.Fatalf("Jobs len = %d, want 1", len(bundle.Jobs))
	}
	if !bundle.Jobs[0].HasEncryption {
		t.Error("HasEncryption should be true for encryption=aes")
	}
	if bundle.Jobs[0].ItemCount != 1 {
		t.Errorf("ItemCount = %d, want 1", bundle.Jobs[0].ItemCount)
	}

	// Runs included; the failed run should have ErrorMessages extracted.
	if len(bundle.Runs) < 2 {
		t.Errorf("Runs len = %d, want >= 2", len(bundle.Runs))
	}
	hasErrors := false
	for _, r := range bundle.Runs {
		if r.Status == "failed" && len(r.ErrorMessages) > 0 {
			hasErrors = true
			break
		}
	}
	if !hasErrors {
		t.Error("failed run's ErrorMessages not extracted")
	}

	// Activity log entries present.
	if len(bundle.Activity) < 2 {
		t.Errorf("Activity len = %d, want >= 2", len(bundle.Activity))
	}

	// Replication URL got redacted (no plaintext password).
	if len(bundle.Replication) != 1 {
		t.Fatalf("Replication len = %d", len(bundle.Replication))
	}
	if bundle.Replication[0].URL == "https://user:pass@vault.remote.com" {
		t.Errorf("URL not redacted: %s", bundle.Replication[0].URL)
	}

	// Settings flags from our SetSetting calls.
	if !bundle.Settings.EncryptionConfigured {
		t.Error("EncryptionConfigured = false, want true")
	}
	if !bundle.Settings.APIKeyConfigured {
		t.Error("APIKeyConfigured = false, want true")
	}
	if bundle.Settings.StagingDirOverride != "/mnt/cache/staging" {
		t.Errorf("StagingDirOverride = %q", bundle.Settings.StagingDirOverride)
	}
	if bundle.Settings.VMBackupEnabled {
		t.Error("VMBackupEnabled = true, want false (explicit override)")
	}
	if bundle.Settings.TimeFormat != "24h" {
		t.Errorf("TimeFormat = %q", bundle.Settings.TimeFormat)
	}

	// Verify runs collected.
	if len(bundle.VerifyRuns) != 1 {
		t.Errorf("VerifyRuns len = %d, want 1", len(bundle.VerifyRuns))
	}

	// Dedup stats collected for the dedup-enabled destination.
	if len(bundle.DedupStats) != 1 {
		t.Fatalf("DedupStats len = %d, want 1", len(bundle.DedupStats))
	}
	if bundle.DedupStats[0].TotalChunks != 100 {
		t.Errorf("TotalChunks = %d", bundle.DedupStats[0].TotalChunks)
	}
	if bundle.DedupStats[0].DedupRatio == 0 {
		t.Error("DedupRatio should be set when physical > 0")
	}

	// Scheduler info — next-run resolved.
	if len(bundle.Scheduler.NextRuns) != 1 {
		t.Errorf("NextRuns len = %d, want 1", len(bundle.Scheduler.NextRuns))
	}

	// Runner status propagated.
	if !bundle.Runner.Active || bundle.Runner.JobID != jobID {
		t.Errorf("Runner not populated: %+v", bundle.Runner)
	}

	// Log tail captured from ring.
	if bundle.LogTail == "" {
		t.Error("LogTail empty after ring write")
	}

	// Database info populated.
	if bundle.Database.Path == "" {
		t.Error("Database.Path empty")
	}
	if bundle.Database.SizeBytes == 0 {
		t.Error("Database.SizeBytes = 0; should be set after writes")
	}
}

// TestCollectorDedupStatsError exercises the dedupFn error branch.
func TestCollectorDedupStatsError(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	if _, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "dedup-dest", Type: "local", Config: `{}`, DedupEnabled: true,
	}); err != nil {
		t.Fatalf("dest: %v", err)
	}
	c := NewCollector(d, nil, "v", nil)
	c.SetDedupStatsFunc(func(_ db.StorageDestination) (chunks, packs, logical, physical, wasted int64, lastGCAt time.Time, lastGCFreed int64, err error) {
		return 0, 0, 0, 0, 0, time.Time{}, 0, errTest{}
	})
	bundle, err := c.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(bundle.DedupStats) != 1 {
		t.Fatalf("DedupStats len = %d", len(bundle.DedupStats))
	}
	if bundle.DedupStats[0].Error == "" {
		t.Errorf("expected error on DedupStats entry, got: %+v", bundle.DedupStats[0])
	}
	// DedupRatio == 1.0 when physical == 0
	if bundle.DedupStats[0].DedupRatio != 1.0 {
		t.Errorf("DedupRatio = %v, want 1.0 when physical=0", bundle.DedupStats[0].DedupRatio)
	}
}

// errTest is a simple error implementation for tests.
type errTest struct{}

func (errTest) Error() string { return "fake dedup error" }

// TestCollectorSkipsDisabledJobInScheduler verifies the "disabled job is
// not surfaced in NextRuns" branch.
func TestCollectorSchedulerSkipsDisabled(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{}`})
	_, _ = d.CreateJob(db.Job{Name: "disabled", Enabled: false, StorageDestID: destID})
	// Also a job with VerifySchedule set (covers the verify-schedule branch).
	_, _ = d.CreateJob(db.Job{
		Name:           "verifyOn",
		Enabled:        true,
		StorageDestID:  destID,
		VerifySchedule: "0 5 * * *",
	})

	c := NewCollector(d, nil, "v", nil)
	c.SetNextRunFunc(func(int64) (string, bool) { return "2030-01-01 00:00:00", true })
	bundle, err := c.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(bundle.Scheduler.NextRuns) != 1 {
		t.Errorf("NextRuns len = %d, want 1 (disabled excluded)", len(bundle.Scheduler.NextRuns))
	}
	if len(bundle.Scheduler.NextVerifyRuns) != 1 {
		t.Errorf("NextVerifyRuns len = %d, want 1", len(bundle.Scheduler.NextVerifyRuns))
	}
}

// TestCollectorNextRunUnresolved exercises the c.nextRunFn(jobID) returning false.
func TestCollectorNextRunUnresolved(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{}`})
	_, _ = d.CreateJob(db.Job{Name: "x", Enabled: true, StorageDestID: destID})
	c := NewCollector(d, nil, "v", nil)
	c.SetNextRunFunc(func(int64) (string, bool) { return "", false })
	bundle, err := c.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(bundle.Scheduler.NextRuns) != 1 {
		t.Fatalf("NextRuns len = %d", len(bundle.Scheduler.NextRuns))
	}
	if bundle.Scheduler.NextRuns[0].NextRun != "" {
		t.Errorf("NextRun = %q, want empty (resolver returned false)", bundle.Scheduler.NextRuns[0].NextRun)
	}
}

// TestCollectorDatabaseHybridFromSetting hits the "snapshot_path_override
// set" branch of collectDatabaseInfo.
func TestCollectorDatabaseHybridMode(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	_ = d.SetSetting("snapshot_path_override", "/mnt/cache/vault")
	c := NewCollector(d, nil, "v", nil)
	bundle, err := c.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if bundle.Database.Mode != "hybrid" {
		t.Errorf("Database.Mode = %q, want hybrid", bundle.Database.Mode)
	}
}

// TestWriteJSONMarshalError covers the marshal-error branch of writeJSON.
// chans are unsupported by encoding/json.
func TestWriteJSONMarshalError(t *testing.T) {
	t.Parallel()
	var buf zipBuf
	w := zip.NewWriter(&buf)
	defer w.Close()
	err := writeJSON(w, "bad.json", make(chan int), time.Now())
	if err == nil {
		t.Errorf("expected marshal error for chan, got nil")
	}
}

// zipBuf adapts bytes.Buffer to io.Writer for zip.NewWriter without dragging
// in the bytes package at the top-level imports (it's local to this file).
type zipBuf struct {
	b []byte
}

func (zb *zipBuf) Write(p []byte) (int, error) {
	zb.b = append(zb.b, p...)
	return len(p), nil
}
