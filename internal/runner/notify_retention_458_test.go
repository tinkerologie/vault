package runner

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
)

func TestCaptureZFSResultB458(t *testing.T) {
	snaps := map[string]string{}
	res := map[string]any{}
	captureZFSResult(map[string]any{"dataset": "pool/ds"}, nil, "z", snaps, res)
	if res["zfs_dataset"] != "pool/ds" || len(snaps) != 0 {
		t.Fatalf("nil result: res=%v snaps=%v", res, snaps)
	}

	res = map[string]any{}
	captureZFSResult(map[string]any{}, &engine.BackupResult{Meta: map[string]any{
		"zfs_snapshot": "pool/ds@vault-1", "zfs_backup_type": "incremental",
	}}, "z", snaps, res)
	if snaps["z"] != "pool/ds@vault-1" || res["zfs_snapshot"] != "pool/ds@vault-1" || res["zfs_backup_type"] != "incremental" {
		t.Fatalf("snapshot capture: res=%v snaps=%v", res, snaps)
	}
	if _, ok := res["zfs_dataset"]; ok {
		t.Fatal("no dataset setting should leave zfs_dataset unset")
	}
}

func TestShouldMentionDiscordB458(t *testing.T) {
	cases := []struct {
		on, status string
		want       bool
	}{
		{"always", "completed", true},
		{"failure", "completed", false},
		{"failure", "failed", true},
		{"never", "failed", false},
		{"", "partial", false},
	}
	for _, c := range cases {
		if got := shouldMentionDiscord(c.on, c.status); got != c.want {
			t.Errorf("shouldMentionDiscord(%q,%q) = %v, want %v", c.on, c.status, got, c.want)
		}
	}
}

func TestDiscordOptionsB458(t *testing.T) {
	r, d, _ := setupTestRunner(t)
	_ = d.SetSetting("discord_bot_username", "VaultBot")
	_ = d.SetSetting("discord_bot_avatar_url", "https://example.invalid/a.png")
	_ = d.SetSetting("discord_mention_role_id", "123456789")
	_ = d.SetSetting("discord_mention_on", "failure")

	if o := r.discordOptions("failed"); o.Username != "VaultBot" || o.AvatarURL == "" || o.MentionRoleID != "123456789" {
		t.Fatalf("failed status options: %+v", o)
	}
	if o := r.discordOptions("completed"); o.MentionRoleID != "" {
		t.Fatalf("completed status should not mention: %+v", o)
	}
}

func TestSendNotificationBranchesB458(t *testing.T) {
	r, d, _ := setupTestRunner(t)
	job := db.Job{Name: "notify-job", NotifyOn: "always"}

	// Global switch off: early return.
	_ = d.SetSetting("notifications_enabled", "false")
	r.sendNotification(job, "failed", 0, 1, 0, 1, []string{"a"})

	_ = d.SetSetting("notifications_enabled", "true")
	// An http:// webhook is rejected by the Discord client before any
	// request is made, so no network traffic happens.
	_ = d.SetSetting("discord_webhook_url", "http://not-discord.invalid/hook")
	_ = d.SetSetting("discord_notify_on", "always")
	for _, status := range []string{"completed", "failed", "partial", "cancelled"} {
		r.sendNotification(job, status, 1, 1, 1024, 65, []string{"b"})
	}

	// Discord on failure only, and job preference never.
	_ = d.SetSetting("discord_notify_on", "failure")
	r.sendNotification(db.Job{Name: "n2"}, "completed", 1, 0, 1, 1, nil)
	r.sendNotification(db.Job{Name: "n3", NotifyOn: "never"}, "failed", 0, 1, 1, 1, nil)
}

func TestSendRestoreNotificationB458(t *testing.T) {
	r, d, _ := setupTestRunner(t)
	_ = d.SetSetting("notifications_enabled", "true")
	r.sendRestoreNotification("item-ok", "folder", nil)
	r.sendRestoreNotification("item-bad", "folder", errors.New("boom"))

	entries, err := d.ListActivityLogs(50, "restore", 0)
	if err != nil {
		t.Fatal(err)
	}
	var ok, bad bool
	for _, e := range entries {
		ok = ok || strings.Contains(e.Message, "Restore completed: item-ok")
		bad = bad || strings.Contains(e.Message, "Restore failed: item-bad")
	}
	if !ok || !bad {
		t.Fatalf("missing restore activity entries: %+v", entries)
	}
}

func TestStructuredDetailsFallbackB458(t *testing.T) {
	// A channel cannot be marshalled, so the fmt fallback is used.
	ch := make(chan int)
	if got := structuredDetails(ch); got == "" || strings.HasPrefix(got, "{") {
		t.Fatalf("fallback output = %q", got)
	}
	if !hasContainerItems([]db.JobItem{{ItemType: "folder"}, {ItemType: "container"}}) {
		t.Fatal("hasContainerItems should find the container")
	}
}

func TestDeleteVMCheckpointsForRPB458(t *testing.T) {
	r, _, _ := setupTestRunner(t)
	// Every variant must be a harmless no-op on hosts without libvirt.
	for _, meta := range []string{
		"",
		"{not json",
		`{"items":1}`,
		`{"vm_checkpoints":{}}`,
		`{"vm_checkpoints":{"vm1":"cp-1","vm2":""}}`,
	} {
		r.deleteVMCheckpointsForRP(db.RestorePoint{ID: 1, Metadata: meta})
	}
}

func b458SeedRPs(t *testing.T, d *db.DB, jobID int64, n int) []int64 {
	t.Helper()
	var ids []int64
	for i := range n {
		ids = append(ids, createRestorePointWithAge(t, d, jobID, "full", "", 0, time.Duration(i+1)*24*time.Hour))
	}
	return ids
}

func TestEnforceRetentionAdapterErrorAndCancelB458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)
	jobID, err := d.CreateJob(db.Job{Name: "ret-job", StorageDestID: dest.ID, BackupTypeChain: "full"})
	if err != nil {
		t.Fatal(err)
	}
	b458SeedRPs(t, d, jobID, 3)
	bad := db.StorageDestination{Type: "bogus-type", Config: "{}"}

	// Cancelled context: sweep stops before deleting anything.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.enforceRetention(ctx, bad, jobID, 1, 0)
	r.enforceRetentionLTR(ctx, bad, jobID, LTRPolicy{KeepLatest: 1})
	if rps, _ := d.ListRestorePoints(jobID); len(rps) != 3 {
		t.Fatalf("cancelled sweep deleted rows: %d left", len(rps))
	}

	// Adapter failure: rows are still pruned in the database.
	r.enforceRetention(context.Background(), bad, jobID, 2, 0)
	if rps, _ := d.ListRestorePoints(jobID); len(rps) != 2 {
		t.Fatalf("retention kept %d, want 2", len(rps))
	}
	r.enforceRetentionLTR(context.Background(), bad, jobID, LTRPolicy{KeepLatest: 1})
	if rps, _ := d.ListRestorePoints(jobID); len(rps) != 1 {
		t.Fatalf("LTR retention kept %d, want 1", len(rps))
	}
}

func TestReadItemSidecarBranchesB458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)
	adapter := b458LocalAdapter(t, storageDir)
	idx := `{"version":1,"archive":"data.tar","files":[{"path":"a","size":1,"mode":"0644","modtime":"2026-01-01T00:00:00Z"}]}`

	// Missing item directory: list error.
	if _, ok := r.ReadItemSidecar(dest, db.RestorePoint{StoragePath: "nope"}, "item", engine.IndexSuffix); ok {
		t.Fatal("missing directory should not yield a sidecar")
	}
	// Adapter construction failure.
	if _, ok := r.ReadItemSidecar(db.StorageDestination{Type: "bogus-type"}, db.RestorePoint{}, "item", engine.IndexSuffix); ok {
		t.Fatal("bogus adapter should not yield a sidecar")
	}

	// Plain sidecar next to a subdirectory.
	b458WriteFile(t, filepath.Join(storageDir, "rp1", "item", "sub", "x"), "x")
	b458WriteFile(t, filepath.Join(storageDir, "rp1", "item", "data.tar"+engine.IndexSuffix), idx)
	if got, ok := r.ReadItemSidecar(dest, db.RestorePoint{StoragePath: "rp1"}, "item", engine.IndexSuffix); !ok || len(got.Files) != 1 {
		t.Fatalf("plain sidecar: ok=%v idx=%+v", ok, got)
	}
	// No candidate for a different suffix.
	if _, ok := r.ReadItemSidecar(dest, db.RestorePoint{StoragePath: "rp1"}, "item", engine.ListingSuffix); ok {
		t.Fatal("no listing sidecar expected")
	}

	// Corrupt sidecar fails to decode.
	b458WriteFile(t, filepath.Join(storageDir, "rp2", "item", "data.tar"+engine.IndexSuffix), "{bad")
	if _, ok := r.ReadItemSidecar(dest, db.RestorePoint{StoragePath: "rp2"}, "item", engine.IndexSuffix); ok {
		t.Fatal("corrupt sidecar should fail")
	}

	// Encrypted sidecar: no passphrase configured, wrong passphrase, right one.
	writeEncryptedStorageFile(t, adapter, "rp3/item/data.tar"+engine.IndexSuffix+".age", idx, "pw")
	rp3 := db.RestorePoint{StoragePath: "rp3"}
	if _, ok := r.readItemSidecar(adapter, rp3, "item", engine.IndexSuffix, ""); ok {
		t.Fatal("encrypted sidecar without passphrase should fail")
	}
	if _, ok := r.readItemSidecar(adapter, rp3, "item", engine.IndexSuffix, "wrong"); ok {
		t.Fatal("encrypted sidecar with wrong passphrase should fail")
	}
	if got, ok := r.readItemSidecar(adapter, rp3, "item", engine.IndexSuffix, "pw"); !ok || len(got.Files) != 1 {
		t.Fatalf("encrypted sidecar: ok=%v idx=%+v", ok, got)
	}
	_ = d.SetSetting("encryption_passphrase", "pw")
	if _, ok := r.readItemSidecar(adapter, rp3, "item", engine.IndexSuffix, ""); !ok {
		t.Fatal("configured passphrase should decrypt the sidecar")
	}
}

func TestWriteManifestFailuresB458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)
	job := db.Job{Name: "wm", Encryption: "age"}
	ctx := context.Background()

	// Adapter construction failure.
	r.writeManifest(ctx, db.StorageDestination{Type: "bogus-type"}, "wm/r0", job, nil, 1, "full", 0, 0, 0, nil, nil, "ts", "")
	// Write failure: path escapes the destination root.
	r.writeManifest(ctx, dest, "../escape", job, nil, 1, "full", 0, 0, 0, nil, nil, "ts", "pw")
	if _, err := os.Stat(filepath.Join(filepath.Dir(storageDir), "escape")); !os.IsNotExist(err) {
		t.Fatalf("manifest must not be written outside the root: %v", err)
	}
	// Age-encrypted manifest with checksums and item manifests.
	r.writeManifest(ctx, dest, "wm/r1", job, []db.JobItem{{ItemName: "a", ItemType: "folder", Settings: `{"path":"/x"}`}},
		1, "full", 1, 0, 10, map[string]map[string]string{"a": {"f": "h"}}, map[string]string{"a": strings.Repeat("00", 32)}, "ts", "pw")
	raw, err := os.ReadFile(filepath.Join(storageDir, "wm", "r1", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if isEnv, env := detectManifestEnvelope(raw); !isEnv || env.Key != "age" {
		t.Fatalf("expected age envelope, got %s", raw)
	}

	// Dedup destination whose repo header is corrupt: encryption is refused
	// and nothing is written.
	dedupDir := t.TempDir()
	dd := makeDedupDest(t, d, dedupDir)
	b458WriteFile(t, filepath.Join(dedupDir, "_vault", "repo.json"), "garbage")
	r.writeManifest(ctx, dd, "wm/r2", job, nil, 1, "full", 0, 0, 0, nil, nil, "ts", "")
	if _, err := os.Stat(filepath.Join(dedupDir, "wm", "r2", "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("manifest should not be written when dedup repo cannot open: %v", err)
	}
}

func TestRunDedupGCBranchesB458(t *testing.T) {
	r, d, _ := setupTestRunner(t)
	r.serverKey = testServerKey()

	// Adapter construction failure on a dedup destination.
	r.RunDedupGC(db.StorageDestination{ID: 77, Name: "bad", Type: "bogus-type", DedupEnabled: true}, "gc-bad")

	// Draining daemon refuses to start GC.
	r.activeMu.Lock()
	r.draining = true
	r.activeMu.Unlock()
	r.RunDedupGC(db.StorageDestination{ID: 78, DedupEnabled: true}, "gc-drain")
	r.activeMu.Lock()
	r.draining = false
	r.activeMu.Unlock()

	// Real repo: add a second restore point whose item_manifests mixes
	// junk values, an unreadable manifest ID and a duplicate, then run GC
	// with an invalid compaction ratio so the fallback is used.
	storageDir := t.TempDir()
	dest, jobID := dedupBackup(t, r, d, storageDir)
	rps, err := d.ListRestorePoints(jobID)
	if err != nil || len(rps) == 0 {
		t.Fatalf("list rps: %v", err)
	}
	var existing string
	if id, ok := ResolveItemManifestID(rps[0], "src"); ok {
		existing = hex.EncodeToString(id[:])
	}
	runID, _ := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	meta := `{"item_manifests":{"n":5,"e":"","bad":"zz","short":"abcd","ghost":"` + strings.Repeat("ee", 32) + `","dup":"` + existing + `"}}`
	if _, err := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "escrow/extra", Metadata: meta}); err != nil {
		t.Fatal(err)
	}
	_ = d.SetSetting("dedup_compaction_min_dead_ratio", "not-a-number")
	r.RunDedupGC(dest, "gc-ok")

	repoAdapter := adapterFor(t, dest)
	repo, err := r.openExistingDedupRepo(repoAdapter, dest)
	if err != nil {
		t.Fatalf("repo should still open after GC: %v", err)
	}
	live, err := r.collectLiveManifestIDs(repo, dest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) < 2 {
		t.Fatalf("expected the real manifest and the unreadable ghost to stay live, got %d", len(live))
	}
}

// b458Symlink plants a symlink at link pointing outside the destination root;
// the local adapter refuses to operate through it, which is how these tests
// force list/delete failures without relying on file permissions.
func b458Symlink(t *testing.T, link string) {
	t.Helper()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
}

func TestDeleteStorageDirFailuresB458(t *testing.T) {
	r, _, storageDir := setupTestRunner(t)
	adapter := b458LocalAdapter(t, storageDir)
	b458WriteFile(t, filepath.Join(storageDir, "jobS", "keep.txt"), "x")
	b458Symlink(t, filepath.Join(storageDir, "jobS", "sub", "escape"))
	if err := r.DeleteStorageDir(adapter, "jobS"); err == nil {
		t.Fatal("expected an error deleting through an escaping symlink")
	}
	if _, err := os.Stat(filepath.Join(storageDir, "jobS", "keep.txt")); !os.IsNotExist(err) {
		t.Fatalf("regular file should still be deleted: %v", err)
	}
}

func TestReclaimDedupAfterJobDeleteFailuresB458(t *testing.T) {
	r, _, _ := setupTestRunner(t)

	t.Run("subpath and header backup removal fail", func(t *testing.T) {
		dir := t.TempDir()
		dest := db.StorageDestination{ID: 9001, DedupEnabled: true}
		b458WriteFile(t, filepath.Join(dir, "_vault", "repo.json"), "{}")
		b458Symlink(t, filepath.Join(dir, "_vault", "packs"))
		b458Symlink(t, filepath.Join(dir, "_vault", "repo.json.1.bak"))
		var errs []error
		r.reclaimDedupAfterJobDelete(b458LocalAdapter(t, dir), 1, dest, &errs)
		if len(errs) < 2 {
			t.Fatalf("want subpath and header backup errors, got %v", errs)
		}
	})

	t.Run("repo root unlistable", func(t *testing.T) {
		dir := t.TempDir()
		dest := db.StorageDestination{ID: 9002, DedupEnabled: true}
		b458Symlink(t, filepath.Join(dir, "_vault"))
		var errs []error
		r.reclaimDedupAfterJobDelete(b458LocalAdapter(t, dir), 2, dest, &errs)
		if len(errs) == 0 {
			t.Fatal("expected errors when the repo root escapes the destination")
		}
	})
}

func TestImportBackupsCreateJobFailureB458(t *testing.T) {
	r, _, _ := setupTestRunner(t)
	// A destination id with no row violates the jobs foreign key.
	n, err := r.ImportBackups(55555, []map[string]any{{"job_name": "orphan-import", "storage_path": "a/b"}})
	if err != nil {
		t.Fatalf("ImportBackups: %v", err)
	}
	if n != 0 {
		t.Fatalf("imported %d backups onto a missing destination, want 0 (job creation must fail)", n)
	}
}
