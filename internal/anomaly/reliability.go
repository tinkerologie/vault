package anomaly

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/ruaan-deysel/vault/internal/db"
)

// maxStreakWindow is the maximum number of recent runs considered when
// computing the consecutive-failure streak. Even if EvalContext.RecentRuns
// carries more runs, we cap at 10 so the streak has a bounded horizon.
const maxStreakWindow = 10

// ReliabilityDetector raises anomalies based on two sub-signals:
//
//  1. Failure streak: a run is "failed" when Status=="failed" OR
//     ItemsFailed>0. A contiguous streak of such failures (newest-first)
//     at or above Sensitivity().Streak() fires CRITICAL.
//
//  2. Verify regression: when the two most recent verify runs for the job
//     show a pass→fail transition (previous passed, newest failed), fires
//     CRITICAL on "verify_outcome".
//
// Both signals may fire in the same Evaluate call (up to 2 anomalies
// returned).
//
// NOTE — auto-resolution gap: the evaluator's resolveSoftAnomalies only
// auto-resolves info/warning rows. A "failure_streak" CRITICAL opened by
// this detector will NOT be auto-resolved when the job recovers; it requires
// an explicit acknowledgement by the operator. Implementing auto-resolution
// for reliability criticals on recovery is deferred to the Task 13
// integration concern. Do NOT change criticals to warnings here as a
// workaround — that would incorrectly demote size_drift/duration_drift
// criticals as well.
type ReliabilityDetector struct {
	d *db.DB
}

// NewReliabilityDetector constructs a ReliabilityDetector backed by the
// provided DB handle. The handle is needed to query verify runs, which are
// not pre-loaded into EvalContext.
func NewReliabilityDetector(d *db.DB) *ReliabilityDetector {
	return &ReliabilityDetector{d: d}
}

func (r *ReliabilityDetector) Name() string { return "reliability" }
func (r *ReliabilityDetector) Kind() Kind   { return KindPerRun }

// Evaluate runs both sub-signals and returns any detected anomalies.
func (r *ReliabilityDetector) Evaluate(ec EvalContext) ([]Anomaly, error) {
	if ec.Job == nil {
		return nil, nil
	}
	jobID := ec.Job.ID

	var out []Anomaly

	// --- Signal 1: consecutive failure streak ---
	if a := r.evalStreak(ec, jobID); a != nil {
		out = append(out, *a)
	}

	// --- Signal 2: verify regression ---
	a, err := r.evalVerifyRegression(jobID)
	if err != nil {
		// Non-fatal: log and continue so the streak result is still returned.
		log.Printf("WARN anomaly: reliability verify regression query (job %d): %v", jobID, err)
	} else if a != nil {
		out = append(out, *a)
	}

	return out, nil
}

// evalStreak counts contiguous failures from the newest run and emits a
// CRITICAL anomaly when the streak meets or exceeds the sensitivity threshold.
//
// A run is considered failed when: Status == "failed" OR ItemsFailed > 0.
// The window is capped at maxStreakWindow (10) regardless of how many runs
// are in RecentRuns. RecentRuns is ordered newest-first (GetJobRuns uses
// ORDER BY started_at DESC).
//
// If the newest run is clean (not failed and ItemsFailed == 0), the streak
// is 0 and no anomaly is emitted.
func (r *ReliabilityDetector) evalStreak(ec EvalContext, jobID int64) *Anomaly {
	runs := ec.RecentRuns
	if len(runs) > maxStreakWindow {
		runs = runs[:maxStreakWindow]
	}
	if len(runs) == 0 {
		return nil
	}

	// Count contiguous failures from index 0 (newest) forward.
	streak := 0
	for _, run := range runs {
		if run.Status == "failed" || run.ItemsFailed > 0 {
			streak++
		} else {
			break
		}
	}

	threshold := ec.Sensitivity().Streak()
	if streak < threshold {
		return nil
	}

	newestRun := runs[0]
	runID := newestRun.ID

	details := buildStreakDetails(streak)
	fp := Fingerprint("reliability", ScopeJob, jobID, "failure_streak")

	return &Anomaly{
		Fingerprint: fp,
		Detector:    "reliability",
		Severity:    SeverityCritical,
		ScopeKind:   ScopeJob,
		ScopeID:     jobID,
		Metric:      "failure_streak",
		Observed:    float64(streak),
		JobRunID:    &runID,
		Summary:     fmt.Sprintf("job has failed %d runs in a row", streak),
		Details:     details,
	}
}

// evalVerifyRegression queries the two most recent verify runs for the job
// and emits a CRITICAL anomaly when the newest is "failed" and the previous
// was "passed" — a pass→fail regression.
//
// Verify run status values in this codebase: "running", "passed", "failed",
// "cancelled". Only terminal states matter for the regression check; a
// "running" or "cancelled" newest run suppresses the signal (not enough
// information to determine a regression).
//
// When fewer than 2 completed verify runs exist, the signal is suppressed
// (insufficient data).
func (r *ReliabilityDetector) evalVerifyRegression(jobID int64) (*Anomaly, error) {
	// Fetch newest 2 verify runs (all statuses, newest first).
	vruns, err := r.d.ListVerifyRunsForJob(jobID, 2)
	if err != nil {
		return nil, err
	}
	if len(vruns) < 2 {
		// Insufficient history — suppress.
		return nil, nil
	}

	newest := vruns[0]
	previous := vruns[1]

	// Regression: newest must be "failed" and previous must be "passed".
	// Any other combination (both failing, flapping, recovering, etc.) is
	// not a clean regression and is not flagged.
	if newest.Status != "failed" || previous.Status != "passed" {
		return nil, nil
	}

	fp := Fingerprint("reliability", ScopeJob, jobID, "verify_outcome")
	details := buildVerifyDetails(newest.Status, previous.Status)

	return &Anomaly{
		Fingerprint: fp,
		Detector:    "reliability",
		Severity:    SeverityCritical,
		ScopeKind:   ScopeJob,
		ScopeID:     jobID,
		Metric:      "verify_outcome",
		Observed:    0, // no numeric metric; presence is the signal
		Summary:     "backup verification regressed: previous run passed, latest run failed",
		Details:     details,
	}, nil
}

// buildStreakDetails marshals the streak count into a JSON Details string.
// Falls back to "{}" on marshal failure (should never happen for an integer).
func buildStreakDetails(streak int) string {
	type payload struct {
		Streak int `json:"streak"`
	}
	b, err := json.Marshal(payload{Streak: streak})
	if err != nil {
		log.Printf("WARN anomaly: reliability buildStreakDetails marshal: %v", err)
		return "{}"
	}
	return string(b)
}

// buildVerifyDetails marshals the two verify run outcomes into a JSON Details
// string. Fields: newest_status (the failing run) and previous_status (the
// passing run that preceded it).
func buildVerifyDetails(newestStatus, previousStatus string) string {
	type payload struct {
		NewestStatus   string `json:"newest_status"`
		PreviousStatus string `json:"previous_status"`
	}
	b, err := json.Marshal(payload{
		NewestStatus:   newestStatus,
		PreviousStatus: previousStatus,
	})
	if err != nil {
		log.Printf("WARN anomaly: reliability buildVerifyDetails marshal: %v", err)
		return "{}"
	}
	return string(b)
}
