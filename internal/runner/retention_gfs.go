package runner

import (
	"fmt"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// GFSPolicy describes the per-job grandfather-father-son retention buckets.
// Each field is the number of restore points to keep in that bucket; 0 means
// the bucket is disabled. A single restore point can fill multiple buckets
// at once (yesterday's only backup is the latest AND the daily AND the
// weekly slot).
type GFSPolicy struct {
	KeepLatest  int
	KeepDaily   int
	KeepWeekly  int
	KeepMonthly int
	KeepYearly  int
}

// FromJob extracts the GFS policy from a job row.
func gfsPolicyFromJob(job db.Job) GFSPolicy {
	return GFSPolicy{
		KeepLatest:  job.KeepLatest,
		KeepDaily:   job.KeepDaily,
		KeepWeekly:  job.KeepWeekly,
		KeepMonthly: job.KeepMonthly,
		KeepYearly:  job.KeepYearly,
	}
}

// IsActive reports whether at least one GFS bucket is enabled. When false
// the runner falls back to the legacy retention_count / retention_days path.
func (p GFSPolicy) IsActive() bool {
	return p.KeepLatest > 0 ||
		p.KeepDaily > 0 ||
		p.KeepWeekly > 0 ||
		p.KeepMonthly > 0 ||
		p.KeepYearly > 0
}

// gfsDirectlyKept walks restore points newest-first and returns the IDs of
// the points that are directly kept by the GFS policy. Chain-protection of
// parent incrementals is applied by the caller via protectedRestorePointIDs.
//
// loc is the timezone used to bucket by day/week/month/year. The server's
// local timezone is the right choice for users who think "yesterday at 3 am"
// in their own wall clock — UTC would split midnights across days for many.
func gfsDirectlyKept(points []db.RestorePoint, policy GFSPolicy, loc *time.Location) map[int64]struct{} {
	kept := make(map[int64]struct{}, len(points))
	if !policy.IsActive() || len(points) == 0 {
		return kept
	}
	if loc == nil {
		loc = time.Local
	}

	var (
		latest  int
		daily   int
		weekly  int
		monthly int
		yearly  int
	)
	seenDay := make(map[string]struct{})
	seenWeek := make(map[string]struct{})
	seenMonth := make(map[string]struct{})
	seenYear := make(map[string]struct{})

	for _, rp := range points {
		t := rp.CreatedAt.In(loc)
		dayKey := t.Format("2006-01-02")
		isoY, isoW := t.ISOWeek()
		weekKey := timeWeekKey(isoY, isoW)
		monthKey := t.Format("2006-01")
		yearKey := t.Format("2006")

		keep := false

		if latest < policy.KeepLatest {
			keep = true
			latest++
		}
		if policy.KeepDaily > 0 {
			if _, seen := seenDay[dayKey]; !seen && daily < policy.KeepDaily {
				keep = true
				daily++
				seenDay[dayKey] = struct{}{}
			}
		}
		if policy.KeepWeekly > 0 {
			if _, seen := seenWeek[weekKey]; !seen && weekly < policy.KeepWeekly {
				keep = true
				weekly++
				seenWeek[weekKey] = struct{}{}
			}
		}
		if policy.KeepMonthly > 0 {
			if _, seen := seenMonth[monthKey]; !seen && monthly < policy.KeepMonthly {
				keep = true
				monthly++
				seenMonth[monthKey] = struct{}{}
			}
		}
		if policy.KeepYearly > 0 {
			if _, seen := seenYear[yearKey]; !seen && yearly < policy.KeepYearly {
				keep = true
				yearly++
				seenYear[yearKey] = struct{}{}
			}
		}

		if keep {
			kept[rp.ID] = struct{}{}
		}
	}

	return kept
}

// gfsProtectedRestorePointIDs returns the union of directly-kept points and
// any ancestor parents required to keep chained incrementals/differentials
// restorable. Mirrors the shape of protectedRestorePointIDs but uses the
// GFS classifier instead of keepCount/keepDays.
func gfsProtectedRestorePointIDs(all []db.RestorePoint, policy GFSPolicy, loc *time.Location) map[int64]struct{} {
	protected := make(map[int64]struct{}, len(all))
	if len(all) == 0 || !policy.IsActive() {
		return protected
	}

	direct := gfsDirectlyKept(all, policy, loc)

	byID := make(map[int64]db.RestorePoint, len(all))
	for _, rp := range all {
		byID[rp.ID] = rp
	}

	for id := range direct {
		current, ok := byID[id]
		if !ok {
			continue
		}
		for {
			if _, already := protected[current.ID]; already {
				break
			}
			protected[current.ID] = struct{}{}
			if current.ParentRestorePointID <= 0 {
				break
			}
			parent, ok := byID[current.ParentRestorePointID]
			if !ok {
				break
			}
			current = parent
		}
	}

	return protected
}

// timeWeekKey formats an ISO year/week pair as "2026-W08".
func timeWeekKey(year, week int) string {
	return fmt.Sprintf("%04d-W%02d", year, week)
}
