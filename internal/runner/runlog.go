package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/format"
)

// Run-log levels. Stored as TEXT in run_log_entries.
const (
	runLogLevelInfo  = "info"
	runLogLevelWarn  = "warn"
	runLogLevelError = "error"
)

// runLog appends one entry to a run's streaming log (issue #328): the
// entry is persisted to run_log_entries and broadcast to WebSocket clients
// as a "run_log" event so open UIs append the line in real time. data may
// be nil; when present it is marshalled to JSON and stored alongside the
// human-readable message.
//
// Uses the reliable broadcast path (not BroadcastLossy): every line is
// unique information emitted at per-item frequency, so volume is low —
// dropping one would leave a visible gap in the stream.
//
// Failures are non-fatal by design: logging must never take a run down,
// matching logActivity's fire-and-forget behaviour.
//
// The append deliberately uses a detached context rather than the run's
// context: terminal entries (end-of-run summary, stall warnings, panic
// recovery) are written exactly when the run's context is being or has
// been cancelled, and those are the lines an operator needs most. It is
// still bounded: with SetMaxOpenConns(1) a pool wait is not covered by
// SQLite's busy_timeout, so an unbounded context could stall the run
// goroutine forever on a stuck pool acquisition. 30s matches the
// busy_timeout scale; a timed-out append is logged and dropped, never
// fatal.
func (r *Runner) runLog(runID int64, level, message string, data map[string]any) {
	if runID == 0 || r.db == nil {
		return
	}
	dataJSON := ""
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			log.Printf("runner: run %d: marshal run-log data: %v", runID, err)
		} else {
			dataJSON = string(raw)
		}
	}
	entry := db.RunLogEntry{
		RunID:   runID,
		Level:   level,
		Message: message,
		Data:    dataJSON,
	}
	appendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, err := r.db.AppendRunLog(appendCtx, entry)
	if err != nil {
		log.Printf("runner: run %d: append run log: %v", runID, err)
		return
	}
	entry.ID = id
	entry.Ts = time.Now().UTC()
	r.broadcast(map[string]any{
		"type":  "run_log",
		"entry": entry,
	})
}

// mirrorEngineMilestone mirrors an engine progress milestone into the run
// log for the item types whose phases operators expect narrated — folder
// (incl. the Flash Drive preset) and container (issue #328 QA). Engine
// progress otherwise feeds only the WebSocket overlay; without this, a
// container backup's run log holds just the generic runner lines.
//
// pct < 0 marks per-file heartbeats from the chunked walks ("chunked <rel>",
// "restored <fp>") and is dropped: those fire once per file and would flood
// the log. runID 0 (tests, unpersisted runs) no-ops inside runLog.
func (r *Runner) mirrorEngineMilestone(runID int64, itemType, name string, pct int, msg string) {
	if pct < 0 {
		return
	}
	if itemType != "folder" && itemType != "container" {
		return
	}
	r.runLog(runID, runLogLevelInfo, name+": "+msg, nil)
}

// runSummaryMessage builds the terminal log line, level, and structured
// data for a finished run. Extracted so the wording and percentages are
// unit-testable and shared verbatim by the backup and restore paths.
func runSummaryMessage(kind, jobName, status string, done, failed, total int, sizeBytes int64, duration time.Duration) (level string, message string, data map[string]any) {
	level = runLogLevelInfo
	switch status {
	case "failed":
		level = runLogLevelError
	case "partial", "cancelled":
		level = runLogLevelWarn
	}
	pct := 0.0
	if total > 0 {
		pct = float64(done) / float64(total) * 100
	}
	// Include the job name on the summary line so the terminal "finished" log
	// carries the same job identity as the "started" line (#328 r9). Restore
	// summaries pass an empty jobName and keep the job-less wording.
	if jobName != "" {
		message = fmt.Sprintf(
			"%s finished, job=%q, status=%s, items=%d/%d, failed=%d, size=%s, duration=%s",
			kind, jobName, status, done, total, failed,
			format.Bytes(float64(sizeBytes)), duration.Truncate(time.Second),
		)
	} else {
		message = fmt.Sprintf(
			"%s finished, status=%s, items=%d/%d, failed=%d, size=%s, duration=%s",
			kind, status, done, total, failed,
			format.Bytes(float64(sizeBytes)), duration.Truncate(time.Second),
		)
	}
	data = map[string]any{
		"status":           status,
		"items_done":       done,
		"items_failed":     failed,
		"items_total":      total,
		"size_bytes":       sizeBytes,
		"duration_seconds": int(duration.Seconds()),
		"percent_success":  pct,
	}
	if jobName != "" {
		data["job_name"] = jobName
	}
	return level, message, data
}
