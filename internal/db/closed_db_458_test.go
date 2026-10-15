package db

import (
	"context"
	"testing"
	"time"
)

// closedTestDB returns a DB whose underlying handle has been closed, so every
// repository call takes its error-return branch.
func closedTestDB(t *testing.T) *DB {
	t.Helper()
	d := setupTestDB(t)
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return d
}

// TestRepoMethodsReturnErrorsOnClosedDB_458 drives each repository method
// against a closed handle and asserts the error is surfaced, not swallowed.
func TestRepoMethodsReturnErrorsOnClosedDB_458(t *testing.T) {
	d := closedTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	id := int64(1)

	checks := map[string]func() error{
		"Ping":                  func() error { return d.Ping() },
		"PingContext":           func() error { return d.PingContext(ctx) },
		"IntegrityCheck":        d.IntegrityCheck,
		"Vacuum":                d.Vacuum,
		"insertDefaultSettings": d.insertDefaultSettings,
		"CreateActivityLog": func() error {
			_, err := d.CreateActivityLog(ActivityLogEntry{Level: "info", Category: "x", Message: "m"})
			return err
		},
		"ListActivityLogs":         func() error { _, err := d.ListActivityLogs(10, "cat", 5); return err },
		"DeleteOldActivityLogs":    func() error { return d.DeleteOldActivityLogs(1) },
		"CapActivityLogs":          func() error { return d.CapActivityLogs(1) },
		"InsertOpenAnomaly":        func() error { _, err := d.InsertOpenAnomaly(Anomaly{Fingerprint: "f"}); return err },
		"RefreshOpenAnomaly":       func() error { return d.RefreshOpenAnomaly(id, 1, nil, now, "warning") },
		"GetOpenAnomalyByFinger":   func() error { _, err := d.GetOpenAnomalyByFingerprint("f"); return err },
		"ResolveOpenAnomalies":     func() error { _, err := d.ResolveOpenAnomaliesForRun(id, []string{"warning"}, now); return err },
		"AckAnomaly":               func() error { _, err := d.AckAnomaly(id, "ack", "me", "r", now); return err },
		"BulkAckAnomalies":         func() error { _, _, err := d.BulkAckAnomalies([]int64{1, 2}, "ack", "me", "r", now); return err },
		"ListAnomalies":            func() error { _, err := d.ListAnomalies(AnomalyFilter{}); return err },
		"GetAnomaly":               func() error { _, err := d.GetAnomaly(id); return err },
		"ExpectedFloor":            func() error { _, err := d.ExpectedFloor("f"); return err },
		"MarkAnomalyNotified":      func() error { return d.MarkAnomalyNotified(id, now) },
		"UpsertJobBaseline":        func() error { return d.UpsertJobBaseline(JobBaseline{JobID: id}) },
		"GetJobBaseline":           func() error { _, err := d.GetJobBaseline(id); return err },
		"ListJobBaselines":         func() error { _, err := d.ListJobBaselines(ctx); return err },
		"InsertCapacitySample":     func() error { return d.InsertCapacitySample(CapacitySample{DestID: id}) },
		"ListCapacitySamples":      func() error { _, err := d.ListCapacitySamples(id, now); return err },
		"PruneOldAnomalies":        func() error { _, err := d.PruneOldAnomalies(now); return err },
		"LatestDedupGCRun":         func() error { _, _, err := d.LatestDedupGCRun(id); return err },
		"ListJobs":                 func() error { _, err := d.ListJobs(); return err },
		"GetJobItems":              func() error { _, err := d.GetJobItems(id); return err },
		"ListJobItems":             func() error { _, err := d.ListJobItems(ctx); return err },
		"UpdateJobItemSettings":    func() error { return d.UpdateJobItemSettings(id, "{}") },
		"CleanupStaleRuns":         func() error { _, err := d.CleanupStaleRuns(); return err },
		"CountRunningBackups":      func() error { _, err := d.CountRunningBackupsOnDestination(id); return err },
		"DeleteOldFailedRuns":      func() error { _, err := d.DeleteOldFailedRuns(1); return err },
		"PurgeEligibleRuns":        func() error { _, err := d.PurgeEligibleRuns(1); return err },
		"ListRecentRuns":           func() error { _, err := d.ListRecentRuns(5); return err },
		"ListJobRuns":              func() error { _, err := d.ListJobRuns(ctx, 5); return err },
		"GetJobRun":                func() error { _, err := d.GetJobRun(id); return err },
		"PurgeJobRuns":             func() error { _, err := d.PurgeJobRuns(); return err },
		"GetJobRuns":               func() error { _, err := d.GetJobRuns(id, 5); return err },
		"GetJobRunsSince":          func() error { _, err := d.GetJobRunsSince(id, now); return err },
		"ListRestorePoints":        func() error { _, err := d.ListRestorePoints(id); return err },
		"GetOldRestorePoints":      func() error { _, err := d.GetOldRestorePoints(id, 1); return err },
		"GetExpiredRestorePoints":  func() error { _, err := d.GetExpiredRestorePoints(id, 1); return err },
		"GetRestorePoint":          func() error { _, err := d.GetRestorePoint(id); return err },
		"ClaimDueRetries":          func() error { _, err := d.ClaimDueRetries(); return err },
		"MarkJobRunStalled":        func() error { return d.MarkJobRunStalled(id, "r") },
		"CreateMountSession":       func() error { _, err := d.CreateMountSession(MountSession{JobID: id}); return err },
		"GetMountSession":          func() error { _, err := d.GetMountSession(id); return err },
		"ListMountSessions":        func() error { _, err := d.ListMountSessions(true); return err },
		"UpdateMountSessionStatus": func() error { return d.UpdateMountSessionStatus(id, "stopped", "") },
		"CleanupStaleMountSess":    func() error { _, err := d.CleanupStaleMountSessions(); return err },
		"ListReplicationSources":   func() error { _, err := d.ListReplicationSources(); return err },
		"UpdateReplicationStatus":  func() error { return d.UpdateReplicationSyncStatus(id, "ok", "") },
		"UpdateReplicationResult":  func() error { return d.UpdateReplicationSyncResult(id, "ok", "", 1, 0, 1, 1) },
		"ListReplicatedJobs":       func() error { _, err := d.ListReplicatedJobs(id); return err },
		"AppendRunLog":             func() error { _, err := d.AppendRunLog(ctx, RunLogEntry{RunID: id, Message: "m"}); return err },
		"ListRunLogEntries":        func() error { _, err := d.ListRunLogEntries(ctx, id, 0, 10); return err },
		"TailRunLogEntries":        func() error { _, err := d.TailRunLogEntries(ctx, id, 10); return err },
		"CapRunLogEntriesPerRun":   func() error { return d.CapRunLogEntriesPerRun(ctx, 10) },
		"GetSettingInt":            func() error { _, err := d.GetSettingInt("k", 1); return err },
		"GetSettingBool":           func() error { _, err := d.GetSettingBool("k", true); return err },
		"GetAllSettings":           func() error { _, err := d.GetAllSettings(); return err },
		"ValidateHasConfiguration": func() error { _, err := d.ValidateHasConfiguration(ctx); return err },
		"ListStorageDestinations":  func() error { _, err := d.ListStorageDestinations(); return err },
		"DeleteStorageDestination": func() error { return d.DeleteStorageDestination(id) },
		"ListDBBackupDests":        func() error { _, err := d.ListDBBackupDestinations(); return err },
		"DisableJobsByStorageDest": func() error { _, err := d.DisableJobsByStorageDestID(id); return err },
		"DeleteDedupState":         func() error { return d.DeleteDedupState(id) },
		"ListJobsByStorageDestID":  func() error { _, err := d.ListJobsByStorageDestID(id); return err },
		"HasDedupChunk":            func() error { _, err := d.HasDedupChunk(id, []byte{1}); return err },
		"DedupAggregates":          func() error { _, err := d.DedupAggregates(id); return err },
		"DropDedupState":           func() error { return d.DropDedupState(id) },
		"ListDedupPacks":           func() error { _, err := d.ListDedupPacks(id); return err },
		"SetDedupPackDeleteState":  func() error { return d.SetDedupPackDeleteState(id, "p", 1) },
		"ListDedupChunksByPack":    func() error { _, err := d.ListDedupChunksByPack(id, "p"); return err },
		"RepointDedupChunks":       func() error { return d.RepointDedupChunks(id, []DedupChunk{{ChunkID: []byte{1}}}) },
		"GetVerifyRun":             func() error { _, err := d.GetVerifyRun(id); return err },
		"ListRecentVerifyRuns":     func() error { _, err := d.ListRecentVerifyRuns(5); return err },
		"ListVerifyRunsForJob":     func() error { _, err := d.ListVerifyRunsForJob(id, 5); return err },
		"ListVerifyRunsForRP":      func() error { _, err := d.ListVerifyRunsForRestorePoint(id, 5); return err },
	}
	for name, fn := range checks {
		if err := fn(); err == nil {
			t.Errorf("%s: expected error on closed DB, got nil", name)
		}
	}
}

func TestDefaultJob_458(t *testing.T) {
	j := DefaultJob()
	if !j.Enabled || j.BackupTypeChain != "full" || j.RetentionCount != 5 || j.RetentionDays != 30 ||
		j.Compression != "zstd" || j.Encryption != "none" || j.ContainerMode != "one_by_one" ||
		j.NotifyOn != "failure" || !j.VerifyBackup {
		t.Fatalf("DefaultJob() = %+v", j)
	}
	if j.Name != "" || j.StorageDestID != 0 {
		t.Fatalf("DefaultJob should leave Name/StorageDestID zero: %+v", j)
	}
}

func TestPingAndGetSettingBool_458(t *testing.T) {
	d := setupTestDB(t)
	if err := d.Ping(); err != nil {
		t.Fatal(err)
	}
	if err := d.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v, err := d.GetSettingBool("absent-bool", true); err != nil || !v {
		t.Fatalf("absent = %v, %v", v, err)
	}
	for in, want := range map[string]bool{"true": true, "1": true, "no": false} {
		if err := d.SetSetting("b", in); err != nil {
			t.Fatal(err)
		}
		if v, err := d.GetSettingBool("b", !want); err != nil || v != want {
			t.Fatalf("GetSettingBool(%q) = %v, %v; want %v", in, v, err, want)
		}
	}
}

func TestValidateHasConfiguration_458(t *testing.T) {
	d := setupTestDB(t)
	ctx := context.Background()
	s, err := d.ValidateHasConfiguration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.HasConfiguration || s.Note == "" || s.Settings == 0 {
		t.Fatalf("fresh summary = %+v", s)
	}
	if _, err := d.CreateStorageDestination(StorageDestination{Name: "s", Type: "local", Config: "{}"}); err != nil {
		t.Fatal(err)
	}
	s, err = d.ValidateHasConfiguration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasConfiguration || s.StorageDests != 1 || s.Note != "" {
		t.Fatalf("configured summary = %+v", s)
	}

	// Missing tables surface each COUNT error in turn.
	for _, tbl := range []string{"settings", "storage_destinations", "jobs"} {
		if _, err := d.Exec("PRAGMA foreign_keys=OFF"); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec("DROP TABLE IF EXISTS " + tbl); err != nil {
			t.Fatalf("drop %s: %v", tbl, err)
		}
		if _, err := d.ValidateHasConfiguration(ctx); err == nil {
			t.Fatalf("expected error after dropping %s", tbl)
		}
	}
}

func TestJobRepoZeroCoverageHappyPaths_458(t *testing.T) {
	d := setupTestDB(t)
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "dest", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(Job{Name: "j", Enabled: true, StorageDestID: destID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddJobItem(JobItem{JobID: jobID, ItemType: "folder", ItemName: "a"}); err != nil {
		t.Fatal(err)
	}
	items, err := d.ListJobItems(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("ListJobItems = %v, %v", items, err)
	}

	runID, err := d.CreateJobRun(JobRun{JobID: jobID, Status: "running", RunType: "backup"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MarkJobRunStalled(runID, "no progress"); err != nil {
		t.Fatal(err)
	}
	// Second call is a no-op: the first stall timestamp is preserved.
	if err := d.MarkJobRunStalled(runID, "later"); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := d.QueryRow("SELECT stall_reason FROM job_runs WHERE id = ?", runID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "no progress" {
		t.Fatalf("stall_reason = %q, want first reason", reason)
	}

	n, err := d.DisableJobsByStorageDestID(destID)
	if err != nil || n != 1 {
		t.Fatalf("DisableJobsByStorageDestID = %d, %v", n, err)
	}
	j, err := d.GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Enabled || j.StorageDestID != 0 {
		t.Fatalf("job after disable = enabled %v dest %d", j.Enabled, j.StorageDestID)
	}

	if err := d.UpsertJobBaseline(JobBaseline{JobID: jobID, SampleCount: 3, BytesMedian: 10}); err != nil {
		t.Fatal(err)
	}
	bl, err := d.ListJobBaselines(context.Background())
	if err != nil || len(bl) != 1 || bl[0].JobID != jobID || bl[0].SampleCount != 3 {
		t.Fatalf("ListJobBaselines = %+v, %v", bl, err)
	}
}

func TestSetDedupPackDeleteState_458(t *testing.T) {
	d := setupTestDB(t)
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "dd", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDedupPack(DedupPack{ID: "p1", StorageID: destID, Path: "packs/p1", SizeBytes: 10}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetDedupPackDeleteState(destID, "p1", PackTombstoned); err != nil {
		t.Fatal(err)
	}
	// Monotonic: a lower state never overwrites a higher one.
	if err := d.SetDedupPackDeleteState(destID, "p1", PackMarked); err != nil {
		t.Fatal(err)
	}
	var state int
	if err := d.QueryRow("SELECT pending_delete FROM dedup_packs WHERE storage_id = ? AND id = ?", destID, "p1").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != PackTombstoned {
		t.Fatalf("pending_delete = %d, want %d", state, PackTombstoned)
	}
}
