package runner

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/storage"
)

func b458JSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestScanStorageManifestsEdgeCasesB458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)

	// Top-level plain file (skipped), a run entry that is a file (skipped),
	// a run with an invalid manifest, a run whose manifest is too new, a
	// dedup envelope whose repo cannot be opened, and one valid manifest.
	b458WriteFile(t, filepath.Join(storageDir, "README.txt"), "not a job")
	b458WriteFile(t, filepath.Join(storageDir, "jobX", "loose-file"), "x")
	b458WriteFile(t, filepath.Join(storageDir, "jobX", "run-bad", "manifest.json"), "{not json")
	b458WriteFile(t, filepath.Join(storageDir, "jobX", "run-new", "manifest.json"),
		b458JSON(t, map[string]any{"version": 99, "job_name": "jobX"}))
	b458WriteFile(t, filepath.Join(storageDir, "jobX", "run-ok", "manifest.json"),
		b458JSON(t, map[string]any{"version": 2, "job_name": "jobX"}))
	env, err := encodeManifestEnvelope("dedup", "aes-256-gcm", []byte("cipher"))
	if err != nil {
		t.Fatal(err)
	}
	b458WriteFile(t, filepath.Join(storageDir, "jobX", "run-dedup", "manifest.json"), string(env))
	b458WriteFile(t, filepath.Join(storageDir, "_vault", "repo.json"), "garbage-header")

	got, err := r.ScanStorageManifests(dest)
	if err != nil {
		t.Fatalf("ScanStorageManifests: %v", err)
	}
	var okFound, encFound bool
	for _, m := range got {
		sp, _ := m["storage_path"].(string)
		if strings.HasSuffix(sp, "run-ok") {
			okFound = true
		}
		if strings.HasSuffix(sp, "run-dedup") && m["encrypted"] == true {
			encFound = true
		}
		if strings.HasSuffix(sp, "run-new") || strings.HasSuffix(sp, "run-bad") {
			t.Errorf("unexpected manifest returned: %v", m)
		}
	}
	if !okFound || !encFound {
		t.Fatalf("expected ok and encrypted placeholder, got %v", got)
	}
}

func TestScanStorageManifestsErrorsB458(t *testing.T) {
	r, _, _ := setupTestRunner(t)
	if _, err := r.ScanStorageManifests(db.StorageDestination{Type: "bogus-type", Config: "{}"}); err == nil {
		t.Fatal("adapter failure should error")
	}
	missing := db.StorageDestination{Type: "local", Config: b458JSON(t, map[string]string{"path": filepath.Join(t.TempDir(), "nope")})}
	if _, err := r.ScanStorageManifests(missing); err == nil {
		t.Fatal("listing a missing root should error")
	}
}

func TestScanAppdataBackupsEdgeCasesB458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)
	b458WriteFile(t, filepath.Join(storageDir, "loose.tar.gz"), "x")
	b458WriteFile(t, filepath.Join(storageDir, "other_dir", "a.tar.gz"), "x")
	b458WriteFile(t, filepath.Join(storageDir, "ab_20260304_040001", "plex.tar.gz"), "x")
	b458WriteFile(t, filepath.Join(storageDir, "ab_20260304_040001", "nested", "inner.tar.gz"), "x")
	b458WriteFile(t, filepath.Join(storageDir, "ab_20260304_040001", "notes.log"), "x")

	got, err := r.ScanAppdataBackups(dest, "")
	if err != nil {
		t.Fatalf("ScanAppdataBackups: %v", err)
	}
	if len(got) != 1 || got[0]["job_name"] != "plex" {
		t.Fatalf("want one plex backup, got %v", got)
	}

	if _, err := r.ScanAppdataBackups(db.StorageDestination{Type: "bogus-type", Config: "{}"}, ""); err == nil {
		t.Fatal("adapter failure should error")
	}
	if _, err := r.ScanAppdataBackups(dest, "does-not-exist"); err == nil {
		t.Fatal("missing base path should error")
	}
}

func TestParseAppdataTimestampB458(t *testing.T) {
	cases := map[string]string{
		"ab_20260304_040001": "2026-03-04T04:00:01Z",
		"ab_2026":            "",
		"ab_2026034_040001":  "",
		"ab_20261399_040001": "",
	}
	for in, want := range cases {
		if got := parseAppdataTimestamp(in); got != want {
			t.Errorf("parseAppdataTimestamp(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildImportJobItemsEdgeCasesB458(t *testing.T) {
	items := buildImportJobItems(map[string]any{
		"items": []any{
			"not-a-map",
			map[string]any{"type": "folder"}, // no name
			map[string]any{"name": "defaulted"},
			map[string]any{"name": "f", "type": "folder", "id": "id1", "settings": map[string]any{"path": "/x"}},
		},
	})
	if len(items) != 2 || items[0].ItemType != "container" || items[1].Settings != `{"path":"/x"}` {
		t.Fatalf("unexpected items: %+v", items)
	}

	// Items array with nothing usable falls through to item_sizes.
	items = buildImportJobItems(map[string]any{
		"items":      []any{"junk"},
		"item_sizes": map[string]any{"": 1.0, "a": 2.0},
	})
	if len(items) != 1 || items[0].ItemName != "a" {
		t.Fatalf("item_sizes fallback: %+v", items)
	}

	if items := buildImportJobItems(map[string]any{"source": "appdata.backup"}); items != nil {
		t.Fatalf("appdata without job name should yield nil, got %+v", items)
	}
	if items := buildImportJobItems(map[string]any{}); items != nil {
		t.Fatalf("empty manifest should yield nil, got %+v", items)
	}
}

func TestImportBackupsMetadataBranchesB458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)
	manifestID := strings.Repeat("ab", 32)

	backups := []map[string]any{
		{
			"job_name":       "imp-meta",
			"storage_path":   "imp-meta/run1",
			"items_done":     1.0,
			"items_failed":   2.0,
			"created_at":     "2026-01-02T03:04:05Z",
			"checksums":      map[string]any{"a": map[string]any{"f": "h"}},
			"verified":       true,
			"item_sizes":     map[string]any{"a": 10.0},
			"item_manifests": map[string]any{"a": manifestID, "skip": 5.0},
		},
		{
			"job_name":     "imp-meta",
			"storage_path": "imp-meta/run2",
			"timestamp":    "2026-01-03_101112",
		},
		{
			"job_name":     "imp-meta",
			"storage_path": "imp-meta/run3",
			"created_at":   "not-a-time",
			"timestamp":    "also-bad",
		},
		{"job_name": "too-new", "storage_path": "x/y", "version": 50.0},
		{"job_name": "", "storage_path": "x/z"},
	}
	seedImportManifests(t, storageDir, backups)
	n, err := r.ImportBackups(dest.ID, backups)
	if err != nil {
		t.Fatalf("ImportBackups: %v", err)
	}
	if n != 3 {
		t.Fatalf("imported %d, want 3", n)
	}
	job, err := d.GetJobByName("imp-meta")
	if err != nil {
		t.Fatal(err)
	}
	rps, _ := d.ListRestorePoints(job.ID)
	var withManifest bool
	for _, rp := range rps {
		if rp.StoragePath == "imp-meta/run1" {
			withManifest = hex.EncodeToString(rp.ManifestID) == manifestID
			if !strings.Contains(rp.Metadata, `"items_failed":2`) || !strings.Contains(rp.Metadata, `"verified":true`) {
				t.Errorf("metadata missing fields: %s", rp.Metadata)
			}
		}
	}
	if !withManifest {
		t.Fatal("single item manifest ID should be promoted to the restore point")
	}
}

func TestImportBackupsEncryptedPlaceholdersB458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)

	env, err := encodeManifestEnvelope("dedup", "aes-256-gcm", []byte("cipher"))
	if err != nil {
		t.Fatal(err)
	}
	// Two dedup envelopes: the repo open fails once and the cached failure
	// is reused for the second.
	b458WriteFile(t, filepath.Join(storageDir, "enc", "r1", "manifest.json"), string(env))
	b458WriteFile(t, filepath.Join(storageDir, "enc", "r2", "manifest.json"), string(env))
	// An age envelope with an undecryptable payload.
	ageEnv, _ := encodeManifestEnvelope("age", "age", []byte("not-age"))
	b458WriteFile(t, filepath.Join(storageDir, "enc", "r3", "manifest.json"), string(ageEnv))
	// A plaintext manifest presented as encrypted is closed and imported.
	b458WriteFile(t, filepath.Join(storageDir, "enc", "r4", "manifest.json"), `{"job_name":"enc"}`)

	backups := []map[string]any{
		{"encrypted": true, "storage_path": ""},
		{"encrypted": true, "storage_path": "enc/r1"},
		{"encrypted": true, "storage_path": "enc/r2"},
		{"encrypted": true, "storage_path": "enc/r3"},
		{"encrypted": true, "storage_path": "enc/missing"},
		{"encrypted": true, "storage_path": "enc/r4", "job_name": "enc"},
	}
	n, err := r.ImportBackups(dest.ID, backups, "wrong-pass")
	if err != nil {
		t.Fatalf("ImportBackups: %v", err)
	}
	if n != 1 {
		t.Fatalf("imported %d, want 1", n)
	}

	// Unknown destination and a destination whose adapter cannot be built.
	if n, _ := r.ImportBackups(987654, []map[string]any{{"encrypted": true, "storage_path": "enc/r1"}}); n != 0 {
		t.Fatalf("unknown destination imported %d", n)
	}
	badID, err := d.CreateStorageDestination(db.StorageDestination{Name: "bad", Type: "bogus-type", Config: "{}"})
	if err == nil {
		if n, _ := r.ImportBackups(badID, []map[string]any{{"encrypted": true, "storage_path": "enc/r1"}}); n != 0 {
			t.Fatalf("bogus adapter imported %d", n)
		}
	}
}

func TestImportBackupsDecryptedInvalidJSONB458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)
	writeEncryptedStorageFile(t, b458LocalAdapter(t, storageDir), "bad/r1/manifest.json.raw", "not json", "pw")
	raw, err := os.ReadFile(filepath.Join(storageDir, "bad", "r1", "manifest.json.raw"))
	if err != nil {
		t.Fatal(err)
	}
	env, _ := encodeManifestEnvelope("age", "age", raw)
	b458WriteFile(t, filepath.Join(storageDir, "bad", "r1", "manifest.json"), string(env))
	n, err := r.ImportBackups(dest.ID, []map[string]any{{"encrypted": true, "storage_path": "bad/r1"}}, "pw")
	if err != nil || n != 0 {
		t.Fatalf("invalid decrypted manifest: n=%d err=%v", n, err)
	}
}

func b458LocalAdapter(t *testing.T, dir string) storage.Adapter {
	t.Helper()
	return storage.NewLocalAdapter(dir)
}
