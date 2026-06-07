package anomaly

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/ruaan-deysel/vault/internal/db"
)

// minCapacitySamples is the minimum number of samples required before the
// capacity trajectory detector will attempt a regression fit. Below this
// threshold the estimate is too noisy to be actionable.
const minCapacitySamples = 14

// totalBytesResetThreshold is the fractional change in total_bytes between
// consecutive samples that triggers a window reset. A >10% change signals
// a disk resize; older samples belong to a different total capacity and
// would corrupt the regression slope.
const totalBytesResetThreshold = 0.10

// lowFreeFraction is the free/total ratio below which the independent
// "free space critically low" anomaly fires, regardless of trajectory slope.
const lowFreeFraction = 0.05

// CapacityTrajectoryDetector is a KindTrend detector that fits a linear
// regression over the stored capacity samples for a destination and projects
// when free space will reach zero. It emits up to two anomalies per run:
//
//  1. "free_bytes_eta_days" — ETA-based warning/critical when the projected
//     runway is shorter than warnDays (warning) or warnDays/2 (critical).
//
//  2. "free_bytes_low" — independent critical when current free/total < 5%,
//     regardless of the regression slope.
type CapacityTrajectoryDetector struct {
	d *db.DB
}

// NewCapacityTrajectoryDetector constructs a CapacityTrajectoryDetector.
// The db handle is required by the daemon-wiring contract (other trend
// detectors, e.g. ReliabilityDetector, also accept *db.DB for the same
// reason); this detector currently reads its data from EvalContext but
// keeps the handle for future open-anomaly dedup queries.
func NewCapacityTrajectoryDetector(d *db.DB) *CapacityTrajectoryDetector {
	return &CapacityTrajectoryDetector{d: d}
}

func (c *CapacityTrajectoryDetector) Name() string { return "capacity_trajectory" }
func (c *CapacityTrajectoryDetector) Kind() Kind   { return KindTrend }

// Evaluate scores the current capacity samples for a destination using OLS
// linear regression and emits anomalies when the projected runway is short
// or free space is already critically low.
//
// CapacitySamples must be ordered oldest → newest (ListCapacitySamples
// returns ASC by sampled_at; the evaluator feeds them in that order).
func (c *CapacityTrajectoryDetector) Evaluate(ec EvalContext) ([]Anomaly, error) {
	if ec.Destination == nil {
		return nil, nil
	}
	destID := ec.Destination.ID

	samples := ec.CapacitySamples
	if len(samples) < minCapacitySamples {
		return nil, nil
	}

	// --- Window reset on total-bytes change > 10% ---
	// Scan samples oldest → newest; restart the window whenever total bytes
	// jumps by more than 10% (disk resize). Use the most recent contiguous
	// window after the last reset.
	window := capacityWindow(samples)
	if len(window) < minCapacitySamples {
		return nil, nil
	}

	// --- OLS fit ---
	// x = days since window[0].SampledAt, y = FreeBytes.
	xs := make([]float64, len(window))
	ys := make([]float64, len(window))
	t0 := window[0].SampledAt
	for i, s := range window {
		xs[i] = s.SampledAt.Sub(t0).Hours() / 24.0
		ys[i] = float64(s.FreeBytes)
	}
	slope, intercept := olsFit(xs, ys)

	latest := window[len(window)-1]
	latestFree := float64(latest.FreeBytes)
	latestTotal := float64(latest.TotalBytes)

	warnDays := ec.Sensitivity().WarnDays()
	var out []Anomaly

	// --- ETA anomaly ---
	// Only meaningful when slope is negative (free space declining) and the
	// latest free value is non-negative. FreeBytes is NOT NULL but has no
	// CHECK >= 0, so a bad/stub value could otherwise yield a negative
	// etaDays that spuriously trips the critical threshold.
	if slope < 0 && latestFree >= 0 {
		// etaDays: how many more days from the latest sample until free hits 0.
		etaDays := latestFree / (-slope)

		var severity Severity
		switch {
		case etaDays < warnDays/2:
			severity = SeverityCritical
		case etaDays < warnDays:
			severity = SeverityWarning
		}

		if severity != "" {
			fp := Fingerprint("capacity_trajectory", ScopeDestination, destID, "free_bytes_eta_days")
			details := buildCapacityDetails(slope, etaDays, len(window), intercept)
			expected := warnDays
			deviation := warnDays - etaDays
			out = append(out, Anomaly{
				Fingerprint: fp,
				Detector:    "capacity_trajectory",
				Severity:    severity,
				ScopeKind:   ScopeDestination,
				ScopeID:     destID,
				Metric:      "free_bytes_eta_days",
				Observed:    etaDays,
				Expected:    &expected,
				Deviation:   &deviation,
				Summary: fmt.Sprintf(
					"storage %q free space projected to run out in %.1f days (%s/day drain)",
					ec.Destination.Name, etaDays, humanizeBytes(-slope),
				),
				Details: details,
			})
		}
	}

	// --- Independent low-free floor anomaly ---
	// Fires regardless of slope when free/total < 5%.
	// Guard against negative/bogus free values (bad stat) to avoid spurious criticals.
	if latestTotal > 0 && latestFree >= 0 && latestFree/latestTotal < lowFreeFraction {
		fp := Fingerprint("capacity_trajectory", ScopeDestination, destID, "free_bytes_low")
		pct := latestFree / latestTotal * 100
		details := buildLowFreeDetails(latestFree, latestTotal, pct)
		out = append(out, Anomaly{
			Fingerprint: fp,
			Detector:    "capacity_trajectory",
			Severity:    SeverityCritical,
			ScopeKind:   ScopeDestination,
			ScopeID:     destID,
			Metric:      "free_bytes_low",
			Observed:    latestFree,
			Summary: fmt.Sprintf(
				"storage %q is critically low: %.1f%% free (%s)",
				ec.Destination.Name, pct, humanizeBytes(latestFree),
			),
			Details: details,
		})
	}

	return out, nil
}

// capacityWindow scans samples oldest→newest and returns the suffix that
// follows the last total-bytes reset (a >10% change). If there is no reset,
// the entire slice is returned unchanged (same backing array, no copy).
func capacityWindow(samples []db.CapacitySample) []db.CapacitySample {
	start := 0
	for i := 1; i < len(samples); i++ {
		prev := float64(samples[i-1].TotalBytes)
		curr := float64(samples[i].TotalBytes)
		if prev > 0 {
			change := (curr - prev) / prev
			if change < 0 {
				change = -change
			}
			if change > totalBytesResetThreshold {
				start = i // reset: new window starts HERE
			}
		}
	}
	return samples[start:]
}

// olsFit computes the ordinary-least-squares slope and intercept for the
// line y ≈ slope*x + intercept.
//
// Returns (0, mean(y)) when n < 2 or when all x values are identical
// (degenerate case — slope is undefined; callers treat slope >= 0 as "no
// signal" so a zero slope is safe).
func olsFit(xs, ys []float64) (slope, intercept float64) {
	n := len(xs)
	if n < 2 {
		if n == 1 {
			return 0, ys[0]
		}
		return 0, 0
	}

	// Compute means.
	var sumX, sumY float64
	for i := range xs {
		sumX += xs[i]
		sumY += ys[i]
	}
	xBar := sumX / float64(n)
	yBar := sumY / float64(n)

	// Compute slope = Σ(x-x̄)(y-ȳ) / Σ(x-x̄)².
	var num, den float64
	for i := range xs {
		dx := xs[i] - xBar
		num += dx * (ys[i] - yBar)
		den += dx * dx
	}
	if den == 0 {
		// All x values are identical (all samples at the same timestamp).
		return 0, yBar
	}
	slope = num / den
	intercept = yBar - slope*xBar
	return slope, intercept
}

// buildCapacityDetails marshals the OLS details into a JSON string.
func buildCapacityDetails(slopeBytesPerDay, etaDays float64, windowSize int, intercept float64) string {
	type payload struct {
		SlopeBytesPerDay float64 `json:"slope_bytes_per_day"`
		EtaDays          float64 `json:"eta_days"`
		WindowSize       int     `json:"window_size"`
		Intercept        float64 `json:"intercept"`
	}
	b, err := json.Marshal(payload{
		SlopeBytesPerDay: slopeBytesPerDay,
		EtaDays:          etaDays,
		WindowSize:       windowSize,
		Intercept:        intercept,
	})
	if err != nil {
		log.Printf("WARN anomaly: buildCapacityDetails marshal: %v", err)
		return "{}"
	}
	return string(b)
}

// buildLowFreeDetails marshals free/total/pct for the free_bytes_low anomaly.
func buildLowFreeDetails(freeBytes, totalBytes, pctFree float64) string {
	type payload struct {
		FreeBytes  float64 `json:"free_bytes"`
		TotalBytes float64 `json:"total_bytes"`
		PctFree    float64 `json:"pct_free"`
	}
	b, err := json.Marshal(payload{
		FreeBytes:  freeBytes,
		TotalBytes: totalBytes,
		PctFree:    pctFree,
	})
	if err != nil {
		log.Printf("WARN anomaly: buildLowFreeDetails marshal: %v", err)
		return "{}"
	}
	return string(b)
}
