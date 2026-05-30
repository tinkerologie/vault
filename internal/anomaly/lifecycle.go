package anomaly

import (
	"fmt"
	"log"
)

// persist saves a newly-detected anomaly, handling the insert-or-refresh path
// with optional escalation detection and activity-log/WebSocket side effects.
//
// State machine:
//   - Fresh fingerprint: INSERT → broadcast "anomaly.raised" + logActivity + maybeNotify.
//   - Already open, same or lower severity: UPDATE (refresh observed/deviation/lastSeen)
//     → broadcast "anomaly.updated" + logActivity.
//   - Already open, escalated to critical: same UPDATE path + maybeNotify if not yet notified.
func (e *Evaluator) persist(a Anomaly) {
	now := e.clock.Now()

	a.State = StateOpen
	if a.FirstSeenAt.IsZero() {
		a.FirstSeenAt = now
	}
	a.LastSeenAt = now

	inserted, err := e.db.InsertOpenAnomaly(ToDB(a))
	if err != nil {
		log.Printf("WARN anomaly: persist %q: insert failed: %v", a.Fingerprint, err)
		return
	}

	if inserted {
		// Fetch back to obtain the auto-assigned primary key ID.
		row, fetchErr := e.db.GetOpenAnomalyByFingerprint(a.Fingerprint)
		if fetchErr != nil {
			// Non-fatal: broadcast with data we have (ID will be 0).
			log.Printf("WARN anomaly: persist fetch after insert %q: %v", a.Fingerprint, fetchErr)
			e.broadcastAnomaly("anomaly.raised", a)
			e.logActivity(a, "raised")
			e.maybeNotify(a, false)
			return
		}
		raised := FromDB(row)
		e.broadcastAnomaly("anomaly.raised", raised)
		e.logActivity(raised, "raised")
		e.maybeNotify(raised, false)
		return
	}

	// Already open — refresh and possibly escalate.
	existing, err := e.db.GetOpenAnomalyByFingerprint(a.Fingerprint)
	if err != nil {
		log.Printf("WARN anomaly: persist fetch existing %q: %v", a.Fingerprint, err)
		return
	}

	escalated := a.Severity == SeverityCritical && Severity(existing.Severity) != SeverityCritical

	if err := e.db.RefreshOpenAnomaly(existing.ID, a.Observed, a.Deviation, a.LastSeenAt, string(a.Severity)); err != nil {
		log.Printf("WARN anomaly: persist refresh %d (%q): %v", existing.ID, a.Fingerprint, err)
		return
	}

	// Construct the updated view for event payloads.
	updated := FromDB(existing)
	updated.Observed = a.Observed
	updated.Deviation = a.Deviation
	updated.LastSeenAt = a.LastSeenAt
	updated.Severity = a.Severity

	e.broadcastAnomaly("anomaly.updated", updated)
	e.logActivity(updated, "refreshed")

	if escalated && existing.NotifiedAt == nil {
		e.maybeNotify(updated, true)
	}
}

// resolveSoftAnomalies flips open info/warning anomalies for the given run to
// state='resolved'. Critical anomalies are never auto-resolved; they require
// explicit acknowledgement. This is called after all detectors have run so
// that a clean run clears stale soft signals.
func (e *Evaluator) resolveSoftAnomalies(runID int64) {
	n, err := e.db.ResolveOpenAnomaliesForRun(
		runID,
		[]string{string(SeverityInfo), string(SeverityWarning)},
		e.clock.Now(),
	)
	if err != nil {
		log.Printf("WARN anomaly: resolveSoftAnomalies(run %d): %v", runID, err)
		return
	}
	if n > 0 {
		log.Printf("INFO anomaly: resolved %d soft anomaly(s) for run %d", n, runID)
		e.broadcastData("anomaly.bulk_resolved", map[string]any{
			"run_id": runID,
			"count":  n,
		})
	}
}

// Ack acknowledges a single open anomaly by ID. The action determines the
// resulting state: "mark_expected" → state='expected'; anything else (e.g.
// "dismiss") → state='acknowledged'.
//
// Returns (true, nil) when the row was successfully transitioned; (false, nil)
// when the row was already in a terminal state (idempotent, not an error).
func (e *Evaluator) Ack(id int64, action AckAction, by, reason string) (bool, error) {
	acked, err := e.db.AckAnomaly(id, string(action), by, reason, e.clock.Now())
	if err != nil {
		return false, fmt.Errorf("anomaly.Ack %d: %w", id, err)
	}
	if !acked {
		return false, nil
	}

	// Fetch the updated row to build accurate event payload.
	row, fetchErr := e.db.GetAnomaly(id)
	if fetchErr != nil {
		// Non-fatal: we still report acked=true.
		log.Printf("WARN anomaly: Ack fetch %d: %v", id, fetchErr)
		return true, nil
	}
	updated := FromDB(row)
	e.broadcastAnomaly("anomaly.updated", updated)
	e.logActivity(updated, "acked")
	return true, nil
}

// BulkAck acknowledges multiple open anomalies in a single call. Each ID is
// processed independently — there is no all-or-nothing transaction.
//
// Returns the count of rows that were transitioned (acknowledged) and the
// count that were already in a terminal state (skipped). A granular per-row
// broadcast is intentionally omitted; callers should trigger a list refresh.
func (e *Evaluator) BulkAck(ids []int64, action AckAction, by, reason string) (acknowledged, skipped int, err error) {
	acknowledged, skipped, err = e.db.BulkAckAnomalies(ids, string(action), by, reason, e.clock.Now())
	if err != nil {
		return acknowledged, skipped, fmt.Errorf("anomaly.BulkAck: %w", err)
	}
	// Broadcast a lightweight summary event so the UI can refresh the list,
	// without emitting one message per row. Also write a single audit row so
	// bulk transitions are reflected in the activity log.
	if acknowledged > 0 {
		e.broadcastData("anomaly.bulk_acked", map[string]any{
			"acknowledged": acknowledged,
			"skipped":      skipped,
			"ids":          ids,
		})
		e.db.LogActivity("info", "anomaly",
			fmt.Sprintf("%d anomaly(s) acknowledged via bulk action by %s", acknowledged, by), "")
	}
	return acknowledged, skipped, nil
}

// logActivity writes a single activity_log row for an anomaly lifecycle
// transition. The category is always "anomaly". Level is "warn" for critical
// severity, "info" otherwise.
func (e *Evaluator) logActivity(a Anomaly, transition string) {
	level := "info"
	if a.Severity == SeverityCritical {
		level = "warn"
	}
	msg := fmt.Sprintf("Anomaly %s: [%s/%s] %s (id=%d)", transition, a.Severity, a.ScopeKind, a.Summary, a.ID)
	e.db.LogActivity(level, "anomaly", msg, a.Details)
}

// maybeNotify is implemented in notify.go (Task 16).
// The stub below is intentionally removed; the real implementation lives in
// (*Evaluator).maybeNotify defined in notify.go and handles severity gating,
// dedup via notified_at, per-job overrides, and Unraid + Discord dispatch.
