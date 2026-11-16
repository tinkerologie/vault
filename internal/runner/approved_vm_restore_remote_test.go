package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
