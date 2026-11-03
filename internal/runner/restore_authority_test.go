package runner

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/storage"
)

func objects(names ...string) []storage.FileInfo {
	out := make([]storage.FileInfo, 0, len(names))
	for _, n := range names {
		out = append(out, storage.FileInfo{Path: filepath.Join("job", "item", n)})
	}
	return out
}

func TestSelectRestoreObjects(t *testing.T) {
	names := func(files []storage.FileInfo) []string {
		var out []string
		for _, f := range files {
			out = append(out, filepath.Base(f.Path))
		}
		return out
	}
	t.Run("an unlisted object cannot land on a verified file", func(t *testing.T) {
		got, unlisted, err := selectRestoreObjects(objects("volume_0.tar", "volume_0.tar.gz", ".DS_Store"), map[string]string{"volume_0.tar": "h"})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(names(got), []string{"volume_0.tar"}) || !slices.Equal(unlisted, []string{"volume_0.tar.gz", ".DS_Store"}) {
			t.Fatalf("got %v, unlisted %v", names(got), unlisted)
		}
	})
	t.Run("backups with no checksums keep every object", func(t *testing.T) {
		got, unlisted, err := selectRestoreObjects(append(objects("a.tar", "b.tar.gz"), storage.FileInfo{Path: "dir", IsDir: true}), nil)
		if err != nil || len(got) != 2 || len(unlisted) != 0 {
			t.Fatalf("got %v, unlisted %v, err %v", names(got), unlisted, err)
		}
	})
	for _, clash := range [][]string{{"a.tar", "a.tar.gz"}, {"a.tar.age", "a.tar.zst"}, {"a.tar.gz.age", "a.tar"}} {
		t.Run("colliding "+strings.Join(clash, " "), func(t *testing.T) {
			if _, _, err := selectRestoreObjects(objects(clash...), nil); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestRestorePointVouched(t *testing.T) {
	r, database := newTestRunner(t)
	dest, err := database.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	local, err := database.CreateJob(db.Job{Name: "local", StorageDestID: dest})
	if err != nil {
		t.Fatal(err)
	}
	const checksums = `{"checksums":{"web":{"config.json":"h"}}}`
	cases := []struct {
		name string
		rp   db.RestorePoint
		want bool
	}{
		{"own backup with checksums", db.RestorePoint{JobID: local, Metadata: checksums}, true},
		{"own chunked backup", db.RestorePoint{JobID: local, Metadata: `{"item_manifests":{"web":"` + strings.Repeat("ab", 32) + `"}}`}, true},
		{"own backup without checksums (Verify backup off)", db.RestorePoint{JobID: local, Metadata: `{}`}, true},
		{"imported, whatever its manifest claims", db.RestorePoint{JobID: local, Metadata: `{"imported":true,"checksums":{"web":{"config.json":"h"}}}`}, false},
		{"replicated", db.RestorePoint{JobID: local, SourceID: 7, Metadata: checksums}, false},
		{"unreadable metadata", db.RestorePoint{JobID: local, Metadata: `{`}, false},
		{"unknown job", db.RestorePoint{JobID: 9999, Metadata: checksums}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := r.restorePointVouched(c.rp, "web"); got != c.want {
				t.Errorf("got %v", got)
			}
		})
	}

	own := db.RestorePoint{JobID: local, Metadata: `{"item_sizes":{"web":1},"checksums":{"web":{"a":"h"}}}`}
	bare := db.RestorePoint{JobID: local, Metadata: `{"item_sizes":{"web":1}}`}
	imported := db.RestorePoint{JobID: local, Metadata: `{"item_sizes":{"web":1},"imported":true,"checksums":{"web":{"a":"h"}}}`}
	other := db.RestorePoint{JobID: local, Metadata: `{"item_sizes":{"other":1}}`}
	if !r.chainSidecarsVouched([]db.RestorePoint{own, other, own}, "web") {
		t.Error("fully checksummed chain refused")
	}
	if r.chainSidecarsVouched([]db.RestorePoint{own, bare}, "web") {
		t.Error("step with no checksums vouched for")
	}
	if r.chainSidecarsVouched([]db.RestorePoint{imported, own}, "web") {
		t.Error("imported step vouched for")
	}
}

func TestRestoreItemChainSetsTrustForTheEngine(t *testing.T) {
	r, _ := newTestRunner(t)
	ctx := engine.WithRestoreTrust(context.Background(), engine.RestoreTrust{AllowUnpinnedImage: true})
	// No such job: staging fails, and what matters is nothing panics and the
	// approval is untouched for the caller.
	_ = r.restoreItemChain(ctx, db.RestorePoint{JobID: 9999, Metadata: `{}`}, "web", "container", "", "", nil, false, restoreProgressReporter{})
	if got := engine.RestoreTrustFrom(ctx); got.ArtifactTrusted || !got.AllowUnpinnedImage {
		t.Errorf("trust = %+v", got)
	}
}

func TestStageRestorePointItemIgnoresPlantedObject(t *testing.T) {
	r, database := newTestRunner(t)
	storageRoot := t.TempDir()
	storageConfig := `{"path":"` + storageRoot + `"}`
	dest, err := database.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: storageConfig})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := database.CreateJob(db.Job{Name: "j", Compression: "none", StorageDestID: dest})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := storage.NewAdapter("local", storageConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.CloseAdapter(adapter) })
	recorded := writeStorageFiles(t, adapter, map[string]string{"j/1_full/web/volume_0.tar": "verified"})
	// Written after the backup: gzip, so it decompresses onto volume_0.tar.
	var planted bytes.Buffer
	gz := gzip.NewWriter(&planted)
	_, _ = gz.Write([]byte("planted"))
	_ = gz.Close()
	if err := adapter.Write("j/1_full/web/volume_0.tar.gz", bytes.NewReader(planted.Bytes())); err != nil {
		t.Fatal(err)
	}
	rp := db.RestorePoint{ID: 1, JobID: jobID, BackupType: "full", StoragePath: "j/1_full", Metadata: restorePointMetadata("web", recorded)}
	tmp := t.TempDir()
	if err := r.stageRestorePointItem(context.Background(), rp, "web", tmp, "", 0, 100, restoreProgressReporter{}); err != nil {
		t.Fatal(err)
	}
	assertFileContents(t, tmp, "volume_0.tar", "verified")

	// Without checksums nothing is filtered, but the collision is refused.
	rp.Metadata = `{}`
	err = r.stageRestorePointItem(context.Background(), rp, "web", t.TempDir(), "", 0, 100, restoreProgressReporter{})
	if err == nil || !strings.Contains(err.Error(), "would both restore to") {
		t.Fatalf("err = %v", err)
	}
}

func TestPruneVouchedContainerChain(t *testing.T) {
	setup := func(t *testing.T) (steps []string, merged, deleted string) {
		t.Helper()
		tmp := t.TempDir()
		source := filepath.Join(tmp, "appdata")
		if err := os.MkdirAll(source, 0o750); err != nil {
			t.Fatal(err)
		}
		deleted = filepath.Join(source, "delete-me.txt")
		for name, body := range map[string]string{"keep.txt": "keep", "delete-me.txt": "gone"} {
			if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		archive := "volume_abcdef0123456789.tar"
		steps = []string{filepath.Join(tmp, "step_0"), filepath.Join(tmp, "step_1")}
		writeVolumeStep(t, steps[0], archive, source, map[string]string{"keep.txt": "keep", "delete-me.txt": "gone"}, false)
		writeVolumeStep(t, steps[1], archive, source, map[string]string{"keep.txt": "keep"}, true)
		merged = filepath.Join(tmp, "merged")
		writeMergedConfig(t, merged, archive, source)
		return steps, merged, deleted
	}
	r, database := newTestRunner(t)
	dest, err := database.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := database.CreateJob(db.Job{Name: "j", StorageDestID: dest})
	if err != nil {
		t.Fatal(err)
	}
	vouched := db.RestorePoint{ID: 2, JobID: jobID, Metadata: `{"checksums":{"web":{"a":"h"}}}`}

	t.Run("checked file lists prune", func(t *testing.T) {
		steps, merged, deleted := setup(t)
		r.pruneVouchedContainerChain([]db.RestorePoint{vouched, vouched}, "web", steps, merged, "", restoreProgressReporter{})
		if _, err := os.Stat(deleted); !os.IsNotExist(err) {
			t.Fatalf("resurrected file kept: %v", err)
		}
	})
	t.Run("unchecked file lists delete nothing", func(t *testing.T) {
		steps, merged, deleted := setup(t)
		bare := db.RestorePoint{ID: 1, JobID: jobID, Metadata: `{}`}
		r.pruneVouchedContainerChain([]db.RestorePoint{bare, vouched}, "web", steps, merged, "", restoreProgressReporter{})
		if _, err := os.Stat(deleted); err != nil {
			t.Fatalf("file deleted on the word of unchecked sidecars: %v", err)
		}
	})
}

func TestChainProvenanceLocal(t *testing.T) {
	r, database := newTestRunner(t)
	dest, err := database.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := database.CreateJob(db.Job{Name: "j", StorageDestID: dest})
	if err != nil {
		t.Fatal(err)
	}
	own := db.RestorePoint{JobID: jobID, Metadata: `{"item_sizes":{"web":1}}`}
	imported := db.RestorePoint{JobID: jobID, Metadata: `{"item_sizes":{"web":1},"imported":true}`}
	otherItem := db.RestorePoint{JobID: jobID, Metadata: `{"item_sizes":{"other":1},"imported":true}`}
	if !r.chainProvenanceLocal([]db.RestorePoint{own, otherItem, own}, "web") {
		t.Error("own chain refused")
	}
	if r.chainProvenanceLocal([]db.RestorePoint{imported, own}, "web") {
		t.Error("imported base step trusted")
	}
}

func TestRestoreItemPointsApprovalsToTheUI(t *testing.T) {
	refused := fmt.Errorf("%w: restoring web would grant host access", engine.ErrRestoreNeedsApproval)
	got := approvalInUI(refused)
	if !errors.Is(got, engine.ErrRestoreNeedsApproval) || !strings.Contains(got.Error(), "Vault UI") {
		t.Fatalf("err = %v", got)
	}
	other := errors.New("disk full")
	if approvalInUI(other) != other || approvalInUI(nil) != nil {
		t.Fatal("unrelated errors changed")
	}
	// RestoreItem itself carries no approvals into the restore.
	r, _ := newTestRunner(t)
	if err := r.RestoreItem(db.RestorePoint{JobID: 9999, Metadata: `{}`}, "web", "container", "", ""); err == nil {
		t.Fatal("want error for an unknown job")
	}
}
