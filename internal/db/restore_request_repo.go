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
	ID             string
	OwnerDigest    string
	RequesterName  string
	Operation      json.RawMessage
	Status         string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	TerminalAt     *time.Time
	TerminalReason string
}

func (d *DB) CreateRestoreRequest(ctx context.Context, r RestoreRequest) error {
	_, err := d.ExecContext(ctx, `INSERT INTO restore_requests
		(id, owner_digest, requester_name, operation, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.OwnerDigest, r.RequesterName, string(r.Operation), r.CreatedAt.UnixNano(), r.ExpiresAt.UnixNano())
	return err
}

const restoreRequestColumns = "id, owner_digest, requester_name, operation, status, created_at, expires_at, terminal_at, terminal_reason"

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
		&created, &expires, &terminal, &request.TerminalReason)
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
func (d *DB) WithdrawRestoreRequest(ctx context.Context, id, owner string, now time.Time) (bool, error) {
	result, err := d.ExecContext(ctx, `UPDATE restore_requests
		SET status = 'withdrawn', terminal_at = ?, terminal_reason = 'withdrawn_by_requester'
		WHERE id = ? AND owner_digest = ? AND status = 'pending' AND expires_at > ?`,
		now.UnixNano(), id, owner, now.UnixNano())
	return restoreRequestsChanged(result, err)
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
