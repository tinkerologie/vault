package anomaly

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// buildSizeEC constructs a minimal EvalContext for SizeDriftDetector tests.
func buildSizeEC(observed float64, bMedian, bMAD float64, sampleCount int, sensitivity string) EvalContext {
	run := &db.JobRun{
		ID:        42,
		JobID:     7,
		SizeBytes: int64(observed),
	}
	job := &db.Job{ID: 7}
	var baseline *db.JobBaseline
	if sampleCount > 0 || bMedian != 0 {
		baseline = &db.JobBaseline{
			JobID:       7,
			SampleCount: sampleCount,
			BytesMedian: bMedian,
			BytesMAD:    bMAD,
		}
	}
	return EvalContext{
		JobRun:            run,
		Job:               job,
		Baseline:          baseline,
		GlobalSensitivity: sensitivity,
		Clock:             NewFakeClock(time.Now()),
	}
}

func TestSizeDrift(t *testing.T) {
	det := NewSizeDriftDetector()

	tests := []struct {
		name        string
		observed    float64
		bMedian     float64
		bMAD        float64
		sampleCount int
		sensitivity string
		wantNil     bool
		wantSev     Severity
		wantMetric  string
	}{
		{
			name:        "cold_start_below_10_samples",
			observed:    1000,
			bMedian:     100,
			bMAD:        10,
			sampleCount: 9, // < 10 → cold start
			sensitivity: "balanced",
			wantNil:     true,
		},
		{
			name:        "normal_within_band",
			observed:    105,
			bMedian:     100,
			bMAD:        5,
			sampleCount: 10,
			sensitivity: "balanced",
			// z = 0.6745 * (105-100)/5 = 0.6745 → < k=3.5 → nil
			wantNil: true,
		},
		{
			name:        "warning_z_score",
			observed:    130,
			bMedian:     100,
			bMAD:        5,
			sampleCount: 10,
			sensitivity: "balanced",
			// z = 0.6745 * 30/5 ≈ 4.047 > k=3.5, < 2k=7 → warning
			wantNil:    false,
			wantSev:    SeverityWarning,
			wantMetric: "total_bytes",
		},
		{
			name:        "critical_z_score",
			observed:    200,
			bMedian:     100,
			bMAD:        5,
			sampleCount: 10,
			sensitivity: "balanced",
			// z = 0.6745 * 100/5 ≈ 13.49 ≥ 2k=7 → critical
			wantNil:    false,
			wantSev:    SeverityCritical,
			wantMetric: "total_bytes",
		},
		{
			name:        "gap_region_no_signal",
			observed:    118,
			bMedian:     100,
			bMAD:        5,
			sampleCount: 10,
			sensitivity: "balanced",
			// z = 0.6745 * 18/5 ≈ 2.43 < k=3.5 (Rule A no-fire) and
			// 118 < 5*100 (Rule B no-fire). The linear band (median+k*mad=117.5)
			// is NOT a trigger, so the result must be nil.
			wantNil: true,
		},
		{
			name:        "median_multiplier_fires",
			observed:    600,
			bMedian:     100,
			bMAD:        5,
			sampleCount: 10,
			sensitivity: "balanced",
			// 600 > 5*100=500 → at least warning (Rule B).
			// Rule A also fires critical (z≈67.5 >> 2k=7), so the effective
			// severity is critical (≥ warning).  The spec says "at least
			// warning"; we assert the minimum here via a separate check below.
			wantNil:    false,
			wantSev:    SeverityCritical, // Rule A (z-score) takes priority
			wantMetric: "total_bytes",
		},
		{
			name:        "shrinkage_critical",
			observed:    40,
			bMedian:     100,
			bMAD:        5,
			sampleCount: 10,
			sensitivity: "balanced",
			// 40 < 0.5*100=50 → critical "total_bytes_low"
			wantNil:    false,
			wantSev:    SeverityCritical,
			wantMetric: "total_bytes_low",
		},
		{
			name:        "mad_zero_multiplier_fires",
			observed:    600,
			bMedian:     100,
			bMAD:        0,
			sampleCount: 10,
			sensitivity: "balanced",
			// mad=0 → Rule A disabled; 600 > 5*100=500 → Rule B warning
			wantNil:    false,
			wantSev:    SeverityWarning,
			wantMetric: "total_bytes",
		},
		{
			name:        "median_zero_no_signal",
			observed:    0,
			bMedian:     0,
			bMAD:        0,
			sampleCount: 10,
			sensitivity: "balanced",
			// median == 0 → return nil
			wantNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ec := buildSizeEC(tc.observed, tc.bMedian, tc.bMAD, tc.sampleCount, tc.sensitivity)
			got, err := det.Evaluate(ec)
			if err != nil {
				t.Fatalf("Evaluate() error: %v", err)
			}
			if tc.wantNil {
				if len(got) != 0 {
					t.Fatalf("want nil result, got %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("want 1 anomaly, got %d: %+v", len(got), got)
			}
			a := got[0]
			if a.Severity != tc.wantSev {
				t.Errorf("Severity: want %q, got %q", tc.wantSev, a.Severity)
			}
			if a.Metric != tc.wantMetric {
				t.Errorf("Metric: want %q, got %q", tc.wantMetric, a.Metric)
			}
			// Verify Details JSON contains the required keys.
			var d map[string]interface{}
			if err := json.Unmarshal([]byte(a.Details), &d); err != nil {
				t.Fatalf("Details not valid JSON: %v (got %q)", err, a.Details)
			}
			for _, key := range []string{"z_score", "growth_factor", "window_size"} {
				if _, ok := d[key]; !ok {
					t.Errorf("Details missing key %q, got: %v", key, a.Details)
				}
			}
		})
	}
}

// TestSizeDrift_ShrinkageMADZero verifies that when mad==0 and Rule C
// (shrinkage) fires, ModifiedZScore's ±Inf does NOT corrupt Details: z_score
// must be 0 (not Inf), and Details must be valid non-empty JSON containing
// growth_factor and window_size.
func TestSizeDrift_ShrinkageMADZero(t *testing.T) {
	det := NewSizeDriftDetector()

	// observed=40, median=100, mad=0 → 40 < 0.5*100=50 → critical shrinkage.
	ec := buildSizeEC(40, 100, 0, 10, "balanced")
	got, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("Evaluate() error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 anomaly, got %d: %+v", len(got), got)
	}
	a := got[0]
	if a.Severity != SeverityCritical {
		t.Errorf("Severity: want %q, got %q", SeverityCritical, a.Severity)
	}
	if a.Metric != "total_bytes_low" {
		t.Errorf("Metric: want %q, got %q", "total_bytes_low", a.Metric)
	}
	if a.Details == "" {
		t.Fatalf("Details must be non-empty JSON")
	}
	var d map[string]interface{}
	if err := json.Unmarshal([]byte(a.Details), &d); err != nil {
		t.Fatalf("Details not valid JSON: %v (got %q)", err, a.Details)
	}
	for _, key := range []string{"z_score", "growth_factor", "window_size"} {
		if _, ok := d[key]; !ok {
			t.Errorf("Details missing key %q, got: %v", key, a.Details)
		}
	}
	z, ok := d["z_score"].(float64)
	if !ok {
		t.Fatalf("z_score not a number: %v", d["z_score"])
	}
	if z != 0 {
		t.Errorf("z_score: want 0 (not Inf) when mad==0, got %v", z)
	}
}

// TestSizeDrift_FloorSuppression verifies that when a user has marked prior
// anomalies as "expected" (raising the floor), an observed value within
// floor + k*MAD is suppressed and returns nil.
func TestSizeDrift_FloorSuppression(t *testing.T) {
	det := NewSizeDriftDetector()

	// observed=130, median=100, mad=5, balanced (k=3.5)
	// Normally z ≈ 4.05 > 3.5 → Rule A fires warning.
	// Set floor=200 → suppression threshold = floor + k*mad = 200 + 3.5*5 = 217.5.
	// 130 <= 217.5 → the fired warning is suppressed → nil.
	const observed = 130
	const bMedian = 100.0
	const bMAD = 5.0
	const sampleCount = 10

	ec := buildSizeEC(observed, bMedian, bMAD, sampleCount, "balanced")

	// Wire the floor lookup directly (same package → unexported field accessible).
	floorValue := 200.0
	ec.floorLookup = func(fp string) float64 {
		return floorValue
	}

	got, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("Evaluate() error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected nil (floor suppressed), got %+v", got)
	}
}

// TestSizeDriftSummary_DirectionMatchesNumbers is the regression test for the
// QA finding that the high-side size anomaly always said "grew" — producing
// "This backup grew to 2.7 GB, about <1× its usual 2.8 GB", which contradicts
// its own figures.
func TestSizeDriftSummary_DirectionMatchesNumbers(t *testing.T) {
	tests := []struct {
		name       string
		observed   float64
		median     float64
		factor     float64
		wantVerb   string
		unwantVerb string
	}{
		{
			name:     "observed below median says shrank",
			observed: 2.7 * 1000 * 1000 * 1000,
			median:   2.8 * 1000 * 1000 * 1000,
			factor:   0.964,
			wantVerb: "shrank", unwantVerb: "grew",
		},
		{
			name:     "observed above median says grew",
			observed: 20 * 1000 * 1000 * 1000,
			median:   4 * 1000 * 1000 * 1000,
			factor:   5,
			wantVerb: "grew", unwantVerb: "shrank",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sizeDriftSummary(tc.observed, tc.median, tc.factor)
			if !strings.Contains(got, tc.wantVerb) {
				t.Errorf("summary %q should contain %q", got, tc.wantVerb)
			}
			if strings.Contains(got, tc.unwantVerb) {
				t.Errorf("summary %q must not contain %q — it contradicts the figures", got, tc.unwantVerb)
			}
		})
	}
}
