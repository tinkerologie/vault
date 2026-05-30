package anomaly

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// helpers ---------------------------------------------------------------

// makeDest returns a minimal StorageDestination for capacity tests.
func makeDest(id int64) *db.StorageDestination {
	return &db.StorageDestination{
		ID:   id,
		Name: "test-dest",
		Type: "local",
	}
}

// buildSamples constructs n capacity samples spaced 1 day apart, starting at
// t0. freeFunc maps sample index (0=oldest) to a free-bytes value.
// totalBytes is constant across all samples unless totalFunc is non-nil.
func buildSamples(t0 time.Time, n int, totalBytes int64, freeFunc func(i int) int64) []db.CapacitySample {
	s := make([]db.CapacitySample, n)
	for i := range s {
		s[i] = db.CapacitySample{
			ID:         int64(i + 1),
			DestID:     1,
			SampledAt:  t0.Add(time.Duration(i) * 24 * time.Hour),
			TotalBytes: totalBytes,
			FreeBytes:  freeFunc(i),
		}
	}
	return s
}

// makeEC builds a minimal EvalContext with the given destination, samples, and
// sensitivity.
func makeEC(dest *db.StorageDestination, samples []db.CapacitySample, sens string) EvalContext {
	return EvalContext{
		Destination:       dest,
		CapacitySamples:   samples,
		GlobalSensitivity: sens,
		Clock:             NewFakeClock(time.Now()),
	}
}

// hasSeverity returns true if any anomaly in the slice matches metric+severity.
func hasSeverity(anomalies []Anomaly, metric string, sev Severity) bool {
	for _, a := range anomalies {
		if a.Metric == metric && a.Severity == sev {
			return true
		}
	}
	return false
}

// detectorForTest returns a CapacityTrajectoryDetector with a nil DB (safe
// for unit tests that construct EvalContext directly).
func detectorForTest() *CapacityTrajectoryDetector {
	return NewCapacityTrajectoryDetector(nil)
}

// OLS helper test --------------------------------------------------------

// TestOLSFit_KnownLine verifies that olsFit recovers the exact coefficients
// for the line y = -2x + 100 (slope=-2, intercept=100).
func TestOLSFit_KnownLine(t *testing.T) {
	t.Parallel()
	// Generate 10 points on y = -2x + 100.
	xs := make([]float64, 10)
	ys := make([]float64, 10)
	for i := range xs {
		xs[i] = float64(i)
		ys[i] = -2*float64(i) + 100
	}
	slope, intercept := olsFit(xs, ys)
	if slope < -2.0001 || slope > -1.9999 {
		t.Errorf("slope = %f, want -2.0", slope)
	}
	if intercept < 99.9999 || intercept > 100.0001 {
		t.Errorf("intercept = %f, want 100.0", intercept)
	}
}

// TestOLSFit_FlatLine verifies slope=0 for a perfectly flat series.
func TestOLSFit_FlatLine(t *testing.T) {
	t.Parallel()
	xs := []float64{0, 1, 2, 3}
	ys := []float64{50, 50, 50, 50}
	slope, intercept := olsFit(xs, ys)
	if slope != 0 {
		t.Errorf("slope = %f, want 0", slope)
	}
	if intercept < 49.9999 || intercept > 50.0001 {
		t.Errorf("intercept = %f, want 50", intercept)
	}
}

// TestOLSFit_Degenerate verifies that identical x values don't panic/NaN.
func TestOLSFit_Degenerate(t *testing.T) {
	t.Parallel()
	xs := []float64{5, 5, 5}
	ys := []float64{100, 200, 300}
	slope, intercept := olsFit(xs, ys)
	if slope != 0 {
		t.Errorf("degenerate: slope = %f, want 0", slope)
	}
	_ = intercept // yBar — not tested for exact value
}

// Detector tests --------------------------------------------------------

// TestCapacityTraj_TooFewSamples verifies that < 14 samples → nil.
func TestCapacityTraj_TooFewSamples(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	dest := makeDest(1)
	// 13 samples (one below the minimum).
	samples := buildSamples(t0, 13, 1<<40, func(i int) int64 {
		return int64(500e9) - int64(i)*int64(1e9)
	})
	ec := makeEC(dest, samples, "balanced")
	det := detectorForTest()
	out, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected nil anomalies for < 14 samples, got %d", len(out))
	}
}

// TestCapacityTraj_FlatSlope verifies that flat/growing free space → no ETA
// anomaly (but may still fire low-free if applicable; this test uses plenty
// of free space).
func TestCapacityTraj_FlatSlope(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	dest := makeDest(1)
	total := int64(1 << 40)
	// Flat free space at 50% — no decline, no low-free.
	samples := buildSamples(t0, 20, total, func(i int) int64 {
		return total / 2
	})
	ec := makeEC(dest, samples, "balanced")
	det := detectorForTest()
	out, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// ETA anomaly must not fire for flat/growing trend.
	for _, a := range out {
		if a.Metric == "free_bytes_eta_days" {
			t.Errorf("unexpected ETA anomaly on flat slope: %+v", a)
		}
	}
}

// TestCapacityTraj_GrowingSlope verifies that growing free space → no ETA
// anomaly (slope > 0 means more free space over time).
func TestCapacityTraj_GrowingSlope(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	dest := makeDest(1)
	total := int64(1 << 40)
	// Free space growing by 1 GB/day.
	samples := buildSamples(t0, 20, total, func(i int) int64 {
		return int64(100e9) + int64(i)*int64(1e9)
	})
	ec := makeEC(dest, samples, "balanced")
	det := detectorForTest()
	out, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, a := range out {
		if a.Metric == "free_bytes_eta_days" {
			t.Errorf("unexpected ETA anomaly on growing slope: %+v", a)
		}
	}
}

// TestCapacityTraj_WarningETA verifies that a declining trajectory with ETA
// between warnDays/2 and warnDays emits a warning-severity ETA anomaly.
// Balanced sensitivity: warnDays = 14, so we need ETA in [7, 14).
func TestCapacityTraj_WarningETA(t *testing.T) {
	t.Parallel()
	// balanced: warnDays=14, critical threshold = 7 days
	// We want ETA ≈ 10 days → use slope = -latestFree/etaDays.
	// latestFree after 20 days of 5 GB/day drain starting at 200 GB:
	//   sample[19].FreeBytes = 200e9 - 19*5e9 = 200e9 - 95e9 = 105 GB
	// ETA from latest sample = 105e9 / 5e9 per day = 21 days → too long.
	//
	// Adjust: drain 10 GB/day starting at 120 GB free:
	//   sample[19].FreeBytes = 120e9 - 19*10e9 = 120e9 - 190e9 → negative, bad.
	//
	// Use 14 samples (minimum), drain 1 GB/day, start at 13 GB:
	//   sample[0].free = 13e9; sample[13].free = 0 → ETA from sample[13]=0/1=0 → critical.
	//
	// Better: 14 samples, start at 20 GB, drain 1 GB/day:
	//   sample[13].free = 20e9-13*1e9 = 7e9; ETA = 7e9/(1e9/day)=7 days → boundary, use 8 GB/day rate.
	// Start at 21 GB, drain 1 GB/day → sample[13].free = 21e9-13*1e9=8e9; ETA=8 days → warning.
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	dest := makeDest(1)
	total := int64(1 << 40) // 1 TiB total, plenty
	drainPerDay := int64(1e9)
	startFree := int64(22e9) // after 14 days (index 0..13), latest free = 22-13=9 GB → ETA=9 days (warning)
	samples := buildSamples(t0, 14, total, func(i int) int64 {
		return startFree - int64(i)*drainPerDay
	})
	ec := makeEC(dest, samples, "balanced")
	det := detectorForTest()
	out, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasSeverity(out, "free_bytes_eta_days", SeverityWarning) {
		t.Errorf("expected warning ETA anomaly; got %v", out)
	}
	if len(out) == 0 {
		t.Fatal("expected at least one anomaly")
	}
	// Also verify Details keys are present.
	for _, a := range out {
		if a.Metric != "free_bytes_eta_days" {
			continue
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(a.Details), &d); err != nil {
			t.Fatalf("Details not valid JSON: %v", err)
		}
		for _, key := range []string{"slope_bytes_per_day", "eta_days", "window_size", "intercept"} {
			if _, ok := d[key]; !ok {
				t.Errorf("Details missing key %q", key)
			}
		}
	}
}

// TestCapacityTraj_CriticalETA verifies that a steep decline with ETA <
// warnDays/2 emits a critical-severity ETA anomaly.
// Balanced: critical when ETA < 7 days.
func TestCapacityTraj_CriticalETA(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	dest := makeDest(1)
	total := int64(1 << 40)
	// 14 samples, start at 6 GB free, drain 1 GB/day:
	// sample[13].free = 6e9-13*1e9 = -7e9 → clamp. The OLS slope is ~-1 GB/day.
	// latestFree from sample[13] (actual value stored) = 6e9 - 13*1e9 = -7e9 is
	// invalid. Use a shallower drain so sample values stay positive:
	// drain 0.3 GB/day, start 5 GB: sample[13] = 5e9-13*0.3e9 = 5e9-3.9e9=1.1e9
	// ETA = 1.1e9 / 0.3e9 = 3.7 days → critical.
	drainPerDay := int64(300e6) // 300 MB/day
	startFree := int64(5e9)
	samples := buildSamples(t0, 14, total, func(i int) int64 {
		return startFree - int64(i)*drainPerDay
	})
	ec := makeEC(dest, samples, "balanced")
	det := detectorForTest()
	out, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasSeverity(out, "free_bytes_eta_days", SeverityCritical) {
		t.Errorf("expected critical ETA anomaly; got %v", out)
	}
}

// TestCapacityTraj_TotalBytesReset verifies that a >10% jump in total_bytes
// resets the window and, if the post-reset window has < 14 samples, no ETA
// anomaly is returned.
func TestCapacityTraj_TotalBytesReset(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	dest := makeDest(1)

	// Build 20 samples: first 15 at 1 TiB total, then a disk resize to 2 TiB
	// at sample index 15. Post-reset window = 5 samples → < 14 → no anomaly.
	var samples []db.CapacitySample
	for i := 0; i < 15; i++ {
		samples = append(samples, db.CapacitySample{
			ID:         int64(i + 1),
			DestID:     1,
			SampledAt:  t0.Add(time.Duration(i) * 24 * time.Hour),
			TotalBytes: 1 << 40,
			FreeBytes:  int64(100e9) - int64(i)*int64(1e9),
		})
	}
	// Reset: 2 TiB disk. Post-reset window is samples 15..19 (5 samples).
	for i := 15; i < 20; i++ {
		samples = append(samples, db.CapacitySample{
			ID:         int64(i + 1),
			DestID:     1,
			SampledAt:  t0.Add(time.Duration(i) * 24 * time.Hour),
			TotalBytes: 2 << 40, // doubled: >10% change triggers reset
			FreeBytes:  int64(500e9) - int64(i-15)*int64(1e9),
		})
	}

	ec := makeEC(dest, samples, "balanced")
	det := detectorForTest()
	out, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Post-reset window is only 5 samples → detector returns nil.
	for _, a := range out {
		if a.Metric == "free_bytes_eta_days" {
			t.Errorf("unexpected ETA anomaly after reset with < 14 post-reset samples: %+v", a)
		}
	}
}

// TestCapacityTraj_TotalBytesReset_SufficientWindow verifies that when the
// post-reset window has >= 14 samples, the detector still evaluates correctly
// using only those samples.
func TestCapacityTraj_TotalBytesReset_SufficientWindow(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	dest := makeDest(1)

	// 5 samples at 1 TiB (declining), then 20 post-reset samples at 2 TiB
	// with flat/growing free space. The pre-reset samples should be ignored.
	var samples []db.CapacitySample
	for i := 0; i < 5; i++ {
		samples = append(samples, db.CapacitySample{
			ID:         int64(i + 1),
			DestID:     1,
			SampledAt:  t0.Add(time.Duration(i) * 24 * time.Hour),
			TotalBytes: 1 << 40,
			FreeBytes:  int64(100e9) - int64(i)*int64(50e9), // steep decline pre-reset
		})
	}
	// Post-reset: flat free space at 500 GB on a 2 TiB disk → no ETA anomaly.
	for i := 5; i < 25; i++ {
		samples = append(samples, db.CapacitySample{
			ID:         int64(i + 1),
			DestID:     1,
			SampledAt:  t0.Add(time.Duration(i) * 24 * time.Hour),
			TotalBytes: 2 << 40,
			FreeBytes:  int64(500e9), // flat
		})
	}

	ec := makeEC(dest, samples, "balanced")
	det := detectorForTest()
	out, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Flat post-reset window → no ETA anomaly.
	for _, a := range out {
		if a.Metric == "free_bytes_eta_days" {
			t.Errorf("unexpected ETA anomaly; post-reset window is flat: %+v", a)
		}
	}
}

// TestCapacityTraj_LowFreeFloor verifies that when free/total < 5%, a
// critical free_bytes_low anomaly is emitted regardless of slope.
func TestCapacityTraj_LowFreeFloor(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	dest := makeDest(1)
	total := int64(1 << 40) // 1 TiB
	// Free at 2% of total (well below 5%). Flat slope → no ETA anomaly.
	freeBytes := total * 2 / 100
	samples := buildSamples(t0, 14, total, func(i int) int64 {
		return freeBytes
	})
	ec := makeEC(dest, samples, "balanced")
	det := detectorForTest()
	out, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasSeverity(out, "free_bytes_low", SeverityCritical) {
		t.Errorf("expected critical free_bytes_low anomaly; got %v", out)
	}
	// Verify Details keys.
	for _, a := range out {
		if a.Metric != "free_bytes_low" {
			continue
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(a.Details), &d); err != nil {
			t.Fatalf("free_bytes_low Details not valid JSON: %v", err)
		}
		for _, key := range []string{"free_bytes", "total_bytes", "pct_free"} {
			if _, ok := d[key]; !ok {
				t.Errorf("free_bytes_low Details missing key %q", key)
			}
		}
	}
}

// TestCapacityTraj_BothAnomalies verifies that both free_bytes_eta_days and
// free_bytes_low can fire together in a single Evaluate call.
func TestCapacityTraj_BothAnomalies(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	dest := makeDest(1)
	total := int64(1 << 40)
	// 14 samples: free starts at 4% of total (well below 5% floor) and
	// declines by 0.3% of total per day.
	//   sample[0].free  = 0.04 * total ≈ 44 GB
	//   sample[13].free = (0.04 - 13*0.003) * total = (0.04-0.039)*total = 0.001*total ≈ 1.1 GB
	//   OLS slope ≈ -0.003*total/day ≈ -3.3 GB/day
	//   ETA ≈ 1.1e9 / 3.3e9 ≈ 0.33 days → critical ETA (< warnDays/2=7)
	// Both free_bytes_low and free_bytes_eta_days should fire.
	startFreeRatio := 0.04
	drainRatio := 0.003
	samples := buildSamples(t0, 14, total, func(i int) int64 {
		ratio := startFreeRatio - float64(i)*drainRatio
		if ratio < 0 {
			ratio = 0
		}
		return int64(float64(total) * ratio)
	})
	ec := makeEC(dest, samples, "balanced")
	det := detectorForTest()
	out, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasSeverity(out, "free_bytes_low", SeverityCritical) {
		t.Errorf("expected free_bytes_low critical; got %v", out)
	}
	// The declining slope should also produce an ETA anomaly.
	hasETA := false
	for _, a := range out {
		if a.Metric == "free_bytes_eta_days" {
			hasETA = true
		}
	}
	if !hasETA {
		t.Errorf("expected free_bytes_eta_days anomaly alongside free_bytes_low; got %v", out)
	}
}

// TestCapacityTraj_NilDestination verifies that nil Destination → nil, nil.
func TestCapacityTraj_NilDestination(t *testing.T) {
	t.Parallel()
	ec := EvalContext{
		Destination:     nil,
		CapacitySamples: buildSamples(time.Now(), 20, 1<<40, func(i int) int64 { return int64(100e9) }),
		Clock:           NewFakeClock(time.Now()),
	}
	det := detectorForTest()
	out, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected nil for nil Destination, got %d anomalies", len(out))
	}
}

// TestCapacityTraj_Fingerprints verifies that ETA and low-free anomalies have
// distinct, deterministic fingerprints.
func TestCapacityTraj_Fingerprints(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	dest := makeDest(42)
	total := int64(1 << 40)
	// Low free (2%) + declining → both anomalies fire.
	startFree := total * 2 / 100
	samples := buildSamples(t0, 14, total, func(i int) int64 {
		return startFree - int64(i)*int64(10e6) // gentle drain
	})
	ec := makeEC(dest, samples, "balanced")
	det := detectorForTest()
	out, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fpETA := Fingerprint("capacity_trajectory", ScopeDestination, 42, "free_bytes_eta_days")
	fpLow := Fingerprint("capacity_trajectory", ScopeDestination, 42, "free_bytes_low")
	for _, a := range out {
		if a.Metric == "free_bytes_eta_days" && a.Fingerprint != fpETA {
			t.Errorf("ETA fingerprint = %q, want %q", a.Fingerprint, fpETA)
		}
		if a.Metric == "free_bytes_low" && a.Fingerprint != fpLow {
			t.Errorf("low-free fingerprint = %q, want %q", a.Fingerprint, fpLow)
		}
	}
}

// TestCapacityTraj_SensitivityStrict verifies that strict sensitivity
// (warnDays=30) fires a warning for longer ETA values than balanced.
func TestCapacityTraj_SensitivityStrict(t *testing.T) {
	t.Parallel()
	// strict: warnDays=30, critical=15.
	// Build samples so ETA ≈ 20 days → warning on strict but not on balanced (14).
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	dest := makeDest(1)
	total := int64(1 << 40)
	// 20 samples, 1 GB/day drain, start at 40 GB:
	// sample[19].free = 40e9 - 19*1e9 = 21e9; ETA = 21 days → warning on strict (>15), not critical.
	startFree := int64(40e9)
	drainPerDay := int64(1e9)
	samples := buildSamples(t0, 20, total, func(i int) int64 {
		return startFree - int64(i)*drainPerDay
	})

	// Balanced: warnDays=14, ETA=21 → above warning threshold → no anomaly.
	ecBalanced := makeEC(dest, samples, "balanced")
	det := detectorForTest()
	outBalanced, _ := det.Evaluate(ecBalanced)
	for _, a := range outBalanced {
		if a.Metric == "free_bytes_eta_days" {
			t.Errorf("balanced: unexpected ETA anomaly for ETA>warnDays: %+v", a)
		}
	}

	// Strict: warnDays=30, ETA=21 → within warning zone.
	ecStrict := makeEC(dest, samples, "strict")
	outStrict, _ := det.Evaluate(ecStrict)
	if !hasSeverity(outStrict, "free_bytes_eta_days", SeverityWarning) {
		t.Errorf("strict: expected warning ETA anomaly for ETA ≈ 21 days; got %v", outStrict)
	}
}

// TestCapacityTraj_DetailsJSON verifies all expected fields in Details are
// present and non-empty for a warning ETA anomaly.
func TestCapacityTraj_DetailsJSON(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	dest := makeDest(1)
	total := int64(1 << 40)
	samples := buildSamples(t0, 14, total, func(i int) int64 {
		return int64(22e9) - int64(i)*int64(1e9) // ETA≈9 days, warning
	})
	ec := makeEC(dest, samples, "balanced")
	det := detectorForTest()
	out, _ := det.Evaluate(ec)
	if len(out) == 0 {
		t.Fatal("expected at least one anomaly")
	}
	for _, a := range out {
		if a.Metric != "free_bytes_eta_days" {
			continue
		}
		if !strings.HasPrefix(a.Details, "{") {
			t.Errorf("Details is not JSON object: %q", a.Details)
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(a.Details), &d); err != nil {
			t.Fatalf("Details unmarshal: %v", err)
		}
		for _, k := range []string{"slope_bytes_per_day", "eta_days", "window_size", "intercept"} {
			if _, ok := d[k]; !ok {
				t.Errorf("missing Details key %q", k)
			}
		}
	}
}
