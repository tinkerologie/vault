package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestBucketDurationTrend(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 6, 18, 2, 0, 0, 0, time.UTC)
	runs := []trendRun{
		{Start: base, DurationSeconds: 60},
		{Start: base.Add(1 * time.Hour), DurationSeconds: 120},
		{Start: base.AddDate(0, 0, -1), DurationSeconds: 30},
		{Start: base.Add(2 * time.Hour), DurationSeconds: 0}, // zero-duration import: skipped
	}

	cases := []struct {
		name    string
		runs    []trendRun
		bucket  string
		wantLen int
		check   func(t *testing.T, buckets []trendBucket)
	}{
		{
			name:    "day buckets average and count runs",
			runs:    runs,
			bucket:  "day",
			wantLen: 2,
			check: func(t *testing.T, buckets []trendBucket) {
				last := buckets[len(buckets)-1] // same-day bucket holds 60s + 120s
				if last.RunCount != 2 || last.AvgDurationSeconds != 90 {
					t.Errorf("same-day bucket = %+v, want run_count 2 / avg 90", last)
				}
				if first := buckets[0]; first.RunCount != 1 || first.AvgDurationSeconds != 30 {
					t.Errorf("yesterday bucket = %+v, want run_count 1 / avg 30", first)
				}
			},
		},
		{
			name:    "run buckets skip zero-duration runs",
			runs:    runs,
			bucket:  "run",
			wantLen: 3,
		},
		{
			name:    "empty input yields no buckets",
			runs:    nil,
			bucket:  "week",
			wantLen: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bucketDurationTrend(tc.runs, tc.bucket)
			if len(got) != tc.wantLen {
				t.Fatalf("buckets = %d, want %d", len(got), tc.wantLen)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}

func TestTrendInvalidMetric(t *testing.T) {
	cases := []struct {
		name   string
		metric string
		want   int
	}{
		{name: "rejects unknown metric", metric: "bogus", want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDB(t)
			h := NewHistoryHandler(d)
			w := httptest.NewRecorder()
			h.Trend(w, newReq(http.MethodGet, "/api/v1/history/trend?metric="+tc.metric, nil))
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestTrendMetricSizeDefault(t *testing.T) {
	cases := []struct {
		name   string
		metric string // empty => default
		want   string
	}{
		{name: "omitted metric defaults to size", metric: "", want: "size"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDB(t)
			destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "trend-size", Type: "local", Config: `{}`})
			if err != nil {
				t.Fatal(err)
			}
			jobID, err := d.CreateJob(db.Job{Name: "trend-size-job", StorageDestID: destID})
			if err != nil {
				t.Fatal(err)
			}
			runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.Exec(`UPDATE job_runs SET size_bytes = 2048 WHERE id = ?`, runID); err != nil {
				t.Fatal(err)
			}

			url := "/api/v1/history/trend?period=7d"
			if tc.metric != "" {
				url += "&metric=" + tc.metric
			}
			h := NewHistoryHandler(d)
			w := httptest.NewRecorder()
			h.Trend(w, newReq(http.MethodGet, url, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
			}

			var body struct {
				Metric string `json:"metric"`
				Points []struct {
					TotalBytes int64            `json:"total_bytes"`
					Categories map[string]int64 `json:"categories"`
				} `json:"points"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Metric != tc.want {
				t.Fatalf("metric = %q, want %s", body.Metric, tc.want)
			}
			if len(body.Points) != 1 {
				t.Fatalf("points = %d, want 1", len(body.Points))
			}
			if body.Points[0].TotalBytes != 2048 {
				t.Errorf("total_bytes = %d, want 2048", body.Points[0].TotalBytes)
			}
			if body.Points[0].Categories["other"] != 2048 {
				t.Errorf("categories[other] = %d, want 2048", body.Points[0].Categories["other"])
			}
		})
	}
}

func TestTrendMetricDuration(t *testing.T) {
	cases := []struct {
		name   string
		metric string
		want   string
	}{
		{name: "duration metric returns averaged buckets", metric: "duration", want: "duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDB(t)
			destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "trend-dur", Type: "local", Config: `{}`})
			if err != nil {
				t.Fatal(err)
			}
			jobID, err := d.CreateJob(db.Job{Name: "trend-dur-job", StorageDestID: destID})
			if err != nil {
				t.Fatal(err)
			}
			seed := func(hoursAgo, durSec int) {
				t.Helper()
				runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"})
				if err != nil {
					t.Fatalf("create run: %v", err)
				}
				start := time.Now().Add(-time.Duration(hoursAgo) * time.Hour).UTC()
				end := start.Add(time.Duration(durSec) * time.Second)
				if _, err := d.Exec(`UPDATE job_runs SET started_at = ?, completed_at = ?, size_bytes = 100 WHERE id = ?`,
					start.Format("2006-01-02 15:04:05"), end.Format("2006-01-02 15:04:05"), runID); err != nil {
					t.Fatalf("seed run: %v", err)
				}
			}
			seed(1, 60)
			seed(2, 120)

			h := NewHistoryHandler(d)
			w := httptest.NewRecorder()
			h.Trend(w, newReq(http.MethodGet, "/api/v1/history/trend?period=7d&metric="+tc.metric, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
			}

			var body struct {
				Metric string `json:"metric"`
				Bucket string `json:"bucket"`
				Points []struct {
					Start              string  `json:"start"`
					AvgDurationSeconds float64 `json:"avg_duration_seconds"`
					RunCount           int     `json:"run_count"`
					TotalBytes         int64   `json:"total_bytes"`
				} `json:"points"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Metric != tc.want {
				t.Fatalf("metric = %q, want %s", body.Metric, tc.want)
			}
			if len(body.Points) != 2 {
				t.Fatalf("points = %d, want 2", len(body.Points))
			}
			for _, p := range body.Points {
				if p.RunCount != 1 {
					t.Errorf("run_count = %d, want 1", p.RunCount)
				}
				if p.TotalBytes != 0 {
					t.Errorf("duration bucket should not carry total_bytes, got %d", p.TotalBytes)
				}
				if p.AvgDurationSeconds != 60 && p.AvgDurationSeconds != 120 {
					t.Errorf("avg_duration_seconds = %f, want 60 or 120", p.AvgDurationSeconds)
				}
			}
		})
	}
}

func TestTrendInvalidPeriod(t *testing.T) {
	cases := []struct {
		name   string
		period string
		want   int
	}{
		{name: "rejects unknown period", period: "bogus", want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDB(t)
			h := NewHistoryHandler(d)
			w := httptest.NewRecorder()
			h.Trend(w, newReq(http.MethodGet, "/api/v1/history/trend?period="+tc.period, nil))
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// seedTrendRun inserts a run for jobID and pins its status, size, start and
// completion so the trend handler sees deterministic input. A non-positive
// duration leaves completed_at NULL (an imported/zero-duration run).
func seedTrendRun(t *testing.T, d *db.DB, jobID int64, status string, sizeBytes int64, durSec int) {
	t.Helper()
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: status, BackupType: "full"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	start := time.Now().Add(-1 * time.Hour).UTC()
	startStr := start.Format("2006-01-02 15:04:05")
	if durSec > 0 {
		end := start.Add(time.Duration(durSec) * time.Second).Format("2006-01-02 15:04:05")
		if _, err := d.Exec(`UPDATE job_runs SET status = ?, started_at = ?, completed_at = ?, size_bytes = ? WHERE id = ?`,
			status, startStr, end, sizeBytes, runID); err != nil {
			t.Fatalf("seed run: %v", err)
		}
		return
	}
	if _, err := d.Exec(`UPDATE job_runs SET status = ?, started_at = ?, completed_at = NULL, size_bytes = ? WHERE id = ?`,
		status, startStr, sizeBytes, runID); err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

// TestTrendExcludesIneligibleRuns pins the handler's filtering of runs that
// must not shape a trend: non-success statuses, zero-size runs on the size
// metric, and zero-duration runs on the duration metric.
func TestTrendExcludesIneligibleRuns(t *testing.T) {
	cases := []struct {
		name       string
		metric     string
		seed       func(t *testing.T, d *db.DB, jobID int64)
		wantPoints int
	}{
		{
			name:   "size metric skips non-success runs",
			metric: "size",
			seed: func(t *testing.T, d *db.DB, jobID int64) {
				seedTrendRun(t, d, jobID, "failed", 2048, 60)
				seedTrendRun(t, d, jobID, "success", 2048, 60)
			},
			wantPoints: 1,
		},
		{
			name:   "size metric skips zero-size runs",
			metric: "size",
			seed: func(t *testing.T, d *db.DB, jobID int64) {
				seedTrendRun(t, d, jobID, "success", 0, 60)
				seedTrendRun(t, d, jobID, "success", 2048, 60)
			},
			wantPoints: 1,
		},
		{
			name:   "duration metric skips zero-duration runs",
			metric: "duration",
			seed: func(t *testing.T, d *db.DB, jobID int64) {
				seedTrendRun(t, d, jobID, "success", 2048, 0)
				seedTrendRun(t, d, jobID, "success", 2048, 60)
			},
			wantPoints: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDB(t)
			destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "trend-filter-" + tc.name, Type: "local", Config: `{}`})
			if err != nil {
				t.Fatal(err)
			}
			jobID, err := d.CreateJob(db.Job{Name: "trend-filter-job-" + tc.name, StorageDestID: destID})
			if err != nil {
				t.Fatal(err)
			}
			tc.seed(t, d, jobID)

			h := NewHistoryHandler(d)
			w := httptest.NewRecorder()
			h.Trend(w, newReq(http.MethodGet, "/api/v1/history/trend?period=7d&metric="+tc.metric, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
			}
			var body struct {
				Points []json.RawMessage `json:"points"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Points) != tc.wantPoints {
				t.Fatalf("points = %d, want %d", len(body.Points), tc.wantPoints)
			}
		})
	}
}
