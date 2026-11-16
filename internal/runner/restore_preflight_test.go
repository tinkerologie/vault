package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/tempdir"
)

// stubFreeSpace overrides freeSpaceAt for a test and restores it after.
func stubFreeSpace(t *testing.T, free int64) {
	t.Helper()
	orig := freeSpaceAt
	freeSpaceAt = func(string) (int64, error) { return free, nil }
	t.Cleanup(func() { freeSpaceAt = orig })
}

func checkByID(res PreflightResult, id string) PreflightCheck {
	for _, c := range res.Checks {
		if c.ID == id {
			return c
		}
	}
	return PreflightCheck{}
}

func newLocalDest(t *testing.T, d *db.DB, storageDir string, dedup bool) (db.StorageDestination, db.Job) {
	t.Helper()
	id, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "local", Type: "local", Config: `{"path":"` + storageDir + `"}`, DedupEnabled: dedup,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	return db.StorageDestination{ID: id}, db.Job{ID: 1, StorageDestID: id, Encryption: "none"}
}

func TestPreflightRestore_AllGreen(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close() //nolint:errcheck

	storageDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(storageDir, "job1/run1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageDir, "job1/run1/config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, job := newLocalDest(t, d, storageDir, false)
	rp := db.RestorePoint{StoragePath: "job1/run1", SizeBytes: 1000}

	stubFreeSpace(t, 1<<40) // 1 TiB free
	r := &Runner{db: d}
	res := r.PreflightRestore(job, rp, PreflightRestoreOptions{})

	if !res.OK {
		t.Fatalf("expected OK preflight, got %+v", res)
	}
	if s := checkByID(res, "reachable").Status; s != "ok" {
		t.Errorf("reachable = %q, want ok", s)
	}
	if s := checkByID(res, "present").Status; s != "ok" {
		t.Errorf("present = %q, want ok", s)
	}
	if s := checkByID(res, "decryptable").Status; s != "skip" {
		t.Errorf("decryptable = %q, want skip (unencrypted)", s)
	}
	if s := checkByID(res, "space").Status; s != "ok" {
		t.Errorf("space = %q, want ok", s)
	}
}

func TestPreflightRestore_MissingRestorePointFails(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()           //nolint:errcheck
	storageDir := t.TempDir() // no restore-point dir created
	_, job := newLocalDest(t, d, storageDir, false)
	rp := db.RestorePoint{StoragePath: "job1/gone", SizeBytes: 1000}

	stubFreeSpace(t, 1<<40)
	res := (&Runner{db: d}).PreflightRestore(job, rp, PreflightRestoreOptions{})

	if res.OK {
		t.Fatal("expected preflight to fail when restore point is missing on storage")
	}
	if s := checkByID(res, "present").Status; s != "fail" {
		t.Errorf("present = %q, want fail", s)
	}
}

func TestPreflightRestore_EncryptedPassphrase(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close() //nolint:errcheck
	storageDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(storageDir, "j/r"), 0o755)
	_ = os.WriteFile(filepath.Join(storageDir, "j/r/x"), []byte("x"), 0o644)
	_, job := newLocalDest(t, d, storageDir, false)
	job.Encryption = "age"
	hash, _ := crypto.HashPassphrase("correct horse")
	if err := d.SetSetting("encryption_passphrase_hash", hash); err != nil {
		t.Fatal(err)
	}
	rp := db.RestorePoint{StoragePath: "j/r", SizeBytes: 10}
	stubFreeSpace(t, 1<<40)
	r := &Runner{db: d}

	// Missing passphrase -> blocking fail.
	if res := r.PreflightRestore(job, rp, PreflightRestoreOptions{}); res.OK || checkByID(res, "decryptable").Status != "fail" {
		t.Errorf("empty passphrase: want decryptable fail, got %+v", checkByID(res, "decryptable"))
	}
	// Wrong passphrase -> fail.
	if res := r.PreflightRestore(job, rp, PreflightRestoreOptions{Passphrase: "wrong"}); res.OK || checkByID(res, "decryptable").Status != "fail" {
		t.Errorf("wrong passphrase: want decryptable fail, got %+v", checkByID(res, "decryptable"))
	}
	// Correct passphrase -> ok.
	if res := r.PreflightRestore(job, rp, PreflightRestoreOptions{Passphrase: "correct horse"}); !res.OK || checkByID(res, "decryptable").Status != "ok" {
		t.Errorf("correct passphrase: want decryptable ok + OK, got %+v", res)
	}
}

func TestPreflightRestore_LowSpaceWarns(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close() //nolint:errcheck
	storageDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(storageDir, "j/r"), 0o755)
	_ = os.WriteFile(filepath.Join(storageDir, "j/r/x"), []byte("x"), 0o644)
	_, job := newLocalDest(t, d, storageDir, false)
	rp := db.RestorePoint{StoragePath: "j/r", SizeBytes: 10_000_000}

	stubFreeSpace(t, 1000) // far less than the backup size
	res := (&Runner{db: d}).PreflightRestore(job, rp, PreflightRestoreOptions{})

	// Low space is a non-blocking warning: still OK overall, but flagged.
	if !res.OK {
		t.Errorf("low space should not block, got OK=false: %+v", res)
	}
	if s := checkByID(res, "space").Status; s != "warn" {
		t.Errorf("space = %q, want warn", s)
	}
}

func TestPreflightRestore_UsesCacheForStaging(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close() //nolint:errcheck
	storageDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(storageDir, "backup.tar"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, job := newLocalDest(t, d, storageDir, false)

	cache := t.TempDir()
	t.Cleanup(tempdir.SetCachePathsForTest([]string{cache}))
	original := freeSpaceAt
	t.Cleanup(func() { freeSpaceAt = original })
	freeSpaceAt = func(path string) (int64, error) {
		switch path {
		case cache:
			return 825 << 30, nil
		case os.TempDir():
			return 13 << 30, nil
		default:
			return 0, fmt.Errorf("unexpected probe path %q", path)
		}
	}

	point := db.RestorePoint{
		StoragePath: ".", BackupType: "full", SizeBytes: 48 << 30,
		Metadata: `{"item_sizes":{"vm":21152713933,"other":30386893619}}`,
	}
	result := (&Runner{db: d}).PreflightRestore(job, point, PreflightRestoreOptions{Items: []string{"vm"}})
	check := checkByID(result, "space")
	if check.Status != "ok" || !strings.Contains(check.Detail, cache) ||
		!strings.Contains(check.Detail, "Estimated space for the selected items is 19.7 GiB") ||
		!strings.Contains(check.Detail, "VM disk and NVRAM") {
		t.Errorf("staging result = %+v, want the cache filesystem to fit the backup", check)
	}
	if _, err := os.Lstat(filepath.Join(cache, tempdir.StageDirName)); !os.IsNotExist(err) {
		t.Errorf("preflight created a staging directory: lstat error = %v", err)
	}
}

func TestPreflightRestore_SpaceEstimates(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close() //nolint:errcheck
	_, job := newLocalDest(t, d, t.TempDir(), false)
	r := &Runner{db: d}
	point := db.RestorePoint{SizeBytes: 48 << 30, Metadata: `{"item_sizes":{"vm":21474836480,"empty":0,"negative":-1,"large":9223372036854775807,"other":2147483648}}`}
	stubFreeSpace(t, 30<<30)

	for _, tc := range []struct {
		name, status, detail string
		items                []string
	}{
		{"selected VM fits", "ok", "Estimated space for the selected items is 20.0 GiB", []string{"vm"}},
		{"sum without duplicate VM", "ok", "Estimated space for the selected items is 22.0 GiB", []string{"vm", "vm", "other"}},
		{"missing size falls back", "warn", "Estimated space for the whole backup is 48.0 GiB", []string{"vm", "unknown"}},
		{"negative size falls back", "warn", "Estimated space for the whole backup is 48.0 GiB", []string{"negative"}},
		{"overflow falls back", "warn", "Estimated space for the whole backup is 48.0 GiB", []string{"large", "vm"}},
		{"recorded zero remains zero", "skip", "Estimated space for the selected items is 0 B", []string{"empty"}},
		{"legacy unscoped request", "warn", "Estimated space for the whole backup is 48.0 GiB", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := r.PreflightRestore(job, point, PreflightRestoreOptions{Items: tc.items})
			check := checkByID(result, "space")
			if !result.OK || check.Status != tc.status || !strings.Contains(check.Detail, tc.detail) {
				t.Errorf("space = %+v, overall ok = %v", check, result.OK)
			}
		})
	}

	point.SizeBytes = 0
	result := r.PreflightRestore(job, point, PreflightRestoreOptions{})
	if check := checkByID(result, "space"); check.Status != "skip" || !strings.Contains(check.Detail, "Estimated space for the whole backup is unknown") {
		t.Errorf("unknown requirement = %+v", check)
	}
}

func TestPreflightRestore_SeparatesDestinationAndStaging(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close() //nolint:errcheck
	_, job := newLocalDest(t, d, t.TempDir(), false)
	stage, parent := t.TempDir(), t.TempDir()
	if err := d.SetSetting("staging_dir_override", stage); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(parent, "new", "folder")
	denied := filepath.Join(parent, "locked")
	dangling := filepath.Join(parent, "dangling")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	beneathDangling := filepath.Join(dangling, "new")
	original := freeSpaceAt
	t.Cleanup(func() { freeSpaceAt = original })
	freeSpaceAt = func(path string) (int64, error) {
		switch path {
		case stage:
			return 40 << 30, nil
		case parent:
			return 5 << 30, nil
		case dest, filepath.Dir(dest), dangling, beneathDangling:
			return 0, fmt.Errorf("statfs: %w", os.ErrNotExist)
		case denied:
			return 0, fmt.Errorf("statfs: %w", os.ErrPermission)
		default:
			t.Errorf("unexpected probe path %q", path)
			return 0, errors.New("unexpected probe")
		}
	}
	point := db.RestorePoint{BackupType: "incremental", SizeBytes: 20 << 30}
	for _, tc := range []struct {
		dest, status, detail string
		zfs                  bool
	}{
		{dest, "warn", "5.0 GiB free at " + parent, false},
		{denied, "skip", "Could not determine free space at " + denied, false},
		{beneathDangling, "skip", "Could not determine free space at " + dangling, false},
		{"pool/clone", "skip", "ZFS dataset target", true},
	} {
		result := (&Runner{db: d}).PreflightRestore(job, point, PreflightRestoreOptions{Destination: tc.dest, DestinationIsZFS: tc.zfs})
		stageCheck, destinationCheck := checkByID(result, "space"), checkByID(result, "destination_space")
		if !result.OK || stageCheck.Status != "ok" || !strings.Contains(stageCheck.Detail, "Earlier Backup Chain steps") {
			t.Errorf("staging = %+v, overall ok = %v", stageCheck, result.OK)
		}
		if destinationCheck.Status != tc.status || !strings.Contains(destinationCheck.Detail, tc.detail) {
			t.Errorf("destination = %+v, want %s with %q", destinationCheck, tc.status, tc.detail)
		}
	}
}

func TestPreflightRestore_ArchiveExpansionAndSharedSpace(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close() //nolint:errcheck
	_, job := newLocalDest(t, d, t.TempDir(), false)
	stage, destination := t.TempDir(), t.TempDir()
	if err := d.SetSetting("staging_dir_override", stage); err != nil {
		t.Fatal(err)
	}
	stubFreeSpace(t, 30<<30)
	r := &Runner{db: d}
	point := db.RestorePoint{BackupType: "full", SizeBytes: 20 << 30, Metadata: `{"item_sizes":{"data":21474836480}}`}
	for _, tc := range []struct {
		name, dest, check, status, detail string
	}{
		{"unknown expanded size", "", "space", "skip", "Expanded archive sizes are unavailable"},
		{"two copies share free space", destination, "destination_space", "warn", "Estimated space for the selected items is 40.0 GiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The test Job supplies a folder item type just as the HTTP handler does.
			result := r.PreflightRestore(job, point, PreflightRestoreOptions{Items: []string{"data"}, Destination: tc.dest, ItemTypes: map[string]string{"data": "folder"}})
			check := checkByID(result, tc.check)
			if !result.OK || check.Status != tc.status || !strings.Contains(check.Detail, tc.detail) {
				t.Errorf("check = %+v, overall ok = %v", check, result.OK)
			}
		})
	}

	point.Metadata = `{"item_sizes":{"data":21474836480},"item_manifests":{"data":"0000000000000000000000000000000000000000000000000000000000000001"}}`
	result := r.PreflightRestore(job, point, PreflightRestoreOptions{Items: []string{"data"}, Destination: destination, ItemTypes: map[string]string{"data": "folder"}})
	if check := checkByID(result, "destination_space"); check.Status != "ok" ||
		!strings.Contains(check.Detail, "Estimated space for the selected items is 20.0 GiB") {
		t.Errorf("direct dedup restore should check one copy of the data: %+v", check)
	}

	stubFreeSpace(t, 150<<30)
	point.Metadata = `{"item_sizes":{"vm":10737418240,"data":107374182400},"item_manifests":{"data":"0000000000000000000000000000000000000000000000000000000000000001"}}`
	result = r.PreflightRestore(job, point, PreflightRestoreOptions{
		Items: []string{"vm", "data"}, Destination: destination, ItemTypes: map[string]string{"vm": "vm", "data": "folder"},
	})
	if check := checkByID(result, "space"); check.Status != "ok" ||
		!strings.Contains(check.Detail, "Estimated space for the selected items that use staging is 10.0 GiB") {
		t.Errorf("only the VM uses classic staging: %+v", check)
	}
	if check := checkByID(result, "destination_space"); check.Status != "ok" ||
		!strings.Contains(check.Detail, "Estimated space for the selected items is 120.0 GiB") {
		t.Errorf("shared space should include both target copies and only the staged VM: %+v", check)
	}
}
