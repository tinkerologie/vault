package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RestoreRequest struct {
	ID                   string
	OwnerDigest          string
	RequesterName        string
	Operation            json.RawMessage
	Status               string
	CreatedAt            time.Time
	ExpiresAt            time.Time
	TerminalAt           *time.Time
	TerminalReason       string
	WithdrawalFlushAfter int64 // Unix nanoseconds, or zero after confirmed persistence.
}

var ErrRestoreRequestLimit = errors.New("restore request admission limit reached")

// AdmitRestoreRequest checks duplicates and limits in the insert statement.
// Concurrent callers using different handles must share the same allowance.
func (d *DB) AdmitRestoreRequest(ctx context.Context, request RestoreRequest, input, rawInput string, maxPending, maxRecent int, recentAfter time.Time) (RestoreRequest, bool, error) {
	now := request.CreatedAt.UnixNano()
	operation := string(request.Operation)
	result, err := d.ExecContext(ctx, `INSERT INTO restore_requests
		(id, owner_digest, requester_name, operation, created_at, expires_at)
		SELECT ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM restore_requests WHERE `+pendingRestoreSubmission+`)
		AND (SELECT COUNT(*) FROM restore_requests
			WHERE owner_digest = ? AND status = 'pending' AND expires_at > ?) < ?
		AND (SELECT COUNT(*) FROM restore_requests
			WHERE owner_digest = ? AND created_at > ?) < ?`,
		request.ID, request.OwnerDigest, request.RequesterName, operation, now, request.ExpiresAt.UnixNano(),
		request.OwnerDigest, now, input, rawInput,
		request.OwnerDigest, now, maxPending,
		request.OwnerDigest, recentAfter.UnixNano(), maxRecent)
	created, err := restoreRequestsChanged(result, err)
	if err != nil || created {
		return request, created, err
	}
	existing, err := d.PendingRestoreRequest(ctx, request.OwnerDigest, request.CreatedAt, input, rawInput)
	if errors.Is(err, ErrNotFound) {
		return RestoreRequest{}, false, ErrRestoreRequestLimit
	}
	return existing, false, err
}

// Compare the submitted input, independently of the captured evidence and
// review digest. Accept the request's original encoding for older records.
const pendingRestoreSubmission = `owner_digest = ? AND status = 'pending' AND expires_at > ?
	AND CASE WHEN json_valid(operation) THEN json_extract(operation, '$.input') END IN (?, ?)`

func (d *DB) PendingRestoreRequest(ctx context.Context, owner string, now time.Time, input, rawInput string) (RestoreRequest, error) {
	return scanRestoreRequest(d.QueryRowContext(ctx, `SELECT `+restoreRequestColumns+`
		FROM restore_requests WHERE `+pendingRestoreSubmission+`
		ORDER BY created_at DESC, id DESC LIMIT 1`, owner, now.UnixNano(), input, rawInput))
}

const restoreRequestColumns = "id, owner_digest, requester_name, operation, status, created_at, expires_at, terminal_at, terminal_reason, withdrawal_flush_after"

func (d *DB) GetRestoreRequest(ctx context.Context, id, owner string) (RestoreRequest, error) {
	query := "SELECT " + restoreRequestColumns + " FROM restore_requests WHERE id = ?"
	args := []any{id}
	if owner != "" {
		query += " AND owner_digest = ?"
		args = append(args, owner)
	}
	return scanRestoreRequest(d.QueryRowContext(ctx, query, args...))
}

func (d *DB) ListRestoreRequests(ctx context.Context, statuses []string, before string, limit int) ([]RestoreRequest, error) {
	query := "SELECT " + restoreRequestColumns + " FROM restore_requests WHERE status IN (?" + strings.Repeat(",?", len(statuses)-1) + ")"
	args := make([]any, 0, len(statuses)+2)
	for _, status := range statuses {
		args = append(args, status)
	}
	if before != "" {
		query += " AND (created_at, id) < (SELECT created_at, id FROM restore_requests WHERE id = ?)"
		args = append(args, before)
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT ?"
	rows, err := d.QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	requests := make([]RestoreRequest, 0, limit)
	for rows.Next() {
		request, err := scanRestoreRequest(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

type restoreRequestScanner interface {
	Scan(...any) error
}

func scanRestoreRequest(row restoreRequestScanner) (RestoreRequest, error) {
	var request RestoreRequest
	var operation string
	var created, expires int64
	var terminal sql.NullInt64
	err := row.Scan(&request.ID, &request.OwnerDigest, &request.RequesterName, &operation, &request.Status,
		&created, &expires, &terminal, &request.TerminalReason, &request.WithdrawalFlushAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return RestoreRequest{}, ErrNotFound
	}
	if err != nil {
		return RestoreRequest{}, err
	}
	request.Operation = json.RawMessage(operation)
	request.CreatedAt = time.Unix(0, created).UTC()
	request.ExpiresAt = time.Unix(0, expires).UTC()
	if terminal.Valid {
		t := time.Unix(0, terminal.Int64).UTC()
		request.TerminalAt = &t
	}
	return request, nil
}

// WithdrawRestoreRequest only changes an owned request that is still pending
// at the time of the attempted transition.
func (d *DB) WithdrawRestoreRequest(ctx context.Context, id, owner string, now time.Time, flushAfter int64) (bool, error) {
	result, err := d.ExecContext(ctx, `UPDATE restore_requests
		SET status = 'withdrawn', terminal_at = ?, terminal_reason = 'withdrawn_by_requester', withdrawal_flush_after = ?
		WHERE id = ? AND owner_digest = ? AND status = 'pending' AND expires_at > ?`,
		now.UnixNano(), flushAfter, id, owner, now.UnixNano())
	return restoreRequestsChanged(result, err)
}

// ReserveWithdrawalFlush advances the cooldown before a retry performs I/O.
func (d *DB) ReserveWithdrawalFlush(ctx context.Context, id, owner string, now, flushAfter int64) (bool, error) {
	return restoreRequestsChanged(d.ExecContext(ctx, `UPDATE restore_requests SET withdrawal_flush_after = ?
		WHERE id = ? AND owner_digest = ? AND status = 'withdrawn'
		AND withdrawal_flush_after > 0 AND withdrawal_flush_after <= ?`, flushAfter, id, owner, now))
}

func (d *DB) MarkWithdrawalFlushed(ctx context.Context, id string, flushAfter int64) (bool, error) {
	return restoreRequestsChanged(d.ExecContext(ctx, `UPDATE restore_requests SET withdrawal_flush_after = 0
		WHERE id = ? AND status = 'withdrawn' AND withdrawal_flush_after = ?`, id, flushAfter))
}

// ExpireRestoreRequests reconciles due rows, optionally for a single request.
func (d *DB) ExpireRestoreRequests(ctx context.Context, now time.Time, id string) (bool, error) {
	query := `UPDATE restore_requests SET status = 'expired', terminal_at = expires_at, terminal_reason = 'approval_window_elapsed'
		WHERE status = 'pending' AND expires_at <= ?`
	args := []any{now.UnixNano()}
	if id != "" {
		query += " AND id = ?"
		args = append(args, id)
	}
	result, err := d.ExecContext(ctx, query, args...)
	return restoreRequestsChanged(result, err)
}

func restoreRequestsChanged(result sql.Result, err error) (bool, error) {
	if err != nil {
		return false, fmt.Errorf("updating Restore Request: %w", err)
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

func (d *DB) HasExpiredRestoreRequests(ctx context.Context, now time.Time) (bool, error) {
	var due bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM restore_requests
		WHERE status = 'pending' AND expires_at <= ?)`, now.UnixNano()).Scan(&due)
	return due, err
}
