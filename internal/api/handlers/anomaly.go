package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ruaan-deysel/vault/internal/anomaly"
	"github.com/ruaan-deysel/vault/internal/db"
)

const (
	maxAckReasonLen  = 500
	defaultListLimit = 100
	maxListLimit     = 500
	defaultSinceDays = 30
	trajectoryDays   = 90
)

// anomalyAcker is a minimal interface for the ack capability so the handler
// is not tightly coupled to *anomaly.Evaluator and avoids import cycles.
// Both *anomaly.Evaluator and the db-fallback adapter satisfy this interface.
type anomalyAcker interface {
	Ack(id int64, action anomaly.AckAction, by, reason string) (bool, error)
	BulkAck(ids []int64, action anomaly.AckAction, by, reason string) (acknowledged, skipped int, err error)
}

// dbAcker is a thin shim around *db.DB that satisfies anomalyAcker when
// no evaluator is available (detection disabled or evaluator not yet set).
// It calls the DB methods directly — no WebSocket broadcast.
type dbAcker struct {
	db *db.DB
}

func (a *dbAcker) Ack(id int64, action anomaly.AckAction, by, reason string) (bool, error) {
	return a.db.AckAnomaly(id, string(action), by, reason, time.Now())
}

func (a *dbAcker) BulkAck(ids []int64, action anomaly.AckAction, by, reason string) (acknowledged, skipped int, err error) {
	return a.db.BulkAckAnomalies(ids, string(action), by, reason, time.Now())
}

// AnomalyHandler implements the six anomaly REST endpoints.
type AnomalyHandler struct {
	db    *db.DB
	acker anomalyAcker // may be nil until SetEvaluator is called
}

// NewAnomalyHandler creates an AnomalyHandler wired to the given database.
// The evaluator is initially nil and should be set via SetEvaluator once the
// anomaly.Evaluator is built in daemon.go.
func NewAnomalyHandler(database *db.DB) *AnomalyHandler {
	return &AnomalyHandler{db: database}
}

// SetEvaluator wires an ack-capable evaluator. Called from daemon.go after
// buildAnomalyEvaluator returns a non-nil evaluator. When not called (detection
// disabled), Ack and AckBulk fall back to direct DB calls via dbAcker.
//
// Invoked during synchronous daemon startup, before StartWithContext serves
// any request, so the acker field needs no mutex (matches the
// SetReplicationSyncer convention).
func (h *AnomalyHandler) SetEvaluator(ev *anomaly.Evaluator) {
	if ev != nil {
		h.acker = ev
	}
}

// resolveAcker returns the acker to use for this request. Prefers the evaluator
// (which also broadcasts WS events); falls back to the plain db layer.
func (h *AnomalyHandler) resolveAcker() anomalyAcker {
	if h.acker != nil {
		return h.acker
	}
	return &dbAcker{db: h.db}
}

// ---------------------------------------------------------------------------
// GET /api/v1/anomalies
// ---------------------------------------------------------------------------

// List lists anomalies with optional query filters and keyset pagination.
//
// Query params:
//
//	state       - repeatable or CSV; defaults to ["open"]
//	severity    - repeatable or CSV
//	scope_kind  - string
//	scope_id    - int64
//	since       - RFC3339 or relative (e.g. "-30d"); defaults to now-30d
//	limit       - int, capped at 500; defaults to 100
//	cursor      - opaque page token from a previous response
func (h *AnomalyHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// Parse state (multi-value or CSV).
	states := parseMultiParam(q["state"])
	if len(states) == 0 {
		states = []string{"open"}
	}

	// Parse severity (multi-value or CSV).
	severities := parseMultiParam(q["severity"])

	// Parse scope_kind.
	scopeKind := q.Get("scope_kind")

	// Parse scope_id.
	var scopeID *int64
	if s := q.Get("scope_id"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			respondError(w, http.StatusBadRequest, "scope_id must be an integer")
			return
		}
		scopeID = &n
	}

	// Parse since; default to now-30d.
	var since *time.Time
	if s := q.Get("since"); s != "" {
		t, err := parseSince(s)
		if err != nil {
			respondError(w, http.StatusBadRequest, "since: "+err.Error())
			return
		}
		since = &t
	} else {
		t := time.Now().Add(-defaultSinceDays * 24 * time.Hour)
		since = &t
	}

	// Parse limit; default defaultListLimit, cap maxListLimit.
	limit := defaultListLimit
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			respondError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if n > maxListLimit {
			n = maxListLimit
		}
		limit = n
	}

	// Validate the cursor up front so a malformed token maps to 400 rather
	// than surfacing as a generic 500 from db.ListAnomalies. The raw cursor
	// is still passed through to the filter below (decoded again there).
	cursor := q.Get("cursor")
	if cursor != "" {
		if _, _, err := db.DecodeAnomalyCursor(cursor); err != nil {
			respondError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
	}

	filter := db.AnomalyFilter{
		States:     states,
		Severities: severities,
		ScopeKind:  scopeKind,
		ScopeID:    scopeID,
		Since:      since,
		Limit:      limit,
		Cursor:     cursor,
	}

	rows, err := h.db.ListAnomalies(filter)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	if rows == nil {
		rows = []db.Anomaly{}
	}

	var nextCursor string
	if len(rows) == limit {
		last := rows[len(rows)-1]
		nextCursor = db.EncodeAnomalyCursor(last.LastSeenAt, last.ID)
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"anomalies":   rows,
		"next_cursor": nextCursor,
	})
}

// ---------------------------------------------------------------------------
// GET /api/v1/anomalies/{id}
// ---------------------------------------------------------------------------

// Get returns a single anomaly by ID.
func (h *AnomalyHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	a, err := h.db.GetAnomaly(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "anomaly not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, a)
}

// ---------------------------------------------------------------------------
// POST /api/v1/anomalies/{id}/ack
// ---------------------------------------------------------------------------

// Ack acknowledges a single open anomaly.
//
// Request body:
//
//	{"action": "dismiss"|"mark_expected", "reason": "...", "by": "..."}
func (h *AnomalyHandler) Ack(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
		By     string `json:"by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	action, err := validateAckAction(req.Action)
	if err != nil {
		respondError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if len(req.Reason) > maxAckReasonLen {
		respondError(w, http.StatusUnprocessableEntity, "reason must be 500 characters or fewer")
		return
	}
	by := req.By
	if by == "" {
		by = "api"
	}

	// Verify the anomaly exists first so we can distinguish 404 from 409.
	if _, err := h.db.GetAnomaly(id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "anomaly not found")
			return
		}
		respondInternalError(w, err)
		return
	}

	acked, err := h.resolveAcker().Ack(id, action, by, req.Reason)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	if !acked {
		// Row exists but is not in state='open' — already terminal.
		respondError(w, http.StatusConflict, "anomaly is already in a terminal state")
		return
	}

	// Return the updated anomaly.
	updated, err := h.db.GetAnomaly(id)
	if err != nil {
		// Anomaly was acked; best-effort fetch failure is non-fatal.
		respondJSON(w, http.StatusOK, map[string]any{"acknowledged": true})
		return
	}
	respondJSON(w, http.StatusOK, updated)
}

// ---------------------------------------------------------------------------
// POST /api/v1/anomalies/ack-bulk
// ---------------------------------------------------------------------------

// AckBulk acknowledges a batch of anomalies in a single request.
//
// Request body:
//
//	{"ids": [1,2,3], "action": "dismiss"|"mark_expected", "reason": "...", "by": "..."}
func (h *AnomalyHandler) AckBulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []int64 `json:"ids"`
		Action string  `json:"action"`
		Reason string  `json:"reason"`
		By     string  `json:"by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	action, err := validateAckAction(req.Action)
	if err != nil {
		respondError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if len(req.Reason) > maxAckReasonLen {
		respondError(w, http.StatusUnprocessableEntity, "reason must be 500 characters or fewer")
		return
	}
	by := req.By
	if by == "" {
		by = "api"
	}

	acknowledged, skipped, err := h.resolveAcker().BulkAck(req.IDs, action, by, req.Reason)
	if err != nil {
		respondInternalError(w, err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"acknowledged": acknowledged,
		"skipped":      skipped,
	})
}

// ---------------------------------------------------------------------------
// GET /api/v1/jobs/{id}/baseline
// ---------------------------------------------------------------------------

// GetBaseline returns the anomaly-detection baseline for a job.
func (h *AnomalyHandler) GetBaseline(w http.ResponseWriter, r *http.Request) {
	jobID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	baseline, err := h.db.GetJobBaseline(jobID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "no baseline computed for this job yet")
			return
		}
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, baseline)
}

// ---------------------------------------------------------------------------
// GET /api/v1/destinations/{id}/capacity-trajectory
// ---------------------------------------------------------------------------

// GetTrajectory returns capacity samples for a storage destination from the
// last 90 days so the caller can render a usage trend.
func (h *AnomalyHandler) GetTrajectory(w http.ResponseWriter, r *http.Request) {
	destID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	since := time.Now().Add(-trajectoryDays * 24 * time.Hour)
	samples, err := h.db.ListCapacitySamples(destID, since)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	if samples == nil {
		samples = []db.CapacitySample{}
	}
	respondJSON(w, http.StatusOK, map[string]any{"samples": samples})
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// parseMultiParam flattens a slice of query-param values (each of which may
// itself be a comma-separated list) into a deduplicated, trimmed string slice.
func parseMultiParam(values []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part != "" && !seen[part] {
				out = append(out, part)
				seen[part] = true
			}
		}
	}
	return out
}

// parseSince parses a since value that is either an RFC3339 timestamp or a
// relative duration in the form "-Nd" (e.g. "-30d").
func parseSince(s string) (time.Time, error) {
	// Try RFC3339 first.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	// Try relative day offset e.g. "-30d".
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "-") && strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(s[1 : len(s)-1])
		if err == nil && n >= 0 {
			return time.Now().Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	return time.Time{}, errors.New("must be RFC3339 or relative like -30d")
}

// validateAckAction validates the action string and returns the typed AckAction.
func validateAckAction(action string) (anomaly.AckAction, error) {
	switch anomaly.AckAction(action) {
	case anomaly.AckDismiss, anomaly.AckMarkExpected:
		return anomaly.AckAction(action), nil
	default:
		return "", errors.New("action must be 'dismiss' or 'mark_expected'")
	}
}
