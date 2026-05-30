package anomaly

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// durationSecs is a convenience helper to create a *int for DurationSeconds.
func durationSecs(s int) *int { return &s }

// buildDurationEC constructs a minimal EvalContext for DurationDriftDetector
// tests. Pass status="" to use the default "completed" status.
// Pass itemsDone=-1 to use the default (non-zero) value of 1.
func buildDurationEC(
	observedSecs int,
	bMedian, bMAD float64,
	sampleCount int,
	sensitivity string,
	status string,
	itemsDone int,
) EvalContext {
	if status == "" {
		status = "completed"
	}
	if itemsDone < 0 {
		itemsDone = 1
	}

	run := &db.JobRun{
		ID:              42,
		JobID:           7,
		Status:          status,
		DurationSeconds: durationSecs(observedSecs),
		ItemsDone:       itemsDone,
	}
	job := &db.Job{ID: 7}

	var baseline *db.JobBaseline
	if sampleCount > 0 || bMedian != 0 {
		baseline = &db.JobBaseline{
			JobID:          7,
			SampleCount:    sampleCount,
			DurationMedian: bMedian,
			DurationMAD:    bMAD,
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

func TestDurationDrift(t *testing.T) {
	det := NewDurationDriftDetector()

	tests := []struct {
		name        string
		observed    int
		bMedian     float64
		bMAD        float64
		sampleCount int
		sensitivity string
		status      string // "" → "completed"
		itemsDone   int    // -1 → default (1)
		wantNil     bool
		wantSev     Severity
		wantMetric  string
	}{
		// 1. Cold start: SampleCount < 10 → nil
		{
			name:        "cold_start_below_10_samples",
			observed:    1000,
			bMedian:     100,
			bMAD:        10,
			sampleCount: 9,
			sensitivity: "balanced",
			itemsDone:   -1,
			wantNil:     true,
		},
		// 2. Normal within band → nil
		// z = 0.6745 * (105-100)/5 = 0.6745 → well below k=3.5
		{
			name:        "normal_within_band",
			observed:    105,
			bMedian:     100,
			bMAD:        5,
			sampleCount: 10,
			sensitivity: "balanced",
			itemsDone:   -1,
			wantNil:     true,
		},
		// 3. Warning z-score
		// z = 0.6745 * (130-100)/5 ≈ 4.047 → > k=3.5, < 2k=7 → warning
		{
			name:        "warning_z_score",
			observed:    130,
			bMedian:     100,
			bMAD:        5,
			sampleCount: 10,
			sensitivity: "balanced",
			itemsDone:   -1,
			wantNil:     false,
			wantSev:     SeverityWarning,
			wantMetric:  "duration_seconds",
		},
		// 4. Critical z-score
		// z = 0.6745 * (200-100)/5 ≈ 13.49 → >= 2k=7 → critical
		{
			name:        "critical_z_score",
			observed:    200,
			bMedian:     100,
			bMAD:        5,
			sampleCount: 10,
			sensitivity: "balanced",
			itemsDone:   -1,
			wantNil:     false,
			wantSev:     SeverityCritical,
			wantMetric:  "duration_seconds",
		},
		// 5. ItemsDone == 0 → nil (even with huge observed that would otherwise fire)
		{
			name:        "items_done_zero_suppressed",
			observed:    500, // would be critical z-score otherwise
			bMedian:     100,
			bMAD:        5,
			sampleCount: 10,
			sensitivity: "balanced",
			itemsDone:   0,
			wantNil:     true,
		},
		// 6. Cancelled + over-timeout (observed > median + k*mad)
		// median=100, mad=5, k=3.5 → threshold = 100 + 3.5*5 = 117.5
		// observed=300 >> 117.5 → critical regardless of standard z-score band
		{
			name:        "cancelled_over_timeout_critical",
			observed:    300,
			bMedian:     100,
			bMAD:        5,
			sampleCount: 10,
			sensitivity: "balanced",
			status:      "cancelled",
			itemsDone:   -1,
			wantNil:     false,
			wantSev:     SeverityCritical,
			wantMetric:  "duration_seconds",
		},
		// 7. Cancelled but NOT over threshold (user quick-cancel) → falls through
		// to normal rules; observed=105 → normal, z≈0.67 < k=3.5 → nil
		{
			name:        "cancelled_within_band_nil",
			observed:    105,
			bMedian:     100,
			bMAD:        5,
			sampleCount: 10,
			sensitivity: "balanced",
			status:      "cancelled",
			itemsDone:   -1,
			wantNil:     true,
		},
		// 8. mad == 0 + Rule B multiplier fires (observed > 5*median, mad=0 → Rule A disabled)
		// Rule B: 600 > 5*100 = 500 → warning
		{
			name:        "mad_zero_multiplier_fires",
			observed:    600,
			bMedian:     100,
			bMAD:        0,
			sampleCount: 10,
			sensitivity: "balanced",
			itemsDone:   -1,
			wantNil:     false,
			wantSev:     SeverityWarning,
			wantMetric:  "duration_seconds",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ec := buildDurationEC(
				tc.observed, tc.bMedian, tc.bMAD,
				tc.sampleCount, tc.sensitivity,
				tc.status, tc.itemsDone,
			)
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
			// Verify Details JSON contains the required keys and z_score is finite.
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

// TestDurationDrift_MADZeroZScoreIsZero verifies that when mad==0 and Rule B
// fires (observed > 5*median), z_score in Details is 0 (not ±Inf) and all
// required keys are present.
func TestDurationDrift_MADZeroZScoreIsZero(t *testing.T) {
	det := NewDurationDriftDetector()

	// observed=600, median=100, mad=0 → Rule A disabled, Rule B fires (600>500).
	ec := buildDurationEC(600, 100, 0, 10, "balanced", "", -1)
	got, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("Evaluate() error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 anomaly, got %d: %+v", len(got), got)
	}
	a := got[0]
	if a.Severity != SeverityWarning {
		t.Errorf("Severity: want warning, got %q", a.Severity)
	}
	if a.Details == "" {
		t.Fatal("Details must be non-empty JSON")
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
		t.Fatalf("z_score not a float64: %v", d["z_score"])
	}
	if z != 0 {
		t.Errorf("z_score: want 0 (not Inf) when mad==0, got %v", z)
	}
}

// TestDurationDrift_FloorSuppression verifies that when a user has marked prior
// anomalies as "expected" (raising the floor), an observed value within
// floor + k*MAD is suppressed and returns nil.
func TestDurationDrift_FloorSuppression(t *testing.T) {
	det := NewDurationDriftDetector()

	// observed=130, median=100, mad=5, balanced (k=3.5)
	// Normally z ≈ 4.05 > 3.5 → Rule A fires warning.
	// Set floor=200 → suppression threshold = floor + k*mad = 200 + 3.5*5 = 217.5.
	// 130 <= 217.5 → the fired warning is suppressed → nil.
	ec := buildDurationEC(130, 100, 5, 10, "balanced", "", -1)

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

// TestDurationDrift_NilDurationSeconds verifies that a nil DurationSeconds
// (in-progress run) produces no anomaly.
func TestDurationDrift_NilDurationSeconds(t *testing.T) {
	det := NewDurationDriftDetector()

	run := &db.JobRun{
		ID:              99,
		JobID:           7,
		Status:          "running",
		DurationSeconds: nil, // not yet completed
		ItemsDone:       5,
	}
	baseline := &db.JobBaseline{
		JobID:          7,
		SampleCount:    20,
		DurationMedian: 100,
		DurationMAD:    5,
	}
	ec := EvalContext{
		JobRun:            run,
		Job:               &db.Job{ID: 7},
		Baseline:          baseline,
		GlobalSensitivity: "balanced",
		Clock:             NewFakeClock(time.Now()),
	}

	got, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("Evaluate() error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want nil for nil DurationSeconds, got %+v", got)
	}
}
