package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"testing"

	"golang.org/x/net/webdav"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/storage"
)

// WebDAV has no bounded directory listing, and VM disks there are split into
// chunks. The review must still confirm the backup from the names the Restore
// Point recorded.
func TestVMCovInspectConfirmsWebDAVBackupFromRecordedNames(t *testing.T) {
	f := newVMCovRunner(t)
	server := httptest.NewServer(&webdav.Handler{FileSystem: webdav.Dir(t.TempDir()), LockSystem: webdav.NewMemLS()})
	defer server.Close()
	config, err := json.Marshal(map[string]any{"url": server.URL, "chunk_size_mb": 1})
	if err != nil {
		t.Fatal(err)
	}
	destID, err := f.d.CreateStorageDestination(db.StorageDestination{Name: "vm-webdav", Type: "webdav", Config: string(config)})
	if err != nil {
		t.Fatal(err)
	}
	f.job.StorageDestID = destID
	if err := f.d.UpdateJob(f.job); err != nil {
		t.Fatal(err)
	}
	adapter, err := storage.NewAdapter("webdav", string(config))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.CloseAdapter(adapter)

	files := vmcovFiles("full", "base", "")
	files["vdisk0.qcow2"] = append(files["vdisk0.qcow2"], make([]byte, 3<<20)...)
	checksums := map[string]string{}
	for name, payload := range files {
		if err := adapter.Write("vm-backup/full/test-vm/"+name, bytes.NewReader(payload)); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(payload)
		checksums[name] = hex.EncodeToString(sum[:])
	}
	record := func(metadata map[string]any) db.RestorePoint {
		t.Helper()
		encoded, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		rp := db.RestorePoint{JobRunID: f.runID, JobID: f.job.ID, BackupType: "full", StoragePath: "vm-backup/full", Metadata: string(encoded)}
		if rp.ID, err = f.d.CreateRestorePoint(rp); err != nil {
			t.Fatal(err)
		}
		return rp
	}
	types, sizes := map[string]any{"test-vm": "vm"}, map[string]any{"test-vm": 50}

	point := record(map[string]any{"item_types": types, "item_sizes": sizes, "checksums": map[string]any{"test-vm": checksums}})
	review, err := f.inspect(t, point, "")
	if err != nil || review.Chain.Status != Confirmed || len(review.Reasons) != 0 || review.Effects == nil {
		t.Fatalf("review = %+v, err = %v", review, err)
	}
	in := f.host.reviews[len(f.host.reviews)-1]
	if !slices.Equal(in.Artifacts, []string{"domain.xml", "vdisk0.qcow2", "vm_meta.json"}) {
		t.Fatalf("reviewed artifacts = %v", in.Artifacts)
	}

	// Without recorded names there is nothing bounded to check.
	unrecorded := record(map[string]any{"item_types": types, "item_sizes": sizes})
	if _, err := f.inspect(t, unrecorded, ""); !errors.Is(err, ErrApprovedStorageUnavailable) {
		t.Fatalf("unrecorded names: %v", err)
	}
}

// statOnlyAdapter stands in for the SFTP review adapter: it never lists, and
// it records every object the review stats.
type statOnlyAdapter struct {
	storage.Adapter
	stats []string
}

func (a *statOnlyAdapter) List(string) ([]storage.FileInfo, error) {
	return nil, errors.New("bounded storage metadata unavailable")
}

func (a *statOnlyAdapter) Stat(p string) (storage.FileInfo, error) {
	a.stats = append(a.stats, p)
	return storage.FileInfo{Path: p, Size: 1}, nil
}

func TestVMCovSFTPReviewStatsOnlyRecordedObjects(t *testing.T) {
	t.Parallel()
	recorded := map[string]any{"domain.xml": "a", "vm_meta.json": "b", "vdisk0.qcow2": "c"}
	point := func(checksums map[string]any) db.RestorePoint {
		t.Helper()
		encoded, err := json.Marshal(map[string]any{"checksums": map[string]any{"test-vm": checksums}})
		if err != nil {
			t.Fatal(err)
		}
		return db.RestorePoint{StoragePath: "vm-backup/full", Metadata: string(encoded)}
	}

	adapter := &statOnlyAdapter{}
	files, err := listVMReviewStep(adapter, point(recorded), "test-vm")
	if err != nil || len(files) != 3 {
		t.Fatalf("files = %+v, err = %v", files, err)
	}
	slices.Sort(adapter.stats)
	want := []string{"vm-backup/full/test-vm/domain.xml", "vm-backup/full/test-vm/vdisk0.qcow2", "vm-backup/full/test-vm/vm_meta.json"}
	if !slices.Equal(adapter.stats, want) {
		t.Fatalf("stats = %v, want %v", adapter.stats, want)
	}

	// More recorded objects than the review allows are refused before any stat.
	many := map[string]any{}
	for i := range maxVMReviewObjects + 1 {
		many[fmt.Sprintf("vdisk%d.img", i)] = "x"
	}
	adapter = &statOnlyAdapter{}
	if _, err := listVMReviewStep(adapter, point(many), "test-vm"); !errors.Is(err, ErrApprovedStorageUnavailable) || len(adapter.stats) != 0 {
		t.Fatalf("over-limit recorded names: err = %v, stats = %d", err, len(adapter.stats))
	}
}
