package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/engine"
	"github.com/ruaan-deysel/vault/internal/storage"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// Issue #382: verify_backup was honoured for classic tar uploads and silently
// ignored on dedup destinations, so a job with verification switched on got no
// verification at all and said nothing about it.

// runDedupVerifyJob runs a one-folder dedup backup with verify_backup set and
// returns the DB, runner, restore point and storage dir.
func runDedupVerifyJob(t *testing.T, verify bool) (*db.DB, *Runner, db.RestorePoint, string) {
	t.Helper()

	storageDir := t.TempDir()
	sourceDir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		body := bytes.Repeat([]byte("verify-backup content for "+name+" "), 64)
		if err := os.WriteFile(filepath.Join(sourceDir, name), body, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	d, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	hub := ws.NewHub()
	go hub.Run()
	r := New(d, hub, bytes.Repeat([]byte{0xee}, 32))

	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "dedup-verify-backup", Type: "local", Config: string(destCfg), DedupEnabled: true,
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name: "dedup-verify-backup-job", StorageDestID: destID,
		BackupTypeChain: "full", Enabled: true, VerifyBackup: verify,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	itemSettings, _ := json.Marshal(map[string]any{"path": sourceDir})
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add job item: %v", err)
	}

	r.RunJob(jobID)

	rps, err := d.ListRestorePoints(jobID)
	if err != nil {
		t.Fatalf("list restore points: %v", err)
	}
	if len(rps) != 1 {
		t.Fatalf("expected 1 restore point, got %d", len(rps))
	}
	return d, r, rps[0], storageDir
}

func TestDedupBackupRecordsVerifiedMetadata(t *testing.T) {
	t.Run("verify_backup on marks the restore point verified", func(t *testing.T) {
		_, _, rp, _ := runDedupVerifyJob(t, true)
		var meta map[string]any
		if err := json.Unmarshal([]byte(rp.Metadata), &meta); err != nil {
			t.Fatalf("restore point metadata: %v (raw %q)", err, rp.Metadata)
		}
		if v, _ := meta["verified"].(bool); !v {
			// Dedup has no per-file checksums, so gating "verified" on a
			// checksums map left every dedup run looking unverified.
			t.Errorf("metadata[verified] = %v, want true (metadata: %v)", meta["verified"], meta)
		}
	})

	t.Run("verify_backup off leaves it unset", func(t *testing.T) {
		_, _, rp, _ := runDedupVerifyJob(t, false)
		var meta map[string]any
		if err := json.Unmarshal([]byte(rp.Metadata), &meta); err != nil {
			t.Fatalf("restore point metadata: %v", err)
		}
		if _, ok := meta["verified"]; ok {
			t.Errorf("metadata[verified] should be absent when verification is off, got %v", meta["verified"])
		}
	})
}

// openFixtureRepo reopens the dedup repo behind a restore point.
func openFixtureRepo(t *testing.T, d *db.DB, rp db.RestorePoint) (*dedup.Repo, storage.Adapter, dedup.ID) {
	t.Helper()
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
	t.Cleanup(func() { storage.CloseAdapter(adapter) })
	repo, err := dedup.OpenRepo(d, adapter, dest.ID, bytes.Repeat([]byte{0xee}, 32))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	var mID dedup.ID
	copy(mID[:], rp.ManifestID)
	return repo, adapter, mID
}

func TestVerifyChunkedItemDetectsCorruption(t *testing.T) {
	d, r, rp, storageDir := runDedupVerifyJob(t, true)
	repo, _, manifestID := openFixtureRepo(t, d, rp)
	item := engine.BackupItem{Name: "src", Type: "folder"}

	// The bytes that were just written must pass — otherwise the negative
	// case below would prove nothing.
	if err := r.verifyChunkedItem(context.Background(), rp.JobRunID, item, repo, manifestID); err != nil {
		t.Fatalf("a freshly written manifest should verify clean: %v", err)
	}

	// Flip a byte inside a referenced file chunk's ciphertext, the same way
	// TestVerifyDedupDeepDetectsTamper does.
	m, err := repo.GetManifest(manifestID)
	if err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	var chunkID dedup.ID
	found := false
	for _, entry := range m.Files {
		if len(entry.Chunks) > 0 {
			chunkID, found = entry.Chunks[0], true
			break
		}
	}
	if !found {
		t.Fatal("manifest has no file chunks to corrupt")
	}
	packRel, offset, length, err := repo.LocateForVerify(chunkID)
	if err != nil {
		t.Fatalf("locate chunk: %v", err)
	}
	packPath := filepath.Join(storageDir, packRel)
	pack, err := os.ReadFile(packPath) // #nosec G304 — test-local path
	if err != nil {
		t.Fatalf("read pack: %v", err)
	}
	at := int(offset) + 1 + int(length)/2
	if at >= len(pack) {
		t.Fatalf("tamper offset %d out of range (pack %d bytes)", at, len(pack))
	}
	pack[at] ^= 0xff
	if err := os.WriteFile(packPath, pack, 0o600); err != nil {
		t.Fatalf("rewrite pack: %v", err)
	}

	err = r.verifyChunkedItem(context.Background(), rp.JobRunID, item, repo, manifestID)
	if err == nil {
		t.Fatal("a corrupted chunk must fail verification — silently passing is the bug")
	}
	if !strings.Contains(err.Error(), "verify src") {
		t.Errorf("error %q should name the item being verified", err)
	}
}

func TestVerifyChunkedItemHonoursCancellation(t *testing.T) {
	d, r, rp, _ := runDedupVerifyJob(t, true)
	repo, _, manifestID := openFixtureRepo(t, d, rp)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := r.verifyChunkedItem(ctx, rp.JobRunID, engine.BackupItem{Name: "src", Type: "folder"}, repo, manifestID)
	if err == nil {
		t.Fatal("a cancelled verification should stop, not re-read the whole repo")
	}
	if !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Errorf("error %q should report the cancellation", err)
	}
}

func TestVerifyChunkedItemMissingManifest(t *testing.T) {
	d, r, rp, _ := runDedupVerifyJob(t, true)
	repo, _, _ := openFixtureRepo(t, d, rp)

	var bogus dedup.ID
	bogus[0] = 0x42
	err := r.verifyChunkedItem(context.Background(), rp.JobRunID, engine.BackupItem{Name: "src"}, repo, bogus)
	if err == nil {
		t.Fatal("verifying an unreadable manifest should error")
	}
	if !strings.Contains(err.Error(), "walking manifest") {
		t.Errorf("error %q should say the manifest walk failed", err)
	}
}

func TestDedupRepoReadAndVerify(t *testing.T) {
	d, _, rp, _ := runDedupVerifyJob(t, false)
	repo, _, manifestID := openFixtureRepo(t, d, rp)

	m, err := repo.GetManifest(manifestID)
	if err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	for _, entry := range m.Files {
		for _, cid := range entry.Chunks {
			body, err := repo.ReadAndVerify(cid)
			if err != nil {
				t.Fatalf("ReadAndVerify(%x): %v", cid[:8], err)
			}
			if len(body) == 0 {
				t.Errorf("ReadAndVerify(%x) returned no bytes — callers account bytes_read from this", cid[:8])
			}
		}
	}

	var missing dedup.ID
	missing[0] = 0x99
	if _, err := repo.ReadAndVerify(missing); err == nil {
		t.Error("ReadAndVerify of an unknown chunk should error")
	}
}
