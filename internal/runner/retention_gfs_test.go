package runner

import (
	"sort"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// makeDailyPoints fabricates one restore point per day for `days` consecutive
// days, ending on `end` (inclusive). IDs are assigned newest-first (highest ID
// = newest), matching what the real DB would produce.
func makeDailyPoints(t *testing.T, end time.Time, days int) []db.RestorePoint {
	t.Helper()
	loc := end.Location()
	out := make([]db.RestorePoint, 0, days)
	for i := 0; i < days; i++ {
		day := time.Date(end.Year(), end.Month(), end.Day()-i, 12, 0, 0, 0, loc)
		out = append(out, db.RestorePoint{
			ID:        int64(days - i), // newest gets highest ID
			JobID:     1,
			CreatedAt: day,
		})
	}
	// Sort newest-first (highest ID first) the same way chain_health does.
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func keptIDSet(t *testing.T, points []db.RestorePoint, policy GFSPolicy, loc *time.Location) map[int64]struct{} {
	t.Helper()
	return gfsDirectlyKept(points, policy, loc)
}

func TestGFSPolicyIsActive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		p    GFSPolicy
		want bool
	}{
		{"all zero", GFSPolicy{}, false},
		{"only latest", GFSPolicy{KeepLatest: 1}, true},
		{"only daily", GFSPolicy{KeepDaily: 7}, true},
		{"only weekly", GFSPolicy{KeepWeekly: 4}, true},
		{"only monthly", GFSPolicy{KeepMonthly: 12}, true},
		{"only yearly", GFSPolicy{KeepYearly: 5}, true},
		{"mixed", GFSPolicy{KeepLatest: 3, KeepWeekly: 4, KeepYearly: 5}, true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.p.IsActive(); got != c.want {
				t.Errorf("IsActive() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestGFSDirectlyKept_KeepLatestOnly(t *testing.T) {
	loc := time.UTC
	end := time.Date(2026, 5, 18, 12, 0, 0, 0, loc)
	points := makeDailyPoints(t, end, 30)

	kept := keptIDSet(t, points, GFSPolicy{KeepLatest: 5}, loc)
	if len(kept) != 5 {
		t.Fatalf("expected 5 kept, got %d", len(kept))
	}
	// Newest 5 (IDs 30..26) should all be kept.
	for id := int64(26); id <= 30; id++ {
		if _, ok := kept[id]; !ok {
			t.Errorf("expected ID %d kept, missing", id)
		}
	}
}

func TestGFSDirectlyKept_PureDaily(t *testing.T) {
	loc := time.UTC
	end := time.Date(2026, 5, 18, 12, 0, 0, 0, loc)
	points := makeDailyPoints(t, end, 30)

	kept := keptIDSet(t, points, GFSPolicy{KeepDaily: 7}, loc)
	if len(kept) != 7 {
		t.Fatalf("expected 7 daily kept, got %d", len(kept))
	}
}

func TestGFSDirectlyKept_PureWeekly(t *testing.T) {
	loc := time.UTC
	// 8 weeks of daily backups → 8 distinct ISO weeks. KeepWeekly=4 should
	// keep the most-recent restore-point in each of the 4 most-recent weeks.
	end := time.Date(2026, 5, 18, 12, 0, 0, 0, loc) // Mon, ISO week 21
	points := makeDailyPoints(t, end, 8*7)

	kept := keptIDSet(t, points, GFSPolicy{KeepWeekly: 4}, loc)
	if len(kept) != 4 {
		t.Fatalf("expected 4 weekly kept, got %d (ids=%v)", len(kept), sortedIDs(kept))
	}
}

func TestGFSDirectlyKept_PureMonthly(t *testing.T) {
	loc := time.UTC
	// 6 months of daily backups → expect 6 unique month buckets. KeepMonthly=3
	// keeps the newest 3.
	end := time.Date(2026, 6, 30, 12, 0, 0, 0, loc)
	points := makeDailyPoints(t, end, 6*30)

	kept := keptIDSet(t, points, GFSPolicy{KeepMonthly: 3}, loc)
	if len(kept) != 3 {
		t.Fatalf("expected 3 monthly kept, got %d (ids=%v)", len(kept), sortedIDs(kept))
	}
}

func TestGFSDirectlyKept_PureYearly(t *testing.T) {
	loc := time.UTC
	// 3 years of monthly backups (36 points). KeepYearly=2 → newest 2 years.
	out := []db.RestorePoint{}
	id := int64(0)
	for y := 2024; y <= 2026; y++ {
		for m := 1; m <= 12; m++ {
			id++
			out = append(out, db.RestorePoint{
				ID:        id,
				JobID:     1,
				CreatedAt: time.Date(y, time.Month(m), 15, 12, 0, 0, 0, loc),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })

	kept := keptIDSet(t, out, GFSPolicy{KeepYearly: 2}, loc)
	if len(kept) != 2 {
		t.Fatalf("expected 2 yearly kept, got %d (ids=%v)", len(kept), sortedIDs(kept))
	}
}

func TestGFSDirectlyKept_MixedPolicy(t *testing.T) {
	loc := time.UTC
	// 18 months of daily backups, ~547 points.
	end := time.Date(2026, 5, 18, 12, 0, 0, 0, loc)
	points := makeDailyPoints(t, end, 18*30)

	// Realistic policy: 3 latest, 7 daily, 4 weekly, 12 monthly, 1 yearly.
	policy := GFSPolicy{KeepLatest: 3, KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 12, KeepYearly: 1}
	kept := keptIDSet(t, points, policy, loc)

	// Upper bound: sum of all buckets if every bucket filled with distinct points.
	maxKept := policy.KeepLatest + policy.KeepDaily + policy.KeepWeekly + policy.KeepMonthly + policy.KeepYearly
	if len(kept) > maxKept {
		t.Fatalf("kept %d exceeds policy sum %d", len(kept), maxKept)
	}

	// Lower bound: at least 12 (monthly is the largest bucket with no overlap pressure here).
	if len(kept) < 12 {
		t.Fatalf("kept %d is suspiciously low (expected >=12 for monthly bucket)", len(kept))
	}

	// All five newest points must be kept (covered by latest/daily/weekly/monthly).
	for i := 0; i < 5; i++ {
		id := points[i].ID
		if _, ok := kept[id]; !ok {
			t.Errorf("expected newest point ID %d (idx %d) to be kept", id, i)
		}
	}
}

func TestGFSDirectlyKept_SinglePointCoversAllBuckets(t *testing.T) {
	loc := time.UTC
	one := []db.RestorePoint{
		{ID: 1, JobID: 1, CreatedAt: time.Date(2026, 5, 18, 12, 0, 0, 0, loc)},
	}
	kept := keptIDSet(t, one, GFSPolicy{KeepLatest: 1, KeepDaily: 1, KeepWeekly: 1, KeepMonthly: 1, KeepYearly: 1}, loc)
	if len(kept) != 1 {
		t.Fatalf("expected single point kept once, got %d", len(kept))
	}
	if _, ok := kept[1]; !ok {
		t.Errorf("expected ID 1 kept")
	}
}

func TestGFSDirectlyKept_EmptyInput(t *testing.T) {
	kept := keptIDSet(t, nil, GFSPolicy{KeepDaily: 7}, time.UTC)
	if len(kept) != 0 {
		t.Errorf("expected empty result on nil input, got %d", len(kept))
	}
}

func TestGFSDirectlyKept_DisabledPolicyKeepsNothing(t *testing.T) {
	loc := time.UTC
	end := time.Date(2026, 5, 18, 12, 0, 0, 0, loc)
	points := makeDailyPoints(t, end, 10)
	kept := keptIDSet(t, points, GFSPolicy{}, loc)
	if len(kept) != 0 {
		t.Errorf("expected empty result with disabled policy, got %d", len(kept))
	}
}

func TestGFSProtectedRestorePointIDs_ChainAncestors(t *testing.T) {
	loc := time.UTC
	end := time.Date(2026, 5, 18, 12, 0, 0, 0, loc)
	// 3 points forming a chain: full (oldest) ← incremental ← incremental
	points := []db.RestorePoint{
		{ID: 3, JobID: 1, CreatedAt: end, ParentRestorePointID: 2, BackupType: "incremental"},
		{ID: 2, JobID: 1, CreatedAt: end.Add(-24 * time.Hour), ParentRestorePointID: 1, BackupType: "incremental"},
		{ID: 1, JobID: 1, CreatedAt: end.Add(-48 * time.Hour), ParentRestorePointID: 0, BackupType: "full"},
	}

	// Keep just 1 latest → directly kept is {3}, but {1, 2} must be protected as ancestors.
	protected := gfsProtectedRestorePointIDs(points, GFSPolicy{KeepLatest: 1}, loc)
	for _, id := range []int64{1, 2, 3} {
		if _, ok := protected[id]; !ok {
			t.Errorf("expected ID %d in protected set (chain ancestor), got %v", id, sortedIDs(protected))
		}
	}
}

func sortedIDs(m map[int64]struct{}) []int64 {
	out := make([]int64, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
