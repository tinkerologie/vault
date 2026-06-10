package anomaly

import (
	"encoding/json"
	"fmt"
	"log"
)

// Rule thresholds.
const (
	// sizeGrowthMultiplier is Rule B's trigger: observed > 5× median is flagged
	// as anomalous growth.
	sizeGrowthMultiplier = 5.0
	// sizeShrinkFraction is Rule C's trigger: observed < 0.5× median is flagged
	// critical as a potential data-loss signal.
	sizeShrinkFraction = 0.5
)

// SizeDriftDetector flags anomalies when a job run's backup size deviates
// significantly from the historical baseline.  It applies three rules:
//
//   - Rule A (MAD z-score): fires when |ModifiedZScore| exceeds k (warning) or
//     2k (critical).  Disabled when mad == 0 to avoid spurious ±Inf z-scores;
//     in that case only Rule B / C apply.
//   - Rule B (median multiplier, growth): fires when observed > 5 × median.
//   - Rule C (asymmetric shrinkage): fires critical when observed < 0.5 × median.
//
// The detector returns at most one Anomaly per evaluation.  If Rule C fires
// (shrinkage) it is always returned; otherwise the highest-severity result
// from Rules A and B is returned.  The high side fires ONLY on a real rule
// (A or B); the user-acknowledged floor is a pure suppressor, never a trigger.
type SizeDriftDetector struct{}

// NewSizeDriftDetector constructs a SizeDriftDetector.  No DB handle is needed;
// all data is consumed from the EvalContext.
func NewSizeDriftDetector() *SizeDriftDetector { return &SizeDriftDetector{} }

func (d *SizeDriftDetector) Name() string { return "size_drift" }
func (d *SizeDriftDetector) Kind() Kind   { return KindPerRun }

// Evaluate scores the run's size against the stored baseline using the three
// rules described above.  Returns nil, nil (no signal) when the baseline is
// absent, has fewer than 10 samples, or when the median is 0.
func (d *SizeDriftDetector) Evaluate(ec EvalContext) ([]Anomaly, error) {
	// Nil guard: EvalContext fields must be populated before dereferencing.
	if ec.Job == nil || ec.JobRun == nil {
		return nil, nil
	}

	// Cold-start guard.
	if ec.Baseline == nil || ec.Baseline.SampleCount < 10 {
		return nil, nil
	}

	median := ec.Baseline.BytesMedian
	mad := ec.Baseline.BytesMAD
	sampleCount := ec.Baseline.SampleCount

	// No signal possible without a positive median.
	if median == 0 {
		return nil, nil
	}

	jobID := ec.Job.ID
	observed := float64(ec.JobRun.SizeBytes)
	runID := ec.JobRun.ID

	k := ec.Sensitivity().K()

	// Pre-compute fingerprint and growth factor for all rules.
	fp := Fingerprint("size_drift", ScopeJob, jobID, "total_bytes")
	growthFactor := observed / median

	// --- Rule C: asymmetric shrinkage (always takes priority) ---
	if observed < sizeShrinkFraction*median {
		fpLow := Fingerprint("size_drift", ScopeJob, jobID, "total_bytes_low")
		// Only compute z when mad!=0; otherwise ModifiedZScore returns ±Inf
		// which would corrupt the Details JSON. Fall back to growthFactor.
		var z, dev float64
		if mad != 0 {
			z = ModifiedZScore(observed, median, mad)
			dev = z
		} else {
			dev = growthFactor
		}
		details := buildDetails(z, growthFactor, sampleCount)
		return []Anomaly{
			{
				Fingerprint: fpLow,
				Detector:    "size_drift",
				Severity:    SeverityCritical,
				ScopeKind:   ScopeJob,
				ScopeID:     jobID,
				Metric:      "total_bytes_low",
				Observed:    observed,
				Expected:    &median,
				Deviation:   &dev,
				JobRunID:    &runID,
				Summary:     fmt.Sprintf("backup shrank to %s (%.0f%% of expected %s)", humanizeBytes(observed), growthFactor*100, humanizeBytes(median)),
				Details:     details,
			},
		}, nil
	}

	// --- Rules A + B: high-side anomaly detection ---
	// The high side fires ONLY on a real rule. The floor is a pure suppressor
	// applied after a rule fires, never a trigger.
	severity, zA := evaluateHighSide(observed, median, mad, k, sizeGrowthMultiplier)
	if severity == "" {
		return nil, nil
	}

	// Floor suppression: if the user previously marked this level "expected"
	// (raising the floor), suppress re-firing while observed stays within
	// floor + k*mad.
	if floor := ec.ExpectedFloor(fp); floor > 0 && observed <= floor+k*mad {
		return nil, nil
	}

	// Use zA as the deviation; if mad==0 fall back to growthFactor.
	var dev float64
	if mad != 0 {
		dev = zA
	} else {
		dev = growthFactor
	}

	details := buildDetails(zA, growthFactor, sampleCount)

	return []Anomaly{
		{
			Fingerprint: fp,
			Detector:    "size_drift",
			Severity:    severity,
			ScopeKind:   ScopeJob,
			ScopeID:     jobID,
			Metric:      "total_bytes",
			Observed:    observed,
			Expected:    &median,
			Deviation:   &dev,
			JobRunID:    &runID,
			Summary:     fmt.Sprintf("backup size anomaly: %s (%.1fx median)", humanizeBytes(observed), growthFactor),
			Details:     details,
		},
	}, nil
}

// higherSeverity returns the more severe of two Severity values.
// Empty string ("") is treated as "no signal" and is the lowest rank.
func higherSeverity(a, b Severity) Severity {
	rank := func(s Severity) int {
		switch s {
		case SeverityCritical:
			return 2
		case SeverityWarning:
			return 1
		default:
			return 0
		}
	}
	if rank(a) >= rank(b) {
		return a
	}
	return b
}

// buildDetails marshals the standard detail fields to a JSON string. z_score is
// 0 when mad == 0 (rule A was disabled). Callers must never pass a non-finite
// z (Inf/NaN) — json.Marshal cannot encode those; if it ever errors we log a
// WARN and fall back to "{}" so Details is always valid non-empty JSON.
// Floats are rounded to 2 decimals so notifications and the UI show
// human-friendly values (issue #134).
func buildDetails(zScore, growthFactor float64, windowSize int) string {
	type detailsPayload struct {
		ZScore       float64 `json:"z_score"`
		GrowthFactor float64 `json:"growth_factor"`
		WindowSize   int     `json:"window_size"`
	}
	b, err := json.Marshal(detailsPayload{
		ZScore:       roundTo(zScore, 2),
		GrowthFactor: roundTo(growthFactor, 2),
		WindowSize:   windowSize,
	})
	if err != nil {
		log.Printf("WARN anomaly: buildDetails marshal: %v", err)
		return "{}"
	}
	return string(b)
}
