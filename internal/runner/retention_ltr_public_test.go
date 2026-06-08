package runner

import (
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestLTRDirectlyKeptPublic exercises the exported wrapper used by the
// retention-preview endpoint.
func TestLTRDirectlyKeptPublic(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	end := time.Date(2026, 5, 18, 12, 0, 0, 0, loc)
	points := makeDailyPoints(t, end, 10)
	kept := LTRDirectlyKept(points, LTRPolicy{KeepLatest: 3}, loc)
	if len(kept) != 3 {
		t.Errorf("len(kept)=%d, want 3", len(kept))
	}
}

// TestLTRDirectlyKeptPublicNilLoc verifies the nil-location fallback (uses
// time.Local). The exported wrapper must not panic and must return the
// same shape as the private function.
func TestLTRDirectlyKeptPublicNilLoc(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	end := time.Date(2026, 5, 18, 12, 0, 0, 0, loc)
	points := makeDailyPoints(t, end, 5)
	kept := LTRDirectlyKept(points, LTRPolicy{KeepLatest: 2}, nil)
	if len(kept) != 2 {
		t.Errorf("len(kept)=%d, want 2", len(kept))
	}
}

// TestLTRProtectedRestorePointIDsPublic exercises the exported wrapper.
func TestLTRProtectedRestorePointIDsPublic(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	end := time.Date(2026, 5, 18, 12, 0, 0, 0, loc)
	points := []db.RestorePoint{
		{ID: 3, JobID: 1, CreatedAt: end, ParentRestorePointID: 2, BackupType: "incremental"},
		{ID: 2, JobID: 1, CreatedAt: end.Add(-24 * time.Hour), ParentRestorePointID: 1, BackupType: "incremental"},
		{ID: 1, JobID: 1, CreatedAt: end.Add(-48 * time.Hour), ParentRestorePointID: 0, BackupType: "full"},
	}
	protected := LTRProtectedRestorePointIDs(points, LTRPolicy{KeepLatest: 1}, loc)
	for _, id := range []int64{1, 2, 3} {
		if _, ok := protected[id]; !ok {
			t.Errorf("ID %d missing from protected set", id)
		}
	}
}

// TestLTRProtectedRestorePointIDsEmpty verifies the early-return on empty
// or disabled policy.
func TestLTRProtectedRestorePointIDsEmpty(t *testing.T) {
	t.Parallel()
	if got := LTRProtectedRestorePointIDs(nil, LTRPolicy{KeepLatest: 1}, time.UTC); len(got) != 0 {
		t.Errorf("nil points should yield empty result, got %d", len(got))
	}
	now := time.Now()
	points := []db.RestorePoint{{ID: 1, CreatedAt: now}}
	if got := LTRProtectedRestorePointIDs(points, LTRPolicy{}, time.UTC); len(got) != 0 {
		t.Errorf("disabled policy should yield empty result, got %d", len(got))
	}
}
