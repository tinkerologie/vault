package runner

import (
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestAnnotateRestorePointsPublicWrapper exercises the public entry point
// that uses time.Now() internally. The result must mirror the package-
// private version when called with a comparable now.
func TestAnnotateRestorePointsPublicWrapper(t *testing.T) {
	t.Parallel()
	job := db.Job{Name: "wrap", RetentionCount: 1}
	now := time.Now()
	points := []db.RestorePoint{
		{ID: 1, BackupType: "full", CreatedAt: now.Add(-time.Hour)},
		{ID: 2, BackupType: "incremental", ParentRestorePointID: 1, CreatedAt: now},
	}
	annotated := AnnotateRestorePoints(job, points)
	if len(annotated) != 2 {
		t.Fatalf("len=%d, want 2", len(annotated))
	}
	byID := map[int64]AnnotatedRestorePoint{}
	for _, a := range annotated {
		byID[a.ID] = a
	}
	if byID[2].ChainStatus != "healthy" {
		t.Errorf("rp 2 ChainStatus = %q, want healthy", byID[2].ChainStatus)
	}
	if byID[1].ChainStatus != "standalone" {
		t.Errorf("rp 1 ChainStatus = %q, want standalone", byID[1].ChainStatus)
	}
}

// TestRestorePointChainStateLoop exercises the loop-detection branch in
// restorePointChainState — a parent pointer that cycles back to a seen ID
// must surface as "broken" with a loop warning.
func TestRestorePointChainStateLoop(t *testing.T) {
	t.Parallel()
	// Build a cycle: 10 -> 11 -> 10
	rp10 := db.RestorePoint{ID: 10, ParentRestorePointID: 11}
	rp11 := db.RestorePoint{ID: 11, ParentRestorePointID: 10}
	byID := map[int64]db.RestorePoint{10: rp10, 11: rp11}

	status, _, missingParent, warning := restorePointChainState(rp10, byID)
	if status != "broken" {
		t.Errorf("status = %q, want broken on loop", status)
	}
	if missingParent != 10 {
		t.Errorf("missingParent = %d, want 10 (cycle hit-back)", missingParent)
	}
	if warning == "" {
		t.Error("expected warning text on loop")
	}
}

// TestDirectlyKeptRestorePointIDsKeepDaysOnly drives the keepDays branch
// (no keepCount filter, so all points within the cutoff are kept).
func TestDirectlyKeptRestorePointIDsKeepDaysOnly(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	points := []db.RestorePoint{
		{ID: 1, CreatedAt: now.Add(-72 * time.Hour)},  // 3 days ago — kept (within 7 days)
		{ID: 2, CreatedAt: now.Add(-24 * time.Hour)},  // yesterday — kept
		{ID: 3, CreatedAt: now.Add(-240 * time.Hour)}, // 10 days ago — dropped
	}
	got := directlyKeptRestorePointIDs(points, 0, 7, now)
	if _, ok := got[1]; !ok {
		t.Errorf("ID 1 should be kept (3 days < 7)")
	}
	if _, ok := got[2]; !ok {
		t.Errorf("ID 2 should be kept (1 day < 7)")
	}
	if _, ok := got[3]; ok {
		t.Errorf("ID 3 should be dropped (10 days > 7)")
	}
}

// TestRetainedDependencyCountsNoDirectKeeps verifies that with an empty
// directKeep set, no dependency counts are recorded.
func TestRetainedDependencyCountsNoDirectKeeps(t *testing.T) {
	t.Parallel()
	now := time.Now()
	points := []db.RestorePoint{
		{ID: 1, CreatedAt: now.Add(-2 * time.Hour)},
		{ID: 2, ParentRestorePointID: 1, CreatedAt: now.Add(-time.Hour)},
	}
	counts := retainedDependencyCounts(points, map[int64]struct{}{})
	if len(counts) != 0 {
		t.Errorf("got %d counts, want 0", len(counts))
	}
}

// TestSortRestorePointsNewestStableOrderingByID verifies the equal-time
// tiebreaker prefers the higher ID first.
func TestSortRestorePointsNewestStableOrderingByID(t *testing.T) {
	t.Parallel()
	now := time.Now()
	points := []db.RestorePoint{
		{ID: 1, CreatedAt: now},
		{ID: 3, CreatedAt: now},
		{ID: 2, CreatedAt: now},
	}
	sorted := sortRestorePointsNewest(points)
	want := []int64{3, 2, 1}
	for i, w := range want {
		if sorted[i].ID != w {
			t.Errorf("sorted[%d].ID = %d, want %d", i, sorted[i].ID, w)
		}
	}
}

// TestAnnotateRestorePointsLTRPath exercises the LTR-active branch of
// annotateRestorePoints.
func TestAnnotateRestorePointsLTRPath(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	job := db.Job{
		Name:       "ltr-job",
		KeepLatest: 1, KeepDaily: 1,
	}
	points := []db.RestorePoint{
		{ID: 1, BackupType: "full", CreatedAt: now.Add(-48 * time.Hour)},
		{ID: 2, BackupType: "incremental", ParentRestorePointID: 1, CreatedAt: now.Add(-24 * time.Hour)},
		{ID: 3, BackupType: "incremental", ParentRestorePointID: 2, CreatedAt: now},
	}
	annotated := annotateRestorePoints(job, points, now)
	if len(annotated) != 3 {
		t.Fatalf("got %d, want 3", len(annotated))
	}
	byID := map[int64]AnnotatedRestorePoint{}
	for _, a := range annotated {
		byID[a.ID] = a
	}
	// Newest (id=3) is directly kept by latest+daily.
	// Ancestors 2 and 1 should be marked retention-preserved.
	if !byID[1].RetentionPreserved {
		t.Errorf("id=1 should be retention-preserved")
	}
	if !byID[2].RetentionPreserved {
		t.Errorf("id=2 should be retention-preserved")
	}
	if byID[3].RetentionPreserved {
		t.Errorf("id=3 is directly kept; RetentionPreserved should be false")
	}
}
