package runner

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/diagnostics"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/format"
	"github.com/tinkerologie/vault/internal/storage"
)

// Run-log diagnostics (#455): a per-run storage collector whose retry events
// become warn lines, one timed line per phase per item, and run totals for
// the terminal summary.

// newRunDiag returns the storage collector for one run. Each recovered
// storage failure (up to storage.DefaultDiagEventCap) becomes a warn line;
// past the cap one notice is written and only the counters move, so a
// flapping destination cannot flood the 10,000-line run log.
func (r *Runner) newRunDiag(runID int64) *storage.DiagStats {
	return storage.NewDiagStats(func(e storage.DiagEvent) {
		if e.Suppressed {
			r.runLog(runID, runLogLevelWarn,
				fmt.Sprintf("Further storage retry events suppressed (more than %d); totals are in the run summary", storage.DefaultDiagEventCap), nil)
			return
		}
		msg, data := diagEventMessage(e)
		r.runLog(runID, runLogLevelWarn, msg, data)
	}, storage.DefaultDiagEventCap)
}

// diagEventMessage renders a storage event as a run-log line. Error text is
// redacted: credentials are masked and URLs are cut to scheme and host, so
// no full remote object path or secret reaches the persistent log.
func diagEventMessage(e storage.DiagEvent) (string, map[string]any) {
	errText := redactStorageError(e.Err)
	var msg string
	switch e.Op {
	case "resume":
		msg = fmt.Sprintf("Storage stream for %s dropped (attempt %d/%d), resuming: %s", e.Object, e.Attempt, e.MaxAttempts, errText)
	case "part":
		msg = fmt.Sprintf("Download part of %s failed (attempt %d/%d), restarting the part: %s", e.Object, e.Attempt, e.MaxAttempts, errText)
	default:
		target := e.Op
		if e.Object != "" && e.Object != "." {
			target += " " + e.Object
		}
		msg = fmt.Sprintf("Transient storage error on %s (attempt %d/%d), retrying: %s", target, e.Attempt, e.MaxAttempts, errText)
	}
	return msg, map[string]any{
		"operation":    e.Op,
		"object":       e.Object,
		"attempt":      e.Attempt,
		"max_attempts": e.MaxAttempts,
		"error":        errText,
	}
}

// urlInText matches a URL in free text, capturing its scheme, optional
// userinfo, and host; the path, query, and fragment are matched to be dropped.
var urlInText = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*)://(?:([^@/\s"'<>]*)@)?([^/?#\s"'<>]+)[^\s"'<>]*`)

// redactStorageError masks credentials in storage error text and trims every
// URL to its scheme and host, so neither secrets nor remote object paths
// reach the persistent run log.
func redactStorageError(s string) string {
	s = string(diagnostics.RedactLogLines([]byte(s)))
	return urlInText.ReplaceAllStringFunc(s, func(raw string) string {
		m := urlInText.FindStringSubmatch(raw)
		host := m[3]
		if m[2] != "" {
			host = "[redacted]@" + host
		}
		return m[1] + "://" + host + "/…"
	})
}

// runPhase times one stage of one item. end writes a single line: info on
// success, error naming the stage on failure, so the run log shows both
// where time went and where an operation stopped.
type runPhase struct {
	r     *Runner
	runID int64
	item  string
	name  string
	start time.Time
}

func (r *Runner) beginPhase(runID int64, item, name string) *runPhase {
	return &runPhase{r: r, runID: runID, item: item, name: name, start: time.Now()}
}

// end logs the phase outcome. extra fields (byte counts) are merged into the
// structured data.
func (p *runPhase) end(err error, extra map[string]any) {
	elapsed := time.Since(p.start)
	data := map[string]any{
		"phase":       p.name,
		"item_name":   p.item,
		"duration_ms": elapsed.Milliseconds(),
	}
	for k, v := range extra {
		data[k] = v
	}
	if err != nil {
		data["error"] = redactStorageError(err.Error())
		p.r.runLog(p.runID, runLogLevelError,
			fmt.Sprintf("Phase %s failed for %s after %s", p.name, p.item, format.Duration(elapsed)), data)
		return
	}
	p.r.runLog(p.runID, runLogLevelInfo,
		fmt.Sprintf("Phase %s completed for %s in %s", p.name, p.item, format.Duration(elapsed)), data)
}

// newRunAdapter opens dest with the run's storage collector (from ctx)
// attached, so retries inside the storage chain are counted for this run.
func newRunAdapter(ctx context.Context, dest db.StorageDestination) (storage.Adapter, error) {
	return storage.NewAdapterWithOptions(dest.Type, dest.Config, storage.Options{
		DestLabel: dest.Name,
		Diag:      storage.DiagStatsFrom(ctx),
	})
}

// diagDelta returns the counters that moved between two snapshots, for
// per-item reporting from a run-wide collector.
func diagDelta(before, after storage.DiagSnapshot) storage.DiagSnapshot {
	return storage.DiagSnapshot{
		Retries:          after.Retries - before.Retries,
		TransientErrors:  after.TransientErrors - before.TransientErrors,
		Resumes:          after.Resumes - before.Resumes,
		PartRestarts:     after.PartRestarts - before.PartRestarts,
		BytesRead:        after.BytesRead - before.BytesRead,
		RefetchedBytes:   after.RefetchedBytes - before.RefetchedBytes,
		SuppressedEvents: after.SuppressedEvents - before.SuppressedEvents,
	}
}

// addDiagTotals appends run-level storage totals to a terminal summary. A
// clean run with nothing read adds nothing, keeping the common line short.
func addDiagTotals(message string, data map[string]any, s storage.DiagSnapshot) string {
	if s.BytesRead > 0 {
		message += ", read=" + format.Bytes(float64(s.BytesRead))
		data["bytes_read"] = s.BytesRead
	}
	if s.TransientErrors > 0 || s.Retries > 0 || s.Resumes > 0 || s.PartRestarts > 0 {
		message += fmt.Sprintf(", retries=%d, transient_errors=%d", s.Retries+s.Resumes+s.PartRestarts, s.TransientErrors)
		data["retries"] = s.Retries
		data["transient_errors"] = s.TransientErrors
		data["resumes"] = s.Resumes
		data["part_restarts"] = s.PartRestarts
		if s.RefetchedBytes > 0 {
			data["refetched_bytes"] = s.RefetchedBytes
		}
	}
	return message
}

// tallyRestoreTypes are the item types whose restores count written files
// into an engine.RestoreTally — the same types that support restoring
// selected files.
var tallyRestoreTypes = map[string]bool{"folder": true, "container": true, "plugin": true}

// restoredItemSize picks the size to report for a restored item. A selective
// restore (file paths chosen) reports the bytes the engine actually wrote —
// zero included, when the selection matched no regular files; otherwise the
// size recorded at backup time is used, as before (#334).
func restoredItemSize(t RestoreTarget, tally *engine.RestoreTally, recorded map[string]int64) (size int64, label string, known bool) {
	if len(t.FilePaths) > 0 && tally != nil && tallyRestoreTypes[t.Type] {
		return tally.Bytes(), "restored", true
	}
	if size, ok := recorded[t.Name]; ok {
		return size, "recorded item size", true
	}
	return 0, "", false
}
