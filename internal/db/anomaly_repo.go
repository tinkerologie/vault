package db

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// anomalyColumns is the ordered column list used in every anomaly SELECT.
// Keeping it in one place prevents scan-order bugs when columns are added.
const anomalyColumns = `id, fingerprint, detector, severity, scope_kind, scope_id,
	metric, observed, expected, deviation, job_run_id,
	summary, details, state, first_seen_at, last_seen_at,
	resolved_at, acknowledged_at,
	COALESCE(ack_action,''), COALESCE(ack_by,''), COALESCE(ack_reason,''),
	notified_at`

// scanAnomaly scans a row (or rows.Scan) into an Anomaly value.
// Nullable pointer fields use sql.Null* intermediates for clean NULL handling.
func scanAnomaly(scan func(...any) error) (Anomaly, error) {
	var a Anomaly
	var expected sql.NullFloat64
	var deviation sql.NullFloat64
	var jobRunID sql.NullInt64
	var resolvedAt sql.NullTime
	var acknowledgedAt sql.NullTime
	var notifiedAt sql.NullTime

	err := scan(
		&a.ID, &a.Fingerprint, &a.Detector, &a.Severity, &a.ScopeKind, &a.ScopeID,
		&a.Metric, &a.Observed, &expected, &deviation, &jobRunID,
		&a.Summary, &a.Details, &a.State, &a.FirstSeenAt, &a.LastSeenAt,
		&resolvedAt, &acknowledgedAt,
		&a.AckAction, &a.AckBy, &a.AckReason,
		&notifiedAt,
	)
	if err != nil {
		return a, err
	}
	if expected.Valid {
		v := expected.Float64
		a.Expected = &v
	}
	if deviation.Valid {
		v := deviation.Float64
		a.Deviation = &v
	}
	if jobRunID.Valid {
		v := jobRunID.Int64
		a.JobRunID = &v
	}
	if resolvedAt.Valid {
		v := resolvedAt.Time
		a.ResolvedAt = &v
	}
	if acknowledgedAt.Valid {
		v := acknowledgedAt.Time
		a.AcknowledgedAt = &v
	}
	if notifiedAt.Valid {
		v := notifiedAt.Time
		a.NotifiedAt = &v
	}
	return a, nil
}

// InsertOpenAnomaly inserts a new open anomaly row.
// The partial unique index (fingerprint WHERE state='open') prevents
// duplicate open rows for the same fingerprint — a conflict returns
// inserted=false with no error.
func (d *DB) InsertOpenAnomaly(a Anomaly) (inserted bool, err error) {
	res, err := d.Exec(
		`INSERT INTO anomalies
			(fingerprint, detector, severity, scope_kind, scope_id, metric,
			 observed, expected, deviation, job_run_id,
			 summary, details, state, first_seen_at, last_seen_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(fingerprint) WHERE state='open' DO NOTHING`,
		a.Fingerprint, a.Detector, a.Severity, a.ScopeKind, a.ScopeID, a.Metric,
		a.Observed, a.Expected, a.Deviation, a.JobRunID,
		a.Summary, a.Details, a.State, a.FirstSeenAt.UTC(), a.LastSeenAt.UTC(),
	)
	if err != nil {
		return false, fmt.Errorf("insert open anomaly: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("insert open anomaly rows affected: %w", err)
	}
	return n == 1, nil
}

// RefreshOpenAnomaly updates the mutable observation fields on an open
// anomaly row. Used when the detector fires on the same fingerprint again
// before the previous open row has been resolved.
func (d *DB) RefreshOpenAnomaly(id int64, observed float64, deviation *float64, lastSeen time.Time, severity string) error {
	_, err := d.Exec(
		`UPDATE anomalies
		 SET observed=?, deviation=?, last_seen_at=?, severity=?
		 WHERE id=? AND state='open'`,
		observed, deviation, lastSeen.UTC(), severity, id,
	)
	if err != nil {
		return fmt.Errorf("refresh open anomaly %d: %w", id, err)
	}
	return nil
}

// GetOpenAnomalyByFingerprint fetches the single open row for a fingerprint.
// Returns ErrNotFound if no open anomaly exists for that fingerprint.
func (d *DB) GetOpenAnomalyByFingerprint(fingerprint string) (Anomaly, error) {
	row := d.QueryRow(
		`SELECT `+anomalyColumns+`
		 FROM anomalies
		 WHERE fingerprint=? AND state='open'`, fingerprint,
	)
	a, err := scanAnomaly(row.Scan)
	if err == sql.ErrNoRows {
		return a, ErrNotFound
	}
	if err != nil {
		return a, fmt.Errorf("get open anomaly by fingerprint: %w", err)
	}
	return a, nil
}

// ResolveOpenAnomaliesForRun flips open anomaly rows whose job_run_id
// matches runID and whose severity is in severitiesToResolve to
// state='resolved', stamping resolved_at. Returns the count of rows updated.
// Used to auto-resolve soft anomalies (e.g. info/warning) after a clean run.
func (d *DB) ResolveOpenAnomaliesForRun(runID int64, severitiesToResolve []string, resolvedAt time.Time) (int64, error) {
	if len(severitiesToResolve) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(severitiesToResolve))
	args := make([]any, 0, len(severitiesToResolve)+3)
	args = append(args, resolvedAt.UTC())
	for i, s := range severitiesToResolve {
		placeholders[i] = "?"
		args = append(args, s)
	}
	args = append(args, runID)

	res, err := d.Exec(
		`UPDATE anomalies
		 SET state='resolved', resolved_at=?
		 WHERE state='open'
		   AND severity IN (`+strings.Join(placeholders, ",")+`)
		   AND job_run_id=?`,
		args...,
	)
	if err != nil {
		return 0, fmt.Errorf("resolve open anomalies for run %d: %w", runID, err)
	}
	return res.RowsAffected()
}

// AckAnomaly acknowledges an open anomaly row. The state transition depends
// on the action:
//   - "mark_expected" → state='expected'
//   - anything else (e.g. "dismiss") → state='acknowledged'
//
// Returns acked=false (with no error) when the row is not in state='open',
// so callers can distinguish "already terminal" from a real error.
func (d *DB) AckAnomaly(id int64, action, by, reason string, ackedAt time.Time) (bool, error) {
	newState := "acknowledged"
	if action == "mark_expected" {
		newState = "expected"
	}
	res, err := d.Exec(
		`UPDATE anomalies
		 SET acknowledged_at=?, ack_action=?, ack_by=?, ack_reason=?, state=?
		 WHERE id=? AND state='open'`,
		ackedAt.UTC(), action, by, reason, newState, id,
	)
	if err != nil {
		return false, fmt.Errorf("ack anomaly %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("ack anomaly %d rows affected: %w", id, err)
	}
	return n == 1, nil
}

// BulkAckAnomalies calls AckAnomaly for each ID in the slice and tallies
// acknowledged vs skipped (rows already in a terminal state). Each ID is
// acted on independently — there is no all-or-nothing transaction.
func (d *DB) BulkAckAnomalies(ids []int64, action, by, reason string, ackedAt time.Time) (acknowledged, skipped int, err error) {
	for _, id := range ids {
		acked, aerr := d.AckAnomaly(id, action, by, reason, ackedAt)
		if aerr != nil {
			return acknowledged, skipped, aerr
		}
		if acked {
			acknowledged++
		} else {
			skipped++
		}
	}
	return acknowledged, skipped, nil
}

// AnomalyFilter controls which rows ListAnomalies returns. Zero values are
// ignored (no filtering on that dimension). Limit defaults to 50 when <= 0.
// Cursor is an opaque token from EncodeAnomalyCursor / a previous page.
type AnomalyFilter struct {
	States     []string
	Severities []string
	ScopeKind  string
	ScopeID    *int64
	Since      *time.Time
	Limit      int
	Cursor     string // opaque; encodes last_seen_at_unix:id
}

// EncodeAnomalyCursor encodes the pagination cursor from the last row of a
// page. The cursor is a colon-separated pair of unix timestamp + row ID.
func EncodeAnomalyCursor(lastSeenAt time.Time, id int64) string {
	return strconv.FormatInt(lastSeenAt.UTC().Unix(), 10) + ":" + strconv.FormatInt(id, 10)
}

// DecodeAnomalyCursor decodes a cursor produced by EncodeAnomalyCursor.
// Returns an error if the cursor is malformed.
func DecodeAnomalyCursor(cursor string) (lastSeenAt time.Time, id int64, err error) {
	parts := strings.SplitN(cursor, ":", 2)
	if len(parts) != 2 {
		return lastSeenAt, 0, fmt.Errorf("malformed anomaly cursor %q", cursor)
	}
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return lastSeenAt, 0, fmt.Errorf("malformed anomaly cursor timestamp: %w", err)
	}
	id, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return lastSeenAt, 0, fmt.Errorf("malformed anomaly cursor id: %w", err)
	}
	return time.Unix(ts, 0).UTC(), id, nil
}

// ListAnomalies returns anomaly rows matching filter, ordered by
// last_seen_at DESC, id DESC. Keyset pagination via Cursor keeps page
// fetches efficient on large tables.
func (d *DB) ListAnomalies(filter AnomalyFilter) ([]Anomaly, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}

	var conditions []string
	var args []any

	// Cursor-based pagination: rows strictly before (lastSeenAt, id).
	// SQLite supports row-value comparison: (a, b) < (x, y) ≡ a<x OR (a=x AND b<y).
	if filter.Cursor != "" {
		cursorTime, cursorID, err := DecodeAnomalyCursor(filter.Cursor)
		if err != nil {
			return nil, fmt.Errorf("list anomalies: %w", err)
		}
		conditions = append(conditions,
			"(last_seen_at < ? OR (last_seen_at = ? AND id < ?))")
		args = append(args, cursorTime.UTC(), cursorTime.UTC(), cursorID)
	}

	if len(filter.States) > 0 {
		ph := make([]string, len(filter.States))
		for i, s := range filter.States {
			ph[i] = "?"
			args = append(args, s)
		}
		conditions = append(conditions, "state IN ("+strings.Join(ph, ",")+")")
	}

	if len(filter.Severities) > 0 {
		ph := make([]string, len(filter.Severities))
		for i, s := range filter.Severities {
			ph[i] = "?"
			args = append(args, s)
		}
		conditions = append(conditions, "severity IN ("+strings.Join(ph, ",")+")")
	}

	if filter.ScopeKind != "" {
		conditions = append(conditions, "scope_kind=?")
		args = append(args, filter.ScopeKind)
	}

	if filter.ScopeID != nil {
		conditions = append(conditions, "scope_id=?")
		args = append(args, *filter.ScopeID)
	}

	if filter.Since != nil {
		conditions = append(conditions, "last_seen_at >= ?")
		args = append(args, filter.Since.UTC())
	}

	query := `SELECT ` + anomalyColumns + ` FROM anomalies`
	if len(conditions) > 0 {
		// aikido-ignore-next-line AIK_go_G202 -- conditions are constant column predicates (e.g. "scope_id=?"); every filter value is bound via args and is never interpolated into the SQL string.
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY last_seen_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list anomalies: %w", err)
	}
	defer rows.Close()

	var anomalies []Anomaly
	for rows.Next() {
		a, err := scanAnomaly(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("list anomalies scan: %w", err)
		}
		anomalies = append(anomalies, a)
	}
	return anomalies, rows.Err()
}

// GetAnomaly returns a single anomaly row by primary key.
// Returns ErrNotFound when no row matches.
func (d *DB) GetAnomaly(id int64) (Anomaly, error) {
	row := d.QueryRow(
		`SELECT `+anomalyColumns+` FROM anomalies WHERE id=?`, id,
	)
	a, err := scanAnomaly(row.Scan)
	if err == sql.ErrNoRows {
		return a, ErrNotFound
	}
	if err != nil {
		return a, fmt.Errorf("get anomaly %d: %w", id, err)
	}
	return a, nil
}

// ExpectedFloor returns MAX(observed) across all rows for fingerprint that
// are in state='expected'. Returns 0 when no such rows exist (COALESCE).
// The detector uses this to set a minimum threshold below which re-occurrence
// of an acknowledged anomaly is not re-raised.
func (d *DB) ExpectedFloor(fingerprint string) (float64, error) {
	var v float64
	err := d.QueryRow(
		`SELECT COALESCE(MAX(observed), 0) FROM anomalies WHERE fingerprint=? AND state='expected'`,
		fingerprint,
	).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("expected floor for %q: %w", fingerprint, err)
	}
	return v, nil
}

// MarkAnomalyNotified stamps notified_at on a row. Called after the
// notification subsystem successfully dispatches an alert.
func (d *DB) MarkAnomalyNotified(id int64, ts time.Time) error {
	_, err := d.Exec(
		`UPDATE anomalies SET notified_at=? WHERE id=?`,
		ts.UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("mark anomaly %d notified: %w", id, err)
	}
	return nil
}

// UpsertJobBaseline inserts or replaces the baseline statistics for a job.
// On conflict (job already has a baseline row) all fields are updated.
func (d *DB) UpsertJobBaseline(b JobBaseline) error {
	_, err := d.Exec(
		`INSERT INTO job_baselines
			(job_id, sample_count, bytes_median, bytes_mad, duration_median, duration_mad, failure_rate, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(job_id) DO UPDATE SET
		 	sample_count=excluded.sample_count,
		 	bytes_median=excluded.bytes_median,
		 	bytes_mad=excluded.bytes_mad,
		 	duration_median=excluded.duration_median,
		 	duration_mad=excluded.duration_mad,
		 	failure_rate=excluded.failure_rate,
		 	updated_at=excluded.updated_at`,
		b.JobID, b.SampleCount, b.BytesMedian, b.BytesMAD,
		b.DurationMedian, b.DurationMAD, b.FailureRate, b.UpdatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("upsert job baseline for job %d: %w", b.JobID, err)
	}
	return nil
}

// GetJobBaseline returns the baseline row for a job.
// Returns ErrNotFound if no baseline has been computed yet for this job.
func (d *DB) GetJobBaseline(jobID int64) (JobBaseline, error) {
	var b JobBaseline
	err := d.QueryRow(
		`SELECT job_id, sample_count, bytes_median, bytes_mad,
		        duration_median, duration_mad, failure_rate, updated_at
		 FROM job_baselines WHERE job_id=?`, jobID,
	).Scan(&b.JobID, &b.SampleCount, &b.BytesMedian, &b.BytesMAD,
		&b.DurationMedian, &b.DurationMAD, &b.FailureRate, &b.UpdatedAt)
	if err == sql.ErrNoRows {
		return b, ErrNotFound
	}
	if err != nil {
		return b, fmt.Errorf("get job baseline %d: %w", jobID, err)
	}
	return b, nil
}

// InsertCapacitySample appends a single capacity measurement for a destination.
func (d *DB) InsertCapacitySample(s CapacitySample) error {
	_, err := d.Exec(
		`INSERT INTO destination_capacity_samples (dest_id, sampled_at, free_bytes, total_bytes)
		 VALUES (?, ?, ?, ?)`,
		s.DestID, s.SampledAt.UTC(), s.FreeBytes, s.TotalBytes,
	)
	if err != nil {
		return fmt.Errorf("insert capacity sample for dest %d: %w", s.DestID, err)
	}
	return nil
}

// ListCapacitySamples returns all capacity samples for a destination taken
// at or after since, ordered by sampled_at ASC (oldest first) so the caller
// can feed them directly into a linear regression.
func (d *DB) ListCapacitySamples(destID int64, since time.Time) ([]CapacitySample, error) {
	rows, err := d.Query(
		`SELECT id, dest_id, sampled_at, free_bytes, total_bytes
		 FROM destination_capacity_samples
		 WHERE dest_id=? AND sampled_at >= ?
		 ORDER BY sampled_at ASC`,
		destID, since.UTC(),
	)
	if err != nil {
		return nil, fmt.Errorf("list capacity samples for dest %d: %w", destID, err)
	}
	defer rows.Close()

	var samples []CapacitySample
	for rows.Next() {
		var s CapacitySample
		if err := rows.Scan(&s.ID, &s.DestID, &s.SampledAt, &s.FreeBytes, &s.TotalBytes); err != nil {
			return nil, fmt.Errorf("list capacity samples scan: %w", err)
		}
		samples = append(samples, s)
	}
	return samples, rows.Err()
}

// PruneOldAnomalies deletes terminal-state (resolved/acknowledged/expected)
// anomaly rows whose last_seen_at is before the cutoff. Open anomaly rows
// are never deleted. Returns the count of rows removed.
func (d *DB) PruneOldAnomalies(before time.Time) (int64, error) {
	res, err := d.Exec(
		`DELETE FROM anomalies
		 WHERE state IN ('resolved','acknowledged','expected')
		   AND last_seen_at < ?`,
		before.UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("prune old anomalies: %w", err)
	}
	return res.RowsAffected()
}
