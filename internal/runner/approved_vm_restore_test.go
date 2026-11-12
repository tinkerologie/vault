package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

func TestApprovedVMBackupChainUsesSavedVMCheckpoint(t *testing.T) {
	for _, tt := range []struct {
		name, parentType, childType, predecessor string
		want                                     Status
		wantSteps                                int
	}{
		{name: "per-VM full fallback", childType: "full", want: Confirmed, wantSteps: 1},
		{name: "incremental", parentType: "full", childType: "incremental", predecessor: "base", want: Confirmed, wantSteps: 2},
		{name: "missing parent", childType: "incremental", predecessor: "base", want: Missing, wantSteps: 1},
		{name: "broken continuity", parentType: "full", childType: "incremental", predecessor: "other", want: Broken, wantSteps: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, d, storageRoot := setupTestRunner(t)
			r.serverKey = []byte("review-only test key")
			destination := createLocalDest(t, d, storageRoot)
			job := db.Job{Name: "vm-backup", Enabled: true, BackupTypeChain: "incremental", StorageDestID: destination.ID}
			var err error
			job.ID, err = d.CreateJob(job)
			if err != nil {
				t.Fatal(err)
			}
			runID, err := d.CreateJobRun(db.JobRun{JobID: job.ID, Status: "completed"})
			if err != nil {
				t.Fatal(err)
			}
			record := func(point, backupType, checkpoint, parent string, parentID int64) db.RestorePoint {
				dir := filepath.Join(storageRoot, "vm-backup", point, "test-vm")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				for name, payload := range map[string]string{
					"domain.xml":   `<domain><name>test-vm</name><uuid>22222222-3333-4444-aaaa-555555555555</uuid><devices><disk device="disk"><driver type="qcow2"/><source file="/mnt/user/domains/test-vm/disk.img"/><target dev="vda"/></disk></devices></domain>`,
					"vm_meta.json": fmt.Sprintf(`{"state":"shutoff","backup_type":%q,"checkpoint":%q,"parent_checkpoint":%q,"disks":[{"target":"vda","backup_file":"vdisk0.qcow2","format":"qcow2"}]}`, backupType, checkpoint, parent),
					"vdisk0.qcow2": "fixture-disk",
				} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(payload), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				rp := db.RestorePoint{
					JobRunID: runID, JobID: job.ID, BackupType: "incremental",
					StoragePath: "vm-backup/" + point, ParentRestorePointID: parentID,
					Metadata: `{"item_sizes":{"test-vm":50},"item_types":{"test-vm":"vm"}}`,
				}
				id, err := d.CreateRestorePoint(rp)
				if err != nil {
					t.Fatal(err)
				}
				rp.ID = id
				return rp
			}
			parentID := int64(500)
			if tt.parentType != "" {
				parentID = record("base", tt.parentType, "base", "", 0).ID
			}
			point := record("selected", tt.childType, "selected", tt.predecessor, parentID)
			item := ItemSelection{ItemType: "vm", ItemName: "test-vm"}
			review, err := r.InspectVMRestoreRequest(t.Context(), ApprovedRestorePlan{Job: job, Point: point, Item: item}, "", "")
			if err != nil || review.Chain.Status != tt.want || len(review.Chain.Steps) != tt.wantSteps {
				t.Fatalf("VM chain = %+v, err = %v", review.Chain, err)
			}
			if review.Chain.Steps[len(review.Chain.Steps)-1].RestorePointID != point.ID {
				t.Fatalf("selected point is not the last required step: %+v", review.Chain)
			}
		})
	}
}
