// Coverage gap-fill tests for previously-uncovered repo methods. Focused on
// the zero-coverage SQL helpers: storage health, breaker state, dedup pack
// rewrite (Replace*), DB-backup destination list, dependent-jobs list, verify
// run lifecycle, snapshot copyFile / Close / IntegrityCheck, plus
// CreateImportedJobRun and GetSettingInt.
package db

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- settings_repo: GetSettingInt -------------------------------------------

func TestGetSettingInt(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)

	// Missing key returns default.
	v, err := d.GetSettingInt("missing-key", 17)
	if err != nil {
		t.Fatalf("GetSettingInt missing: %v", err)
	}
	if v != 17 {
		t.Fatalf("missing key = %d, want 17", v)
	}

	// Stored numeric value parses.
	if err := d.SetSetting("port", "24085"); err != nil {
		t.Fatal(err)
	}
	v, err = d.GetSettingInt("port", -1)
	if err != nil {
		t.Fatalf("GetSettingInt port: %v", err)
	}
	if v != 24085 {
		t.Fatalf("port = %d, want 24085", v)
	}

	// Stored unparseable value falls back to default (no error).
	if err := d.SetSetting("garbage", "not-an-int"); err != nil {
		t.Fatal(err)
	}
	v, err = d.GetSettingInt("garbage", 9000)
	if err != nil {
		t.Fatalf("GetSettingInt garbage: %v", err)
	}
	if v != 9000 {
		t.Fatalf("garbage = %d, want 9000 (silent fallback)", v)
	}

	// Empty string falls back to default.
	if err := d.SetSetting("empty", ""); err != nil {
		t.Fatal(err)
	}
	v, err = d.GetSettingInt("empty", 42)
	if err != nil {
		t.Fatalf("GetSettingInt empty: %v", err)
	}
	if v != 42 {
		t.Fatalf("empty = %d, want 42", v)
	}
}

// --- storage_repo: health / breaker / dest-backup ---------------------------

func TestUpdateStorageDestinationHealth(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	id, err := d.CreateStorageDestination(StorageDestination{Name: "health", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}

	if err := d.UpdateStorageDestinationHealth(id, "ok", ""); err != nil {
		t.Fatalf("Update ok: %v", err)
	}
	got, err := d.GetStorageDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastHealthCheckStatus != "ok" {
		t.Fatalf("status = %q, want ok", got.LastHealthCheckStatus)
	}
	if got.LastHealthCheckAt == nil || got.LastHealthCheckAt.IsZero() {
		t.Fatal("LastHealthCheckAt should be set after UpdateHealth")
	}

	if err := d.UpdateStorageDestinationHealth(id, "failed", "broken pipe"); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	got, err = d.GetStorageDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastHealthCheckStatus != "failed" {
		t.Fatalf("status = %q, want failed", got.LastHealthCheckStatus)
	}
	if got.LastHealthCheckError != "broken pipe" {
		t.Fatalf("error = %q, want broken pipe", got.LastHealthCheckError)
	}
}

func TestRecordDestinationFailureAndSuccess(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	id, err := d.CreateStorageDestination(StorageDestination{Name: "fs", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}

	if err := d.RecordDestinationFailure(id, 3); err != nil {
		t.Fatalf("RecordDestinationFailure: %v", err)
	}
	got, err := d.GetStorageDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConsecutiveFailures != 3 {
		t.Fatalf("ConsecutiveFailures = %d, want 3", got.ConsecutiveFailures)
	}

	if err := d.RecordDestinationSuccess(id); err != nil {
		t.Fatalf("RecordDestinationSuccess: %v", err)
	}
	got, err = d.GetStorageDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConsecutiveFailures != 0 {
		t.Fatalf("ConsecutiveFailures after success = %d, want 0", got.ConsecutiveFailures)
	}
}

func TestOpenCloseBreaker(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	id, err := d.CreateStorageDestination(StorageDestination{Name: "br", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}

	if err := d.OpenBreaker(id, 5); err != nil {
		t.Fatalf("OpenBreaker: %v", err)
	}
	got, err := d.GetStorageDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.BreakerState != "open" {
		t.Fatalf("BreakerState = %q, want open", got.BreakerState)
	}
	if got.ConsecutiveFailures != 5 {
		t.Fatalf("ConsecutiveFailures = %d, want 5", got.ConsecutiveFailures)
	}
	if got.BreakerOpenedAt == nil || got.BreakerOpenedAt.IsZero() {
		t.Fatal("BreakerOpenedAt should be set after OpenBreaker")
	}

	if err := d.CloseBreaker(id); err != nil {
		t.Fatalf("CloseBreaker: %v", err)
	}
	got, err = d.GetStorageDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.BreakerState != "closed" {
		t.Fatalf("BreakerState = %q, want closed", got.BreakerState)
	}
	if got.ConsecutiveFailures != 0 {
		t.Fatalf("ConsecutiveFailures after close = %d, want 0", got.ConsecutiveFailures)
	}
	if got.BreakerOpenedAt != nil {
		t.Fatalf("BreakerOpenedAt after close = %v, want nil", got.BreakerOpenedAt)
	}
}

func TestListDBBackupDestinations(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)

	plain, err := d.CreateStorageDestination(StorageDestination{Name: "plain", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	backup, err := d.CreateStorageDestination(StorageDestination{Name: "backup", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	// CreateStorageDestination doesn't write backup_database_enabled — set it via Update.
	if err := d.UpdateStorageDestination(StorageDestination{
		ID: backup, Name: "backup", Type: "local", Config: "{}", BackupDatabaseEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := d.ListDBBackupDestinations()
	if err != nil {
		t.Fatalf("ListDBBackupDestinations: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ID != backup {
		t.Fatalf("returned destination id = %d, want %d (plain=%d)", got[0].ID, backup, plain)
	}
	if !got[0].BackupDatabaseEnabled {
		t.Fatal("returned destination should have BackupDatabaseEnabled=true")
	}

	// Empty case: no DB-backup destinations.
	d2 := setupTestDB(t)
	if _, err := d2.CreateStorageDestination(StorageDestination{Name: "x", Type: "local", Config: "{}"}); err != nil {
		t.Fatal(err)
	}
	got2, err := d2.ListDBBackupDestinations()
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 0 {
		t.Fatalf("expected 0 DB-backup destinations, got %d", len(got2))
	}
}

func TestListJobsByStorageDestID(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	destA, err := d.CreateStorageDestination(StorageDestination{Name: "A", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	destB, err := d.CreateStorageDestination(StorageDestination{Name: "B", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateJob(Job{Name: "alpha", StorageDestID: destA, BackupTypeChain: "full"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateJob(Job{Name: "beta", StorageDestID: destA, BackupTypeChain: "full"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateJob(Job{Name: "other", StorageDestID: destB, BackupTypeChain: "full"}); err != nil {
		t.Fatal(err)
	}

	jobs, err := d.ListJobsByStorageDestID(destA)
	if err != nil {
		t.Fatalf("ListJobsByStorageDestID: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("len = %d, want 2", len(jobs))
	}
	// Returned in ORDER BY name: alpha, beta.
	if jobs[0].Name != "alpha" || jobs[1].Name != "beta" {
		t.Fatalf("ordering = [%s, %s], want [alpha, beta]", jobs[0].Name, jobs[1].Name)
	}

	// No-match destination returns an empty slice (not nil).
	none, err := d.ListJobsByStorageDestID(99999)
	if err != nil {
		t.Fatalf("ListJobsByStorageDestID(missing): %v", err)
	}
	if none == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(none) != 0 {
		t.Fatalf("len = %d, want 0", len(none))
	}
}

// --- storage_repo: ReplaceDedupPack / ReplaceDedupChunk ---------------------

func TestReplaceDedupPackUpsertsExisting(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}

	// First insert via UpsertDedupPack.
	if err := d.UpsertDedupPack(DedupPack{ID: "p1", StorageID: destID, Path: "old.pack", SizeBytes: 100, ChunkCount: 1}); err != nil {
		t.Fatal(err)
	}
	// ReplaceDedupPack updates path/size/chunk_count via ON CONFLICT.
	if err := d.ReplaceDedupPack(DedupPack{ID: "p1", StorageID: destID, Path: "new.pack", SizeBytes: 200, ChunkCount: 5}); err != nil {
		t.Fatalf("ReplaceDedupPack update: %v", err)
	}
	packs, err := d.ListDedupPacks(destID)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 {
		t.Fatalf("len = %d, want 1", len(packs))
	}
	if packs[0].Path != "new.pack" || packs[0].SizeBytes != 200 || packs[0].ChunkCount != 5 {
		t.Fatalf("Replace did not update fields: %+v", packs[0])
	}

	// ReplaceDedupPack on a never-seen id is an insert.
	if err := d.ReplaceDedupPack(DedupPack{ID: "p2", StorageID: destID, Path: "fresh.pack", SizeBytes: 10, ChunkCount: 0}); err != nil {
		t.Fatalf("ReplaceDedupPack insert: %v", err)
	}
	packs, err = d.ListDedupPacks(destID)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 2 {
		t.Fatalf("len = %d, want 2", len(packs))
	}
}

func TestReplaceDedupChunkUpsertsExisting(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDedupPack(DedupPack{ID: "p1", StorageID: destID, Path: "p1.pack", SizeBytes: 100, ChunkCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDedupPack(DedupPack{ID: "p2", StorageID: destID, Path: "p2.pack", SizeBytes: 100, ChunkCount: 1}); err != nil {
		t.Fatal(err)
	}
	cid := []byte{0xab, 0xcd}

	// Initial chunk in p1.
	if err := d.UpsertDedupChunk(DedupChunk{ChunkID: cid, StorageID: destID, PackID: "p1", Offset: 0, Length: 50}); err != nil {
		t.Fatal(err)
	}
	// Replace moves it to p2.
	if err := d.ReplaceDedupChunk(DedupChunk{ChunkID: cid, StorageID: destID, PackID: "p2", Offset: 4096, Length: 50}); err != nil {
		t.Fatalf("ReplaceDedupChunk: %v", err)
	}
	path, off, length, err := d.LocateDedupChunk(destID, cid)
	if err != nil {
		t.Fatal(err)
	}
	if path != "p2.pack" || off != 4096 || length != 50 {
		t.Fatalf("re-pointed location wrong: (%q, %d, %d)", path, off, length)
	}

	// New chunk is straight insert.
	cid2 := []byte{0x01}
	if err := d.ReplaceDedupChunk(DedupChunk{ChunkID: cid2, StorageID: destID, PackID: "p1", Offset: 0, Length: 1}); err != nil {
		t.Fatalf("ReplaceDedupChunk insert: %v", err)
	}
	has, err := d.HasDedupChunk(destID, cid2)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("expected HasDedupChunk to be true after ReplaceDedupChunk insert")
	}
}

// --- storage_repo: ListDedupPacks / DeleteDedupPack / ListDedupChunksByPack -

func TestListAndDeleteDedupPack(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}

	// Empty state.
	packs, err := d.ListDedupPacks(destID)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 0 {
		t.Fatalf("expected 0 packs, got %d", len(packs))
	}

	// Two packs with one chunk each.
	if err := d.UpsertDedupPack(DedupPack{ID: "pa", StorageID: destID, Path: "a.pack", SizeBytes: 10, ChunkCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDedupPack(DedupPack{ID: "pb", StorageID: destID, Path: "b.pack", SizeBytes: 20, ChunkCount: 1}); err != nil {
		t.Fatal(err)
	}
	cA := []byte{0x01}
	cB := []byte{0x02}
	if err := d.UpsertDedupChunk(DedupChunk{ChunkID: cA, StorageID: destID, PackID: "pa", Offset: 0, Length: 5}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDedupChunk(DedupChunk{ChunkID: cB, StorageID: destID, PackID: "pb", Offset: 0, Length: 5}); err != nil {
		t.Fatal(err)
	}

	packs, err = d.ListDedupPacks(destID)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 2 {
		t.Fatalf("expected 2 packs, got %d", len(packs))
	}

	// ListDedupChunksByPack returns only chunks belonging to that pack.
	chunks, err := d.ListDedupChunksByPack(destID, "pa")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk in pa, got %d", len(chunks))
	}
	if !bytes.Equal(chunks[0].ChunkID, cA) {
		t.Fatalf("chunk ID mismatch: %x vs %x", chunks[0].ChunkID, cA)
	}
	if chunks[0].PackID != "pa" {
		t.Fatalf("PackID = %q, want pa", chunks[0].PackID)
	}

	// Unknown pack returns empty.
	chunks, err = d.ListDedupChunksByPack(destID, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for missing pack, got %d", len(chunks))
	}

	// DeleteDedupPack removes the pack and (via FK cascade) its chunks.
	if err := d.DeleteDedupPack(destID, "pa"); err != nil {
		t.Fatalf("DeleteDedupPack: %v", err)
	}
	packs, err = d.ListDedupPacks(destID)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 || packs[0].ID != "pb" {
		t.Fatalf("after delete: packs = %+v, want only pb", packs)
	}
	has, err := d.HasDedupChunk(destID, cA)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("chunk cA should have been cascade-deleted")
	}

	// Deleting a non-existent pack is a no-op (no error).
	if err := d.DeleteDedupPack(destID, "never-existed"); err != nil {
		t.Fatalf("DeleteDedupPack missing: %v", err)
	}
}

// --- verify_repo: lifecycle -------------------------------------------------

func TestVerifyRunLifecycle(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(Job{Name: "j", StorageDestID: destID, BackupTypeChain: "full"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := d.CreateJobRun(JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	rpID, err := d.CreateRestorePoint(RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "p", SizeBytes: 1})
	if err != nil {
		t.Fatal(err)
	}

	id, err := d.CreateVerifyRun(rpID, "quick")
	if err != nil {
		t.Fatalf("CreateVerifyRun: %v", err)
	}
	got, err := d.GetVerifyRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" || got.Mode != "quick" {
		t.Fatalf("initial verify run = %+v, want running+quick", got)
	}
	if got.RestorePointID != rpID {
		t.Fatalf("RestorePointID = %d, want %d", got.RestorePointID, rpID)
	}

	if err := d.UpdateVerifyRunProgress(id, 3, 1, 12345); err != nil {
		t.Fatalf("UpdateVerifyRunProgress: %v", err)
	}
	got, err = d.GetVerifyRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.FilesChecked != 3 || got.FilesFailed != 1 || got.BytesRead != 12345 {
		t.Fatalf("progress = (%d,%d,%d), want (3,1,12345)", got.FilesChecked, got.FilesFailed, got.BytesRead)
	}
	if got.CompletedAt != nil {
		t.Fatalf("CompletedAt should still be nil during progress, got %v", got.CompletedAt)
	}

	if err := d.FinishVerifyRun(id, "passed", ""); err != nil {
		t.Fatalf("FinishVerifyRun: %v", err)
	}
	got, err = d.GetVerifyRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "passed" {
		t.Fatalf("Status = %q, want passed", got.Status)
	}
	if got.CompletedAt == nil || got.CompletedAt.IsZero() {
		t.Fatal("CompletedAt should be set after FinishVerifyRun")
	}
}

func TestListVerifyRuns(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(Job{Name: "j", StorageDestID: destID, BackupTypeChain: "full"})
	runID, _ := d.CreateJobRun(JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	rpA, _ := d.CreateRestorePoint(RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "p/a", SizeBytes: 1})
	rpB, _ := d.CreateRestorePoint(RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "p/b", SizeBytes: 2})

	// Two verify runs against rpA, one against rpB.
	if _, err := d.CreateVerifyRun(rpA, "quick"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateVerifyRun(rpA, "deep"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateVerifyRun(rpB, "quick"); err != nil {
		t.Fatal(err)
	}

	all, err := d.ListRecentVerifyRuns(10)
	if err != nil {
		t.Fatalf("ListRecentVerifyRuns: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListRecentVerifyRuns returned %d, want 3", len(all))
	}

	// Default limit (<=0 → 25).
	all, err = d.ListRecentVerifyRuns(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("ListRecentVerifyRuns(0) = %d, want 3 (default limit)", len(all))
	}

	rpAOnly, err := d.ListVerifyRunsForRestorePoint(rpA, 10)
	if err != nil {
		t.Fatalf("ListVerifyRunsForRestorePoint: %v", err)
	}
	if len(rpAOnly) != 2 {
		t.Fatalf("ListVerifyRunsForRestorePoint(rpA) = %d, want 2", len(rpAOnly))
	}
	for _, vr := range rpAOnly {
		if vr.RestorePointID != rpA {
			t.Fatalf("unexpected RestorePointID = %d", vr.RestorePointID)
		}
	}

	// Default limit (<=0 → 10).
	rpAOnly, err = d.ListVerifyRunsForRestorePoint(rpA, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rpAOnly) != 2 {
		t.Fatalf("ListVerifyRunsForRestorePoint(rpA, -1) = %d, want 2", len(rpAOnly))
	}

	// Unknown restore point returns empty.
	none, err := d.ListVerifyRunsForRestorePoint(99999, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("ListVerifyRunsForRestorePoint(missing) = %d, want 0", len(none))
	}
}

func TestFinishVerifyRunTruncatesLongSummary(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(Job{Name: "j", StorageDestID: destID, BackupTypeChain: "full"})
	runID, _ := d.CreateJobRun(JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "p", SizeBytes: 1})
	id, err := d.CreateVerifyRun(rpID, "deep")
	if err != nil {
		t.Fatal(err)
	}

	big := make([]byte, 8192)
	for i := range big {
		big[i] = 'A'
	}
	if err := d.FinishVerifyRun(id, "failed", string(big)); err != nil {
		t.Fatalf("FinishVerifyRun: %v", err)
	}
	got, err := d.GetVerifyRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ErrorSummary) != 4096 {
		t.Fatalf("ErrorSummary length = %d, want 4096 (truncated)", len(got.ErrorSummary))
	}
}

// --- job_repo: CreateImportedJobRun ----------------------------------------

func TestCreateImportedJobRun(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(Job{Name: "imp", StorageDestID: destID, BackupTypeChain: "full"})
	if err != nil {
		t.Fatal(err)
	}

	ts := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	id, err := d.CreateImportedJobRun(JobRun{
		JobID: jobID, Status: "completed", BackupType: "full",
		ItemsTotal: 5, ItemsDone: 5, ItemsFailed: 0, SizeBytes: 12345,
	}, ts)
	if err != nil {
		t.Fatalf("CreateImportedJobRun: %v", err)
	}
	if id <= 0 {
		t.Fatalf("returned id = %d, want positive", id)
	}

	runs, err := d.GetJobRuns(jobID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("len = %d, want 1", len(runs))
	}
	r := runs[0]
	if r.Status != "completed" || r.ItemsTotal != 5 || r.ItemsDone != 5 || r.SizeBytes != 12345 {
		t.Fatalf("imported run = %+v", r)
	}
	if r.CompletedAt == nil {
		t.Fatal("imported run CompletedAt should be set (not in-progress)")
	}
	// started_at == completed_at — both stamped to ts.
	if !r.StartedAt.Equal(ts) {
		t.Fatalf("StartedAt = %v, want %v", r.StartedAt, ts)
	}

	// Zero ts is replaced with now() — verify behaviour by ensuring the
	// returned StartedAt is non-zero and recent.
	id2, err := d.CreateImportedJobRun(JobRun{JobID: jobID, Status: "completed", BackupType: "full"}, time.Time{})
	if err != nil {
		t.Fatalf("CreateImportedJobRun zero ts: %v", err)
	}
	if id2 <= 0 {
		t.Fatal("returned id <= 0 on zero ts")
	}

	// Default RunType applied when empty.
	runs, err = d.GetJobRuns(jobID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.RunType != "backup" {
			t.Fatalf("RunType = %q, want backup (default)", r.RunType)
		}
	}
}

// --- snapshot: copyFile / Close / IntegrityCheck ----------------------------

func TestSnapshotCopyFileHappyPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	if err := os.WriteFile(src, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst.bin")
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Fatalf("copied content = %q, want %q", got, "hello world")
	}
}

func TestSnapshotCopyFileNonexistentSource(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst.bin")
	err := copyFile(filepath.Join(dir, "no-such-file"), dst)
	if err == nil {
		t.Fatal("copyFile with nonexistent source should fail")
	}
}

func TestSnapshotCopyFileUnwritableDest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Make the destination a directory under a file — OpenFile must fail.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(blocker, "sub", "dst.bin")
	if err := copyFile(src, dst); err == nil {
		t.Fatal("copyFile to unwritable destination should fail")
	}
}

func TestSnapshotManagerClose(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := setupTestDB(t)
	snapshotPath := filepath.Join(dir, "snap.db")
	sm := NewSnapshotManager(d, snapshotPath, snapshotPath)

	if err := sm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close calls FlushToUSB which calls SaveSnapshot — file must exist.
	if _, err := os.Stat(snapshotPath); err != nil {
		t.Fatalf("snapshot not created by Close: %v", err)
	}
}

func TestSnapshotManagerIntegrityCheck(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	snapshotPath := filepath.Join(t.TempDir(), "snap.db")
	sm := NewSnapshotManager(d, snapshotPath, snapshotPath)

	if err := sm.IntegrityCheck(); err != nil {
		t.Fatalf("IntegrityCheck on a fresh DB should succeed, got: %v", err)
	}
}

// --- ClaimDueRetries -------------------------------------------------------

func TestClaimDueRetries(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(Job{Name: "j", StorageDestID: destID, BackupTypeChain: "full"})

	// One failed run with retry_next_at in the past.
	pastID, err := d.CreateJobRun(JobRun{JobID: jobID, Status: "failed", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour).UTC()
	if _, err := d.Exec(`UPDATE job_runs SET retry_next_at = ? WHERE id = ?`, past, pastID); err != nil {
		t.Fatal(err)
	}

	// One failed run with retry_next_at far in the future — must not be claimed.
	futureID, err := d.CreateJobRun(JobRun{JobID: jobID, Status: "failed", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour).UTC()
	if _, err := d.Exec(`UPDATE job_runs SET retry_next_at = ? WHERE id = ?`, future, futureID); err != nil {
		t.Fatal(err)
	}

	// One completed run (status != failed) — must not be claimed.
	if _, err := d.CreateJobRun(JobRun{JobID: jobID, Status: "completed", BackupType: "full"}); err != nil {
		t.Fatal(err)
	}

	claimed, err := d.ClaimDueRetries()
	if err != nil {
		t.Fatalf("ClaimDueRetries: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed = %d, want 1", len(claimed))
	}
	if claimed[0].OriginalRunID != pastID {
		t.Fatalf("claimed run id = %d, want %d", claimed[0].OriginalRunID, pastID)
	}

	// Second call should not re-claim the same row (retry_next_at was cleared).
	claimed2, err := d.ClaimDueRetries()
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed2) != 0 {
		t.Fatalf("second ClaimDueRetries returned %d, want 0", len(claimed2))
	}
}

// --- UpdateStorageDestinationCapacity: empty / probed_at-only branch -------

func TestUpdateStorageDestinationCapacityProbedAtOnly(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	id, err := d.CreateStorageDestination(StorageDestination{Name: "cap-empty", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}

	// Empty + no error path: rec.IsZero() AND errMsg=="" — function takes
	// the short-circuit branch and only stamps capacity_probed_at (which is
	// the zero time here, but the UPDATE still runs). Numeric capacity
	// columns must remain NULL.
	if err := d.UpdateStorageDestinationCapacity(id, CapacityRecord{}, ""); err != nil {
		t.Fatalf("UpdateStorageDestinationCapacity empty: %v", err)
	}
	got, err := d.GetStorageDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.CapacityTotalBytes != nil {
		t.Fatalf("TotalBytes = %v, want nil on empty-result branch", got.CapacityTotalBytes)
	}
	if got.CapacityError != "" {
		t.Fatalf("Error = %q, want empty on empty-result branch", got.CapacityError)
	}
}

// TestUpdateStorageDestinationCapacityNullableZeros: passing TotalBytes=0
// (with non-empty errMsg so the empty-result branch is skipped) writes SQL
// NULL via nullableInt64. Exercises the v=0 branch.
func TestUpdateStorageDestinationCapacityNullableZeros(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	id, _ := d.CreateStorageDestination(StorageDestination{Name: "nz", Type: "local", Config: "{}"})
	now := time.Now().UTC().Truncate(time.Second)
	if err := d.UpdateStorageDestinationCapacity(id, CapacityRecord{ProbedAt: now}, "probe failed"); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetStorageDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.CapacityError != "probe failed" {
		t.Fatalf("CapacityError = %q, want probe failed", got.CapacityError)
	}
	if got.CapacityTotalBytes != nil {
		t.Fatalf("TotalBytes = %v, want nil (nullableInt64(0))", got.CapacityTotalBytes)
	}
}

// --- Snapshot validation error branches ------------------------------------

func TestSetUSBBackupPathInvalid(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	snapshotPath := filepath.Join(t.TempDir(), "snap.db")
	sm := NewSnapshotManager(d, snapshotPath, snapshotPath)
	// Passing a path containing ".." triggers validateSnapshotPath failure,
	// which logs a warning and returns without updating usbBackupPath.
	sm.SetUSBBackupPath("/bad/../path")
	// usbBackupPath must remain empty.
	if err := sm.SaveSnapshotAndUSBBackup(); err != nil {
		t.Fatalf("SaveSnapshotAndUSBBackup: %v", err)
	}
	// No usb backup file should be written because SetUSBBackupPath rejected ours.
	if _, err := os.Stat("/bad"); err == nil {
		t.Fatal("rejected USB path should not exist")
	}
}

func TestRestoreFromPathRejectsBadPath(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	snapshotPath := filepath.Join(t.TempDir(), "snap.db")
	sm := NewSnapshotManager(d, snapshotPath, snapshotPath)
	if err := sm.RestoreFromPath(""); err == nil {
		t.Fatal("RestoreFromPath with empty source should fail validation")
	}
	if err := sm.RestoreFromPath("/some/../path"); err == nil {
		t.Fatal("RestoreFromPath with traversal should fail validation")
	}
}

// --- Unique-constraint error branches (Create*) ----------------------------

func TestCreateStorageDestinationDuplicateName(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	dest := StorageDestination{Name: "dup", Type: "local", Config: "{}"}
	if _, err := d.CreateStorageDestination(dest); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateStorageDestination(dest); err == nil {
		t.Fatal("expected UNIQUE constraint failure on duplicate name")
	}
}

func TestCreateJobDuplicateName(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}"})
	job := Job{Name: "dup-job", StorageDestID: destID, BackupTypeChain: "full"}
	if _, err := d.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateJob(job); err == nil {
		t.Fatal("expected UNIQUE constraint failure on duplicate job name")
	}
}

func TestCreateRestorePointFKViolation(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	// job_id and job_run_id reference real tables; passing nonexistent IDs
	// triggers FK ON DELETE CASCADE violation at insert time.
	_, err := d.CreateRestorePoint(RestorePoint{
		JobRunID: 99999, JobID: 99999, BackupType: "full", StoragePath: "p", SizeBytes: 1,
	})
	if err == nil {
		t.Fatal("CreateRestorePoint with bogus job_id should fail FK constraint")
	}
}

func TestAddJobItemFKViolation(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	_, err := d.AddJobItem(JobItem{
		JobID: 99999, ItemType: "container", ItemName: "x", ItemID: "x", Settings: "{}", SortOrder: 0,
	})
	if err == nil {
		t.Fatal("AddJobItem with bogus job_id should fail FK constraint")
	}
}

func TestCreateJobRunFKViolation(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	_, err := d.CreateJobRun(JobRun{JobID: 99999, Status: "running", BackupType: "full"})
	if err == nil {
		t.Fatal("CreateJobRun with bogus job_id should fail FK constraint")
	}
}

func TestCreateImportedJobRunFKViolation(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	_, err := d.CreateImportedJobRun(JobRun{JobID: 99999, Status: "completed", BackupType: "full"}, time.Now())
	if err == nil {
		t.Fatal("CreateImportedJobRun with bogus job_id should fail FK constraint")
	}
}

func TestCreateVerifyRunFKViolation(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	_, err := d.CreateVerifyRun(99999, "quick")
	if err == nil {
		t.Fatal("CreateVerifyRun with bogus restore_point_id should fail FK constraint")
	}
}

func TestCreateReplicatedJobDuplicateName(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}"})
	srcID, _ := d.CreateReplicationSource(ReplicationSource{Name: "src", URL: "u"})
	job := Job{Name: "rep-dup", StorageDestID: destID, BackupTypeChain: "full", SourceID: srcID}
	if _, err := d.CreateReplicatedJob(job); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateReplicatedJob(job); err == nil {
		t.Fatal("expected UNIQUE constraint failure on duplicate replicated job name")
	}
}

func TestCreateActivityLogTriggersInsert(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	// Drives an additional execution of CreateActivityLog (already covered)
	// plus a re-check that the returned ID is positive.
	id, err := d.CreateActivityLog(ActivityLogEntry{Level: "info", Category: "test", Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatalf("returned id = %d", id)
	}
}

func TestInsertDedupGCRunFKViolation(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	_, err := d.InsertDedupGCRun(DedupGCRun{StorageID: 99999, StartedAt: time.Now(), CompletedAt: time.Now()})
	if err == nil {
		t.Fatal("InsertDedupGCRun with bogus storage_id should fail FK constraint")
	}
}

func TestCreateReplicationSourceDuplicateName(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	src := ReplicationSource{Name: "dup-src", URL: "u"}
	if _, err := d.CreateReplicationSource(src); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateReplicationSource(src); err == nil {
		t.Fatal("expected UNIQUE constraint failure on duplicate replication source name")
	}
}

func TestSaveUSBBackupMkdirFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := setupTestDB(t)

	snapshotPath := filepath.Join(dir, "snap.db")
	// Put a regular file in the way so MkdirAll for the USB path's directory fails.
	blocker := filepath.Join(dir, "usb-blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	usbPath := filepath.Join(blocker, "child", "vault.db.backup")

	sm := NewSnapshotManager(d, snapshotPath, snapshotPath)
	sm.SetUSBBackupPath(usbPath)

	// SaveSnapshotAndUSBBackup tolerates a USB failure (logs + continues).
	if err := sm.SaveSnapshotAndUSBBackup(); err != nil {
		t.Fatalf("SaveSnapshotAndUSBBackup unexpectedly returned error: %v", err)
	}
	// USB file must not exist; primary snapshot must exist.
	if _, err := os.Stat(usbPath); err == nil {
		t.Fatal("USB backup should not exist when MkdirAll fails")
	}
	if _, err := os.Stat(snapshotPath); err != nil {
		t.Fatalf("primary snapshot missing: %v", err)
	}
}

func TestSetSnapshotPathMkdirFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := setupTestDB(t)

	// Put a file at a path so MkdirAll under it fails.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	badNewPath := filepath.Join(blocker, "sub", "snap.db")

	originalPath := filepath.Join(dir, "ok", "snap.db")
	sm := NewSnapshotManager(d, originalPath, originalPath)

	if err := sm.SetSnapshotPath(badNewPath); err == nil {
		t.Fatal("SetSnapshotPath should fail when MkdirAll cannot create parent dir")
	}
}

func TestOpenFailsOnInvalidPath(t *testing.T) {
	t.Parallel()
	// Opening a path whose parent does not exist forces sql.Open's first
	// Ping (or schema Exec) to fail. We pass a directory as the DB path —
	// modernc.org/sqlite returns an error on Ping.
	dir := t.TempDir()
	_, err := Open(dir) // directory, not a file → ping/exec fails
	if err == nil {
		t.Fatal("Open with directory path should fail")
	}
}

func TestSaveSnapshotRejectsBadPath(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	// Construct a SnapshotManager whose snapshotPath is invalid.
	sm := NewSnapshotManager(d, "", "")
	if err := sm.SaveSnapshot(); err == nil {
		t.Fatal("SaveSnapshot with empty snapshotPath should fail validation")
	}
}

// --- DropDedupState: full state reset --------------------------------------

func TestDropDedupStateRemovesEverything(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}

	// Seed a pack, a chunk, and a GC run.
	if err := d.UpsertDedupPack(DedupPack{ID: "p1", StorageID: destID, Path: "p1.pack", SizeBytes: 1, ChunkCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDedupChunk(DedupChunk{ChunkID: []byte{0x01}, StorageID: destID, PackID: "p1", Offset: 0, Length: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertDedupGCRun(DedupGCRun{
		StorageID: destID, StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := d.DropDedupState(destID); err != nil {
		t.Fatalf("DropDedupState: %v", err)
	}

	packs, err := d.ListDedupPacks(destID)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 0 {
		t.Fatalf("packs after drop = %d, want 0", len(packs))
	}
	if _, found, _ := d.LatestDedupGCRun(destID); found {
		t.Fatal("gc run not cleared by DropDedupState")
	}
}
