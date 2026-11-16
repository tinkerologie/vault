package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/storage"
)

// fldcovMetadata is a scripted metadata provider for evidence probes.
type fldcovMetadata struct {
	stat  map[string]storage.FileInfo
	err   error
	calls int
}

func (m *fldcovMetadata) Stat(name string) (storage.FileInfo, error) {
	m.calls++
	if info, ok := m.stat[name]; ok {
		return info, nil
	}
	if m.err != nil {
		return storage.FileInfo{}, m.err
	}
	return storage.FileInfo{}, fs.ErrNotExist
}

func (m *fldcovMetadata) List(string) ([]storage.FileInfo, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return nil, fs.ErrNotExist
}

func TestEvidenceProbeStatusesForUnusableMetadata(t *testing.T) {
	f := newFldcovFixture(t)
	folder := ItemSelection{ItemType: "folder", ItemName: "archive"}
	unavailable := errors.New("fixture provider unavailable")

	exhausted := &evidenceProbe{adapter: &fldcovMetadata{}, remaining: 0}
	if _, status := exhausted.stat("anything"); status != Unknown {
		t.Fatalf("exhausted budget: %v", status)
	}

	probe := &evidenceProbe{adapter: &fldcovMetadata{err: unavailable}, remaining: 8}
	if status := probe.deduplicated(t.Context(), f.db, f.dest.ID, dedup.ID{1}); status != Unknown {
		t.Fatalf("unreadable repository header: %v", status)
	}
	header := map[string]storage.FileInfo{dedup.RepoRoot + "/repo.json": {Size: 10}}
	probe = &evidenceProbe{adapter: &fldcovMetadata{stat: header}, remaining: 8}
	if status := probe.deduplicated(t.Context(), f.db, f.dest.ID, dedup.ID{1}); status != Missing {
		t.Fatalf("unindexed manifest: %v", status)
	}

	for name, point := range map[string]db.RestorePoint{
		"escaping storage path": {StoragePath: "../outside"},
		"too many checksums":    {StoragePath: "job/full", Metadata: fldcovChecksums(maxEvidenceObjects + 1)},
	} {
		probe := &evidenceProbe{adapter: &fldcovMetadata{}, remaining: 8}
		if status := probe.classic(point, folder); status != Unknown {
			t.Fatalf("%s: %v", name, status)
		}
	}
	probe = &evidenceProbe{adapter: &fldcovMetadata{}, remaining: 8}
	if status := probe.classic(db.RestorePoint{StoragePath: "job/full"}, ItemSelection{ItemType: "folder", ItemName: "nested/../archive"}); status != Unknown {
		t.Fatalf("non-canonical item name: %v", status)
	}
	listing := &fldcovMetadata{err: unavailable}
	probe = &evidenceProbe{adapter: listing, remaining: 8}
	badName := db.RestorePoint{StoragePath: "job/full", Metadata: `{"checksums":{"archive":{"nested/data.tar":"abc"}}}`}
	if status := probe.classic(badName, folder); status != Broken {
		t.Fatalf("checksum names a nested object: %v", status)
	}

	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	probe = &evidenceProbe{adapter: &fldcovMetadata{stat: header}, remaining: 8}
	if status := probe.deduplicated(context.Background(), f.db, f.dest.ID, dedup.ID{1}); status != Unknown {
		t.Fatalf("catalog unavailable: %v", status)
	}
	if result := recoveryVerification(context.Background(), f.db, db.RestorePoint{ID: 1}); result.Status != "unknown" {
		t.Fatalf("verification without catalog: %+v", result)
	}
	_, failure := walkRestoreChain(context.Background(), f.db, db.RestorePoint{ID: 2, BackupType: "incremental", ParentRestorePointID: 1}, 0)
	if failure == nil || failure.reason != "catalog_unavailable" || failure.status != Unknown {
		t.Fatalf("chain without catalog: %+v", failure)
	}
	if !strings.Contains(failure.Error(), "getting parent restore point 1") || errors.Unwrap(failure) == nil {
		t.Fatalf("chain error does not explain itself: %v", failure)
	}
}

func fldcovChecksums(n int) string {
	entries := make([]string, n)
	for i := range entries {
		entries[i] = fmt.Sprintf(`"part-%d.tar":"x"`, i)
	}
	return `{"checksums":{"archive":{` + strings.Join(entries, ",") + `}}}`
}

func TestInspectRecoveryClassifiesUnknownPointsAndBackupTypes(t *testing.T) {
	f := newFldcovFixture(t)
	folder := ItemSelection{ItemType: "folder", ItemName: "archive"}
	wrap := func(adapter storage.Adapter) RecoveryMetadata { return adapter }
	if _, err := InspectRecovery(t.Context(), f.db, 424242, folder, wrap); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("unknown point: %v", err)
	}
	odd := f.point(t, "snapshot", "odd/point", 0, `{"item_sizes":{"archive":1}}`)
	evidence, err := InspectRecovery(t.Context(), f.db, odd.ID, folder, wrap)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Chain.Status != Unknown || evidence.Chain.Reason != "unknown_backup_type" {
		t.Fatalf("unrecognized backup type: %+v", evidence.Chain)
	}
}

func TestProbeRecoveryArtifactsWithoutUsableStorage(t *testing.T) {
	f := newFldcovFixture(t)
	folder := ItemSelection{ItemType: "folder", ItemName: "archive"}
	point := f.point(t, "full", "probe/full", 0, "")
	steps := []RecoveryChainStep{{Artifacts: Unknown, Membership: Confirmed}}
	wrap := func(adapter storage.Adapter) RecoveryMetadata { return adapter }

	if got := probeRecoveryArtifacts(t.Context(), f.db, 424242, []db.RestorePoint{point}, steps, folder, false, wrap); got[0].status != "" {
		t.Fatalf("unknown Job produced evidence: %+v", got)
	}

	brokenDest, err := f.db.CreateStorageDestination(db.StorageDestination{Name: "Broken", Type: "fixture-unknown-storage", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	brokenJob, err := f.db.CreateJob(db.Job{Name: "Broken storage Job", StorageDestID: brokenDest})
	if err != nil {
		t.Fatal(err)
	}
	if got := probeRecoveryArtifacts(t.Context(), f.db, brokenJob, []db.RestorePoint{point}, steps, folder, false, wrap); got[0].status != "" {
		t.Fatalf("unusable storage produced evidence: %+v", got)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if got := probeRecoveryArtifacts(cancelled, f.db, f.job.ID, []db.RestorePoint{point}, steps, folder, false, wrap); got[0].status != "" {
		t.Fatalf("cancelled probe produced evidence: %+v", got)
	}
}

func TestRecoveryEvidenceRefusesInvalidSelectionsAndSaturatedProbes(t *testing.T) {
	f := newFldcovFixture(t)
	wrap := func(adapter storage.Adapter) RecoveryMetadata { return adapter }
	if _, err := InspectRecovery(t.Context(), f.db, 1, ItemSelection{ItemType: "plugin", ItemName: "archive"}, wrap); !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("invalid selection: %v", err)
	}

	manifest := db.RestorePoint{Metadata: `{"item_manifests":{"archive":"` + strings.Repeat("ab", 32) + `"}}`}
	probe := &evidenceProbe{adapter: &fldcovMetadata{}, remaining: 8}
	for _, item := range []ItemSelection{{ItemType: "vm", ItemName: "archive"}, {ItemType: "container", ItemName: "archive"}} {
		if status := probe.inspect(t.Context(), f.db, f.dest.ID, manifest, item, false); status != Broken {
			t.Fatalf("classic %s replay over a dedup manifest: %v", item.ItemType, status)
		}
	}

	for range cap(recoveryProbeSlots) {
		recoveryProbeSlots <- struct{}{}
	}
	defer func() {
		for range cap(recoveryProbeSlots) {
			<-recoveryProbeSlots
		}
	}()
	steps := []RecoveryChainStep{{Artifacts: Unknown, Membership: Confirmed}}
	if got := probeRecoveryArtifacts(t.Context(), f.db, f.job.ID, []db.RestorePoint{{}}, steps, ItemSelection{ItemType: "folder", ItemName: "archive"}, false, wrap); got != nil {
		t.Fatalf("saturated probe slots still probed storage: %+v", got)
	}
}
