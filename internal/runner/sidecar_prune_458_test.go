package runner

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/storage"
)

func c458IndexJSON(t *testing.T, entries ...engine.TarIndexEntry) string {
	t.Helper()
	b, err := json.Marshal(engine.TarIndex{Version: 1, Files: entries})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func c458Adapter(t *testing.T, dest db.StorageDestination) storage.Adapter {
	t.Helper()
	a, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.CloseAdapter(a) })
	return a
}

// c458ReadFailAdapter lists one sidecar candidate but fails to read it.
type c458ReadFailAdapter struct {
	storage.Adapter
	entries []storage.FileInfo
}

func (a c458ReadFailAdapter) List(string) ([]storage.FileInfo, error) { return a.entries, nil }
func (a c458ReadFailAdapter) Read(string) (io.ReadCloser, error) {
	return nil, errors.New("read refused")
}

func TestReadItemSidecarBranchesC458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)
	a := c458Adapter(t, dest)
	rp := db.RestorePoint{StoragePath: "job/run"}

	// Unknown storage type.
	if _, ok := r.ReadItemSidecar(db.StorageDestination{Type: "bogus"}, rp, "item", engine.ListingSuffix); ok {
		t.Fatal("bogus dest should not yield a sidecar")
	}
	// Missing item directory: List fails.
	if _, ok := r.ReadItemSidecar(dest, rp, "missing", engine.ListingSuffix); ok {
		t.Fatal("missing dir should not yield a sidecar")
	}

	// A sub-directory and a non-sidecar file only: no candidate.
	if err := os.MkdirAll(filepath.Join(storageDir, "job", "run", "item", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeStorageFiles(t, a, map[string]string{"job/run/item/readme.txt": "x"})
	if _, ok := r.ReadItemSidecar(dest, rp, "item", engine.ListingSuffix); ok {
		t.Fatal("no candidate should yield nothing")
	}

	// Malformed JSON sidecar.
	writeStorageFiles(t, a, map[string]string{"job/run/bad/data.tar.listing.json": "{nope"})
	if _, ok := r.ReadItemSidecar(dest, rp, "bad", engine.ListingSuffix); ok {
		t.Fatal("malformed sidecar should not parse")
	}

	// Encrypted sidecar: no passphrase, wrong passphrase, right passphrase.
	listing := c458IndexJSON(t, engine.TarIndexEntry{Path: "a.txt", Size: 1})
	writeEncryptedStorageFile(t, a, "job/run/enc/data.tar.listing.json.age", listing, "right-pass")
	if _, ok := r.readItemSidecar(a, rp, "enc", engine.ListingSuffix, ""); ok {
		t.Fatal("encrypted sidecar without passphrase should fail")
	}
	if _, ok := r.readItemSidecar(a, rp, "enc", engine.ListingSuffix, "wrong-pass"); ok {
		t.Fatal("wrong passphrase should fail")
	}
	idx, ok := r.readItemSidecar(a, rp, "enc", engine.ListingSuffix, "right-pass")
	if !ok || len(idx.Files) != 1 || idx.Files[0].Path != "a.txt" {
		t.Fatalf("decrypted sidecar = %+v, %v", idx, ok)
	}

	// Read failure on the chosen candidate.
	fail := c458ReadFailAdapter{entries: []storage.FileInfo{{Path: "job/run/x/data.tar.listing.json"}}}
	if _, ok := r.readItemSidecar(fail, rp, "x", engine.ListingSuffix, ""); ok {
		t.Fatal("read failure should yield nothing")
	}
}

func TestLoadParentListingPathsBranchesC458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)
	rp := &db.RestorePoint{StoragePath: "job/run"}
	if got := r.loadParentListingPaths(rp, db.StorageDestination{Type: "bogus"}, "item", ""); got != nil {
		t.Fatalf("bogus dest: %v", got)
	}
	if got := r.loadParentListingPaths(rp, dest, "item", ""); got != nil {
		t.Fatalf("no listing: %v", got)
	}
}

func TestLoadParentVolumeListingPathsBranchesC458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)
	a := c458Adapter(t, dest)
	rp := &db.RestorePoint{StoragePath: "job/run"}

	if m, _ := r.loadParentVolumeListingPaths(rp, db.StorageDestination{Type: "bogus"}, "c", ""); m != nil {
		t.Fatal("bogus dest should yield nil")
	}
	if m, _ := r.loadParentVolumeListingPaths(rp, dest, "absent", ""); m != nil {
		t.Fatal("missing dir should yield nil")
	}

	// Encrypted volumes.json without any passphrase available.
	writeEncryptedStorageFile(t, a, "job/run/encnopass/volumes.json.age", `[]`, "pw")
	if m, _ := r.loadParentVolumeListingPaths(rp, dest, "encnopass", ""); m != nil {
		t.Fatal("no passphrase should yield nil")
	}
	// Wrong passphrase.
	if m, _ := r.loadParentVolumeListingPaths(rp, dest, "encnopass", "nope"); m != nil {
		t.Fatal("wrong passphrase should yield nil")
	}

	// Malformed manifest.
	writeStorageFiles(t, a, map[string]string{"job/run/badjson/volumes.json": "{"})
	if m, _ := r.loadParentVolumeListingPaths(rp, dest, "badjson", ""); m != nil {
		t.Fatal("malformed manifest should yield nil")
	}

	// Only archive-less entries: nothing resolved.
	writeStorageFiles(t, a, map[string]string{"job/run/noarch/volumes.json": `[{"source":"/mnt/a"}]`})
	if m, _ := r.loadParentVolumeListingPaths(rp, dest, "noarch", ""); m != nil {
		t.Fatal("archive-less manifest should yield nil")
	}

	// Listing present but unparseable.
	writeStorageFiles(t, a, map[string]string{
		"job/run/badlist/volumes.json":                        `[{"source":"/mnt/a","archive":"volume_0.tar"}]`,
		"job/run/badlist/volume_0.tar" + engine.ListingSuffix: "{",
	})
	if m, _ := r.loadParentVolumeListingPaths(rp, dest, "badlist", ""); m != nil {
		t.Fatal("unparseable listing should yield nil")
	}
}

// c458PruneFixture builds a two-step folder chain on a local destination.
type c458PruneFixture struct {
	r       *Runner
	d       *db.DB
	dest    db.StorageDestination
	adapter storage.Adapter
	jobID   int64
	base    db.RestorePoint
	child   db.RestorePoint
}

func c458NewPruneFixture(t *testing.T, item string) c458PruneFixture {
	t.Helper()
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)
	jobID, err := d.CreateJob(db.Job{Name: "c458-prune", StorageDestID: dest.ID, BackupTypeChain: "incremental"})
	if err != nil {
		t.Fatal(err)
	}
	f := c458PruneFixture{
		r: r, d: d, dest: dest, adapter: c458Adapter(t, dest), jobID: jobID,
		base: db.RestorePoint{ID: 1, JobID: jobID, BackupType: "full", StoragePath: "c458-prune/1",
			Metadata: `{"item_sizes":{"` + item + `":1}}`, CreatedAt: time.Now().Add(-time.Hour)},
		child: db.RestorePoint{ID: 2, JobID: jobID, BackupType: "incremental", StoragePath: "c458-prune/2",
			ParentRestorePointID: 1, Metadata: `{"item_sizes":{"` + item + `":1}}`, CreatedAt: time.Now()},
	}
	return f
}

func TestPruneChainResurrectedEarlyReturnsC458(t *testing.T) {
	f := c458NewPruneFixture(t, "item")
	chain := []db.RestorePoint{f.base, f.child}

	// Unknown job.
	ghost := f.child
	ghost.JobID = 9999
	f.r.pruneChainResurrected([]db.RestorePoint{ghost}, "item", t.TempDir(), "", time.Now())

	// Job whose destination cannot build an adapter.
	badID, err := f.d.CreateStorageDestination(db.StorageDestination{Name: "c458-bogus", Type: "bogus", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	badJob, err := f.d.CreateJob(db.Job{Name: "c458-bogus-job", StorageDestID: badID})
	if err != nil {
		t.Fatal(err)
	}
	bad := f.child
	bad.JobID = badJob
	f.r.pruneChainResurrected([]db.RestorePoint{bad}, "item", t.TempDir(), "", time.Now())

	// No listing on the newest step.
	f.r.pruneChainResurrected(chain, "item", t.TempDir(), "", time.Now())

	// Listing present but the earlier step has no tar index.
	writeStorageFiles(t, f.adapter, map[string]string{
		"c458-prune/2/item/data.tar" + engine.ListingSuffix: c458IndexJSON(t),
	})
	dst := t.TempDir()
	keepFile := filepath.Join(dst, "keep.txt")
	if err := os.WriteFile(keepFile, []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.r.pruneChainResurrected(chain, "item", dst, "", time.Now())
	if _, err := os.Stat(keepFile); err != nil {
		t.Fatalf("missing index must skip the prune: %v", err)
	}

	// Destination directory that cannot be opened.
	writeStorageFiles(t, f.adapter, map[string]string{
		"c458-prune/1/item/data.tar" + engine.IndexSuffix: c458IndexJSON(t, engine.TarIndexEntry{Path: "x", Size: 1}),
	})
	f.r.pruneChainResurrected(chain, "item", filepath.Join(t.TempDir(), "does-not-exist"), "", time.Now())
}

func TestPruneChainResurrectedFolderDestinationC458(t *testing.T) {
	f := c458NewPruneFixture(t, "docs")
	dst := t.TempDir()
	settings, _ := json.Marshal(map[string]any{"path": dst})
	if _, err := f.d.AddJobItem(db.JobItem{JobID: f.jobID, ItemType: "folder", ItemName: "docs", Settings: string(settings)}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for name, body := range map[string]string{"stale.txt": "old!", "keep.txt": "kk", "wrongsize.txt": "abc"} {
		if err := os.WriteFile(filepath.Join(dst, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dst, "olddir", "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeStorageFiles(t, f.adapter, map[string]string{
		"c458-prune/1/docs/data.tar" + engine.IndexSuffix: c458IndexJSON(t,
			engine.TarIndexEntry{Path: "", IsDir: true},
			engine.TarIndexEntry{Path: "olddir", IsDir: true},
			engine.TarIndexEntry{Path: "olddir/inner", IsDir: true},
			engine.TarIndexEntry{Path: "stale.txt", Size: 4},
			engine.TarIndexEntry{Path: "wrongsize.txt", Size: 99},
			engine.TarIndexEntry{Path: "keep.txt", Size: 2},
		),
		"c458-prune/2/docs/data.tar" + engine.ListingSuffix: c458IndexJSON(t,
			engine.TarIndexEntry{Path: "keep.txt", Size: 2},
		),
	})

	f.r.pruneChainResurrected([]db.RestorePoint{f.base, f.child}, "docs", "", "", start)

	if _, err := os.Stat(filepath.Join(dst, "stale.txt")); !os.IsNotExist(err) {
		t.Errorf("stale.txt should be pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "olddir")); !os.IsNotExist(err) {
		t.Errorf("empty olddir tree should be pruned: %v", err)
	}
	for _, kept := range []string{"keep.txt", "wrongsize.txt"} {
		if _, err := os.Stat(filepath.Join(dst, kept)); err != nil {
			t.Errorf("%s should survive: %v", kept, err)
		}
	}
}

func TestPruneChainResurrectedPluginFallbackPathC458(t *testing.T) {
	f := c458NewPruneFixture(t, "c458-no-such-plugin")
	// Plugin item without a path or item ID: destination falls back to the
	// plugin config dir derived from the name, which does not exist here,
	// so the prune returns without touching anything.
	if _, err := f.d.AddJobItem(db.JobItem{JobID: f.jobID, ItemType: "plugin", ItemName: "c458-no-such-plugin", Settings: "{}"}); err != nil {
		t.Fatal(err)
	}
	writeStorageFiles(t, f.adapter, map[string]string{
		"c458-prune/1/c458-no-such-plugin/config.tar" + engine.IndexSuffix:   c458IndexJSON(t, engine.TarIndexEntry{Path: "a", Size: 1}),
		"c458-prune/2/c458-no-such-plugin/config.tar" + engine.ListingSuffix: c458IndexJSON(t),
	})
	if _, err := os.Stat(engine.PluginPath("c458-no-such-plugin")); err == nil {
		t.Skip("unexpected plugin dir present on host")
	}
	f.r.pruneChainResurrected([]db.RestorePoint{f.base, f.child}, "c458-no-such-plugin", "", "", time.Now())

	// No matching job item at all: same name-derived fallback.
	f2 := c458NewPruneFixture(t, "c458-orphan")
	writeStorageFiles(t, f2.adapter, map[string]string{
		"c458-prune/1/c458-orphan/data.tar" + engine.IndexSuffix:   c458IndexJSON(t, engine.TarIndexEntry{Path: "a", Size: 1}),
		"c458-prune/2/c458-orphan/data.tar" + engine.ListingSuffix: c458IndexJSON(t),
	})
	f2.r.pruneChainResurrected([]db.RestorePoint{f2.base, f2.child}, "c458-orphan", "", "", time.Now())
}
