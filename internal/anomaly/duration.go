package anomaly

import "fmt"

// Rule thresholds for duration anomaly detection.
const (
	// durationGrowthMultiplier is Rule B's trigger: observed > 5× median is
	// flagged as anomalously long.
	durationGrowthMultiplier = 5.0
)

// DurationDriftDetector flags anomalies when a job run's duration deviates
// significantly from the historical baseline. It applies two rules:
//
//   - Rule A (MAD z-score, high side only): fires when |ModifiedZScore| exceeds
//     k (warning) or 2k (critical). Disabled when mad == 0 to avoid spurious
//     ±Inf z-scores; in that case only Rule B applies.
//   - Rule B (median multiplier, growth): fires when observed > 5 × median.
//
// Two additional guards apply before the standard rules:
//
//   - ItemsDone == 0: a run that performed no work carries no meaningful
//     duration signal and is suppressed (nil).
//   - Cancelled + over-baseline: a cancelled run whose duration is far above
//     the historical median (observed > median + k*mad) is a strong signal that
//     the job stalled or timed out, and is promoted to CRITICAL regardless of
//     the standard z-score thresholds.
//
// There is intentionally no shrinkage rule: short runs are not suspicious.
// The high side fires ONLY on a real rule (A or B); the user-acknowledged
// floor is a pure suppressor, never a trigger.
type DurationDriftDetector struct{}

// NewDurationDriftDetector constructs a DurationDriftDetector. No DB handle is
// needed; all data is consumed from the EvalContext.
func NewDurationDriftDetector() *DurationDriftDetector { return &DurationDriftDetector{} }

func (d *DurationDriftDetector) Name() string { return "duration_drift" }
func (d *DurationDriftDetector) Kind() Kind   { return KindPerRun }

// Evaluate scores the run's duration against the stored baseline using the
// rules described above. Returns nil, nil (no signal) when:
//   - The baseline is absent or has fewer than 10 samples (cold start).
//   - The baseline median is 0.
//   - DurationSeconds is nil (run not yet completed).
//   - ItemsDone == 0 (no work was performed — not a meaningful duration signal).
func (d *DurationDriftDetector) Evaluate(ec EvalContext) ([]Anomaly, error) {
	// Nil guard: EvalContext fields must be populated before dereferencing.
	if ec.Job == nil || ec.JobRun == nil {
		return nil, nil
	}

	// Cold-start guard.
	if ec.Baseline == nil || ec.Baseline.SampleCount < 10 {
		return nil, nil
	}

	median := ec.Baseline.DurationMedian
	mad := ec.Baseline.DurationMAD
	sampleCount := ec.Baseline.SampleCount

	// No signal possible without a positive median.
	if median == 0 {
		return nil, nil
	}

	// DurationSeconds is nil for in-progress or never-completed runs.
	if ec.JobRun.DurationSeconds == nil {
		return nil, nil
	}

	// ItemsDone == 0 means the run performed no actual work. A zero-work run
	// has an artificially short (or irrelevant) duration and carries no useful
	// signal. We use ItemsDone (completed items) rather than ItemsTotal because
	// total counts planned items, not processed ones.
	if ec.JobRun.ItemsDone == 0 {
		return nil, nil
	}

	jobID := ec.Job.ID
	observed := float64(*ec.JobRun.DurationSeconds)
	runID := ec.JobRun.ID
	k := ec.Sensitivity().K()

	const metric = "duration_seconds"
	fp := Fingerprint("duration_drift", ScopeJob, jobID, metric)
	growthFactor := observed / median

	// --- Cancelled + stall/timeout guard ---
	// A "cancelled" status can mean either an operator cancel (harmless) or a
	// stall/timeout kill. We have no separate flag to distinguish them in the
	// persisted run, so we use a conservative heuristic: if the run was
	// cancelled AND its duration is meaningfully above the historical median
	// (observed > median + k*mad), it almost certainly represents a stall or
	// timeout — promote to CRITICAL regardless of the z-score band.
	//
	// When mad == 0 (perfectly stable baseline), any cancelled run with
	// observed > median satisfies this condition (median + 0 = median).
	if ec.JobRun.Status == "cancelled" && observed > median+k*mad {
		dev := growthFactor // always finite
		var zA float64
		if mad != 0 {
			zA = ModifiedZScore(observed, median, mad)
			dev = zA
		}
		details := buildDetails(zA, growthFactor, sampleCount)
		return []Anomaly{
			{
				Fingerprint: fp,
				Detector:    "duration_drift",
				Severity:    SeverityCritical,
				ScopeKind:   ScopeJob,
				ScopeID:     jobID,
				Metric:      metric,
				Observed:    observed,
				Expected:    &median,
				Deviation:   &dev,
				JobRunID:    &runID,
				Summary: fmt.Sprintf(
					"job was cancelled after %.0fs (%.1fx median — possible stall/timeout)",
					observed, growthFactor,
				),
				Details: details,
			},
		}, nil
	}

	// --- Rules A + B: high-side anomaly detection ---
	// The high side fires ONLY on a real rule. The floor is a pure suppressor
	// applied after a rule fires, never a trigger.
	severity, zA := evaluateHighSide(observed, median, mad, k, durationGrowthMultiplier)
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
			Detector:    "duration_drift",
			Severity:    severity,
			ScopeKind:   ScopeJob,
			ScopeID:     jobID,
			Metric:      metric,
			Observed:    observed,
			Expected:    &median,
			Deviation:   &dev,
			JobRunID:    &runID,
			Summary: fmt.Sprintf(
				"backup duration anomaly: %.0fs (%.1fx median)",
				observed, growthFactor,
			),
			Details: details,
		},
	}, nil
}
