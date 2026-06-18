package handlers

import (
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

func TestBucketTrend(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 6, 18, 2, 0, 0, 0, time.UTC)
	runs := []trendRun{
		{Start: base, Size: 100, Category: "containers"},
		{Start: base.Add(1 * time.Hour), Size: 50, Category: "vms"},
		{Start: base.AddDate(0, 0, -1), Size: 200, Category: "containers"},
	}

	day := bucketTrend(runs, "day")
	if len(day) != 2 {
		t.Fatalf("day buckets = %d, want 2", len(day))
	}
	last := day[len(day)-1]
	if last.Categories["containers"] != 100 || last.Categories["vms"] != 50 || last.TotalBytes != 150 {
		t.Errorf("day bucket sums wrong: %+v", last)
	}

	perRun := bucketTrend(runs, "run")
	if len(perRun) != 3 {
		t.Errorf("run buckets = %d, want 3", len(perRun))
	}

	if got := bucketTrend(nil, "week"); len(got) != 0 {
		t.Errorf("empty input buckets = %d, want 0", len(got))
	}
}

func TestPeriodToWindow(t *testing.T) {
	t.Parallel()
	cases := map[string]string{"7d": "run", "30d": "day", "90d": "day", "6m": "week", "1y": "week"}
	for period, wantBucket := range cases {
		_, bucket, ok := periodToWindow(period)
		if !ok || bucket != wantBucket {
			t.Errorf("period %q -> bucket %q ok=%v, want %q", period, bucket, ok, wantBucket)
		}
	}
	if _, _, ok := periodToWindow("bogus"); ok {
		t.Errorf("bogus period should be rejected")
	}
}

// TestDominantCategory covers the empty-items edge case (must be "other", not
// "containers") plus basic classification and tie-break order.
func TestDominantCategory(t *testing.T) {
	t.Parallel()
	if got := dominantCategory(nil); got != "other" {
		t.Errorf("empty items = %q, want other", got)
	}
	mk := func(types ...string) []db.JobItem {
		out := make([]db.JobItem, 0, len(types))
		for _, ty := range types {
			out = append(out, db.JobItem{ItemType: ty})
		}
		return out
	}
	if got := dominantCategory(mk("vm", "vm", "container")); got != "vms" {
		t.Errorf("vm-dominant = %q, want vms", got)
	}
	if got := dominantCategory(mk("container")); got != "containers" {
		t.Errorf("single container = %q, want containers", got)
	}
}
