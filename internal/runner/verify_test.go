package runner

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/storage"
	"github.com/ruaan-deysel/vault/internal/ws"
)

func TestParseRestorePointChecksums_HappyPath(t *testing.T) {
	// Matches the actual restore-point metadata shape: items is an int
	// (count), item_sizes is per-item totals, checksums is the per-file
	// SHA-256 map.
	meta := `{
	  "backup_type": "full",
	  "checksums": {
	    "Flash Drive": {"data.tar.zst": "abc123", "metadata.json": "def456"},
	    "OtherItem":   {"data.tar.gz": "deadbeef"}
	  },
	  "item_sizes": {"Flash Drive": 100, "OtherItem": 50},
	  "items": 2,
	  "items_failed": 0,
	  "verified": true
	}`
	got, err := parseRestorePointChecksums(meta)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got["Flash Drive/data.tar.zst"].SHA256 != "abc123" {
		t.Errorf("missing or wrong SHA for Flash Drive/data.tar.zst: %+v", got["Flash Drive/data.tar.zst"])
	}
	if got["OtherItem/data.tar.gz"].SHA256 != "deadbeef" {
		t.Errorf("missing other-item SHA: %+v", got["OtherItem/data.tar.gz"])
	}
}

func TestParseRestorePointChecksums_EmptyMetadata(t *testing.T) {
	got, err := parseRestorePointChecksums("")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil map, got %v", got)
	}
}

func TestParseRestorePointChecksums_NoChecksumsKey(t *testing.T) {
	meta := `{"items": 1, "item_sizes": {"a": 0}}`
	got, err := parseRestorePointChecksums(meta)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result without checksums key, got %d", len(got))
	}
}

func TestParseRestorePointChecksums_SizeAlwaysZero(t *testing.T) {
	// Per-file sizes are not persisted in restore-point metadata today,
	// so every recordedChecksum has Size = 0 (the verifier's
	// size-mismatch check is skipped when Size is 0).
	meta := `{"checksums": {"X": {"f.tar": "ffee"}}, "items": 1, "item_sizes": {"X": 42}}`
	got, err := parseRestorePointChecksums(meta)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got["X/f.tar"].SHA256 != "ffee" {
		t.Errorf("missing SHA: %+v", got)
	}
	if got["X/f.tar"].Size != 0 {
		t.Errorf("size should default to 0, got %d", got["X/f.tar"].Size)
	}
}

func TestParseRestorePointChecksums_MalformedJSON(t *testing.T) {
	_, err := parseRestorePointChecksums("not json")
	if err == nil {
		t.Error("expected error on malformed JSON")
	}
}

func TestVerifyMode_IsValid(t *testing.T) {
	cases := map[VerifyMode]bool{
		VerifyModeQuick: true,
		VerifyModeDeep:  true,
		"":              false,
		"slow":          false,
		"QUICK":         false, // case sensitive — handler lowercases before checking
	}
	for m, want := range cases {
		if got := m.IsValid(); got != want {
			t.Errorf("IsValid(%q) = %v, want %v", m, got, want)
		}
	}
}

// setupDedupVerifyFixture provisions a Runner, a dedup-enabled local
// destination, and runs a small folder backup so the caller has a dedup
// restore point to verify. Mirrors the setup used by
// TestRunnerDedupBackupRoundTrip in runner_test.go.
func setupDedupVerifyFixture(t *testing.T) (*db.DB, *Runner, db.RestorePoint, string) {
	t.Helper()

	storageDir := t.TempDir()
	sourceDir := t.TempDir()
	// A few files so the manifest references real chunks (and a deep
	// verify has > 0 bytes_read).
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		body := []byte("verify content for " + name + " — repeated text to ensure splitter emits chunks")
		if err := os.WriteFile(filepath.Join(sourceDir, name), body, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	dbPath := filepath.Join(t.TempDir(), "vault.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	serverKey := bytes.Repeat([]byte{0xee}, 32)
	hub := ws.NewHub()
	go hub.Run()
	r := New(d, hub, serverKey)

	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:         "dedup-verify-test",
		Type:         "local",
		Config:       string(destCfg),
		DedupEnabled: true,
	})
	if err != nil {
		t.Fatalf("create storage destination: %v", err)
	}

	jobID, err := d.CreateJob(db.Job{
		Name:            "dedup-verify-job",
		StorageDestID:   destID,
		BackupTypeChain: "full",
		Enabled:         true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	itemSettings, _ := json.Marshal(map[string]any{"path": sourceDir})
	if _, err := d.AddJobItem(db.JobItem{
		JobID:    jobID,
		ItemType: "folder",
		ItemName: "src",
		Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add job item: %v", err)
	}

	// RunJob is synchronous (holds r.mu and runs the pipeline inline).
	r.RunJob(jobID)

	rps, err := d.ListRestorePoints(jobID)
	if err != nil {
		t.Fatalf("list restore points: %v", err)
	}
	if len(rps) != 1 {
		t.Fatalf("expected 1 restore point, got %d", len(rps))
	}
	rp := rps[0]
	if len(rp.ManifestID) != 32 {
		t.Fatalf("manifest_id not persisted: got %d bytes", len(rp.ManifestID))
	}

	return d, r, rp, storageDir
}

// waitForVerifyCompletion polls db.GetVerifyRun until status leaves
// "running" or the timeout fires. Returns the final row.
func waitForVerifyCompletion(t *testing.T, d *db.DB, verifyID int64, timeout time.Duration) db.VerifyRun {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		run, err := d.GetVerifyRun(verifyID)
		if err != nil {
			t.Fatalf("get verify run %d: %v", verifyID, err)
		}
		if run.Status != "running" {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("verify run %d did not complete within %s", verifyID, timeout)
	return db.VerifyRun{}
}

func TestVerifyDedupQuick(t *testing.T) {
	d, r, rp, _ := setupDedupVerifyFixture(t)

	verifyID, err := r.RunVerify(rp, VerifyModeQuick)
	if err != nil {
		t.Fatalf("RunVerify quick: %v", err)
	}
	run := waitForVerifyCompletion(t, d, verifyID, 10*time.Second)

	if run.Status != "passed" {
		t.Fatalf("quick verify status = %q, want passed (error_summary=%q)", run.Status, run.ErrorSummary)
	}
	if run.FilesFailed != 0 {
		t.Fatalf("quick verify files_failed = %d, want 0", run.FilesFailed)
	}
	if run.FilesChecked < 1 {
		t.Fatalf("quick verify files_checked = %d, want >= 1 (at least one pack)", run.FilesChecked)
	}
	// Quick mode does no decryption / streaming; bytes_read should stay 0.
	if run.BytesRead != 0 {
		t.Fatalf("quick verify bytes_read = %d, want 0", run.BytesRead)
	}
}

func TestVerifyDedupDeep(t *testing.T) {
	d, r, rp, _ := setupDedupVerifyFixture(t)

	verifyID, err := r.RunVerify(rp, VerifyModeDeep)
	if err != nil {
		t.Fatalf("RunVerify deep: %v", err)
	}
	run := waitForVerifyCompletion(t, d, verifyID, 30*time.Second)

	if run.Status != "passed" {
		t.Fatalf("deep verify status = %q, want passed (error_summary=%q)", run.Status, run.ErrorSummary)
	}
	if run.FilesFailed != 0 {
		t.Fatalf("deep verify files_failed = %d, want 0", run.FilesFailed)
	}
	if run.FilesChecked < 1 {
		t.Fatalf("deep verify files_checked = %d, want >= 1", run.FilesChecked)
	}
	if run.BytesRead <= 0 {
		t.Fatalf("deep verify bytes_read = %d, want > 0 (deep mode reads chunk plaintexts)", run.BytesRead)
	}
}

func TestVerifyDedupDeepDetectsTamper(t *testing.T) {
	d, r, rp, storageDir := setupDedupVerifyFixture(t)

	// Open the repo independently so we can resolve the on-disk offset
	// of a known *file* chunk (not the manifest chunk) and flip a byte
	// inside its ciphertext. Targeting a referenced chunk guarantees the
	// verify path will read and AEAD-decrypt it.
	job, err := d.GetJob(rp.JobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	dest, err := d.GetStorageDestination(job.StorageDestID)
	if err != nil {
		t.Fatalf("get dest: %v", err)
	}
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	defer storage.CloseAdapter(adapter)

	serverKey := bytes.Repeat([]byte{0xee}, 32)
	repo, err := dedup.OpenRepo(d, adapter, dest.ID, serverKey)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}

	var mID dedup.ID
	copy(mID[:], rp.ManifestID)
	m, err := repo.GetManifest(mID)
	if err != nil {
		t.Fatalf("get manifest: %v", err)
	}

	// Pick the first file chunk from the first file in the manifest.
	var chunkID dedup.ID
	var foundChunk bool
	for _, entry := range m.Files {
		if len(entry.Chunks) > 0 {
			chunkID = entry.Chunks[0]
			foundChunk = true
			break
		}
	}
	if !foundChunk {
		t.Fatalf("manifest has no file chunks to tamper")
	}

	packRel, offset, length, err := repo.LocateForVerify(chunkID)
	if err != nil {
		t.Fatalf("locate chunk: %v", err)
	}
	if length < 2 {
		t.Fatalf("chunk length too small to tamper (%d)", length)
	}

	packPath := filepath.Join(storageDir, packRel)
	pack, err := os.ReadFile(packPath)
	if err != nil {
		t.Fatalf("read pack %s: %v", packPath, err)
	}
	// Flip a byte inside the chunk's ciphertext region (skip the leading
	// flags byte). int64 offset is safe here — packs are KB-sized in this
	// test, well within int range.
	tamperOffset := int(offset) + 1 + int(length)/2
	if tamperOffset >= len(pack) {
		t.Fatalf("tamper offset %d out of range (pack %d bytes)", tamperOffset, len(pack))
	}
	pack[tamperOffset] ^= 0xFF
	if err := os.WriteFile(packPath, pack, 0o644); err != nil {
		t.Fatalf("rewrite tampered pack: %v", err)
	}

	verifyID, err := r.RunVerify(rp, VerifyModeDeep)
	if err != nil {
		t.Fatalf("RunVerify deep: %v", err)
	}
	run := waitForVerifyCompletion(t, d, verifyID, 30*time.Second)

	if run.Status != "failed" {
		t.Fatalf("tamper deep verify status = %q, want failed (error_summary=%q)", run.Status, run.ErrorSummary)
	}
	if run.FilesFailed != 1 {
		t.Fatalf("tamper deep verify files_failed = %d, want exactly 1 (one chunk tampered)", run.FilesFailed)
	}
	if run.ErrorSummary == "" {
		t.Fatalf("expected non-empty error_summary on tampered deep verify")
	}
	if !strings.Contains(run.ErrorSummary, "chunk") {
		t.Fatalf("expected error_summary to mention chunk, got %q", run.ErrorSummary)
	}
}


// TestVerifyDedupMultiItemDeep is the regression test for deep verify on
// MULTI-ITEM dedup jobs. Such restore points have an EMPTY manifest_id
// (the single-item shortcut only fires for one-item jobs); the per-item
// manifest IDs live in metadata.item_manifests. Before the fix, verify keyed
// off rp.ManifestID==32 and fell through to the classic per-file path, which
// failed with "no recorded file checksums in restore point metadata". It must
// now route to the dedup path and pass.
func TestVerifyDedupMultiItemDeep(t *testing.T) {
	storageDir := t.TempDir()
	src1 := t.TempDir()
	src2 := t.TempDir()
	for dir, name := range map[string]string{src1: "one.txt", src2: "two.txt"} {
		body := []byte("multi-item dedup verify payload for " + name + " — long enough to chunk")
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	d, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	serverKey := bytes.Repeat([]byte{0xee}, 32)
	hub := ws.NewHub()
	go hub.Run()
	r := New(d, hub, serverKey)

	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "dedup-multi", Type: "local", Config: string(destCfg), DedupEnabled: true,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "dedup-multi-job", StorageDestID: destID, BackupTypeChain: "full", Enabled: true})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	for name, dir := range map[string]string{"item1": src1, "item2": src2} {
		s, _ := json.Marshal(map[string]any{"path": dir})
		if _, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: name, Settings: string(s)}); err != nil {
			t.Fatalf("add item: %v", err)
		}
	}

	r.RunJob(jobID)

	rps, err := d.ListRestorePoints(jobID)
	if err != nil || len(rps) != 1 {
		t.Fatalf("list restore points: err=%v n=%d", err, len(rps))
	}
	rp := rps[0]
	// The crux: a multi-item job does NOT set the single-item shortcut.
	if len(rp.ManifestID) == 32 {
		t.Fatalf("expected empty manifest_id for multi-item job, got 32 bytes (test no longer exercises the multi-item path)")
	}

	verifyID, err := r.RunVerify(rp, VerifyModeDeep)
	if err != nil {
		t.Fatalf("RunVerify deep: %v", err)
	}
	run := waitForVerifyCompletion(t, d, verifyID, 30*time.Second)
	if run.Status != "passed" {
		t.Fatalf("multi-item deep verify status = %q, want passed (error_summary=%q)", run.Status, run.ErrorSummary)
	}
	if run.FilesFailed != 0 {
		t.Fatalf("multi-item deep verify files_failed = %d, want 0", run.FilesFailed)
	}
	if run.BytesRead <= 0 {
		t.Fatalf("multi-item deep verify bytes_read = %d, want > 0", run.BytesRead)
	}
}
