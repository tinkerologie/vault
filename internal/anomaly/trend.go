package anomaly

import (
	"log"
	"time"
)

// anomalyRetentionDays is the default retention window for terminal-state
// (resolved/acknowledged/expected) anomalies when PruneOldAnomalies is called.
// Declared here so the daemon wiring and tests share the same constant.
const anomalyRetentionDays = 90

// EvaluateTrendDetectors runs all KindTrend detectors against every configured
// storage destination. It is called by the daemon's 5-minute ticker goroutine.
//
// For each destination, it:
//  1. Loads the most recent 90 days of capacity samples.
//  2. Builds a minimal EvalContext with the destination and capacity data.
//  3. Runs every detector returned by reg.Trend() through the existing
//     panic-recovering runDetector so a broken trend detector cannot prevent
//     the others from running.
//
// Errors loading destinations are logged and abort the entire pass (no
// destinations to evaluate). Per-destination errors are logged and skipped
// so one bad destination doesn't stop the rest.
func (e *Evaluator) EvaluateTrendDetectors() {
	dests, err := e.db.ListStorageDestinations()
	if err != nil {
		log.Printf("WARN anomaly: EvaluateTrendDetectors: ListStorageDestinations: %v", err)
		return
	}

	detectors := e.reg.Trend()
	if len(detectors) == 0 {
		return
	}

	sensitivity, err := e.db.GetSetting("anomaly_sensitivity_default", "balanced")
	if err != nil {
		sensitivity = "balanced"
	}

	since := e.clock.Now().AddDate(0, 0, -anomalyRetentionDays)

	floorLookup := e.floorLookupFunc()

	for _, dest := range dests {
		samples, err := e.db.ListCapacitySamples(dest.ID, since)
		if err != nil {
			log.Printf("WARN anomaly: EvaluateTrendDetectors: ListCapacitySamples(dest %d): %v", dest.ID, err)
			continue
		}

		ec := EvalContext{
			JobRun:            nil,
			Job:               nil,
			Destination:       &dest,
			RecentRuns:        nil,
			Baseline:          nil,
			CapacitySamples:   samples,
			GlobalSensitivity: sensitivity,
			Clock:             e.clock,
			floorLookup:       floorLookup,
		}

		for _, det := range detectors {
			e.runDetector(det, ec)
		}
	}

	log.Printf("INFO anomaly: EvaluateTrendDetectors: evaluated %d destination(s) with %d trend detector(s)",
		len(dests), len(detectors))
}

// PruneOldAnomalies deletes terminal-state anomalies older than retentionDays.
// Logs the count pruned if > 0; errors are logged and not propagated.
// Called by the daemon's trend ticker goroutine on each 5-minute tick.
func (e *Evaluator) PruneOldAnomalies() {
	cutoff := e.clock.Now().AddDate(0, 0, -anomalyRetentionDays)
	n, err := e.db.PruneOldAnomalies(cutoff)
	if err != nil {
		log.Printf("WARN anomaly: PruneOldAnomalies: %v", err)
		return
	}
	if n > 0 {
		log.Printf("INFO anomaly: pruned %d old terminal anomaly(s) older than %s",
			n, cutoff.Format(time.DateOnly))
	}
}
