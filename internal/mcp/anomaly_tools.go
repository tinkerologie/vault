package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ruaan-deysel/vault/internal/db"
)

// maxAnomalyLimit caps the number of rows list_anomalies returns per call,
// keeping the compact payload well under the 16 KB budget.
const maxAnomalyLimit = 100

// --- Anomaly summary type (compact list payload) ---

// anomalySummary is the compact per-row shape returned by list_anomalies.
// It intentionally omits heavy fields (fingerprint, details, deviation, etc.)
// to keep a 100-row response well under the 16 KB payload budget.
type anomalySummary struct {
	ID          int64     `json:"id"`
	Severity    string    `json:"severity"`
	Summary     string    `json:"summary"`
	ScopeKind   string    `json:"scope_kind"`
	ScopeID     int64     `json:"scope_id"`
	FirstSeenAt time.Time `json:"first_seen_at"`
}

// toSummary projects a db.Anomaly to the compact anomalySummary.
func toSummary(a db.Anomaly) anomalySummary {
	return anomalySummary{
		ID:          a.ID,
		Severity:    a.Severity,
		Summary:     a.Summary,
		ScopeKind:   a.ScopeKind,
		ScopeID:     a.ScopeID,
		FirstSeenAt: a.FirstSeenAt,
	}
}

// --- list_anomalies ---

type listAnomaliesInput struct {
	States     []string `json:"state,omitempty"`
	Severities []string `json:"severity,omitempty"`
	ScopeKind  string   `json:"scope_kind,omitempty"`
	ScopeID    *int64   `json:"scope_id,omitempty"`
	Since      *string  `json:"since,omitempty"` // RFC3339 string
	Limit      int      `json:"limit,omitempty"`
}

func (s *MCPServer) addListAnomaliesTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name: "list_anomalies",
		Description: "List detected anomalies with optional filters. " +
			"Returns a compact summary (id, severity, summary, scope_kind, scope_id, first_seen_at) " +
			"capped at 100 rows per call. " +
			"Filters: state (open|resolved|acknowledged|expected), severity (info|warning|critical), " +
			"scope_kind (job|destination), scope_id, since (RFC3339), limit (1-100).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input listAnomaliesInput) (*mcp.CallToolResult, any, error) {
		limit := input.Limit
		if limit <= 0 || limit > maxAnomalyLimit {
			limit = maxAnomalyLimit
		}

		filter := db.AnomalyFilter{
			States:     input.States,
			Severities: input.Severities,
			ScopeKind:  input.ScopeKind,
			ScopeID:    input.ScopeID,
			Limit:      limit,
		}

		if input.Since != nil && *input.Since != "" {
			t, err := time.Parse(time.RFC3339, *input.Since)
			if err != nil {
				return nil, nil, fmt.Errorf("parsing since %q: %w", *input.Since, err)
			}
			filter.Since = &t
		}

		anomalies, err := s.db.ListAnomalies(filter)
		if err != nil {
			return nil, nil, fmt.Errorf("listing anomalies: %w", err)
		}

		summaries := make([]anomalySummary, len(anomalies))
		for i, a := range anomalies {
			summaries[i] = toSummary(a)
		}

		// Use compact JSON (no indentation) to keep the payload under the 16 KB budget.
		r, _ := compactTextResult(summaries)
		return r, nil, nil
	})
}

// --- get_anomaly ---

type getAnomalyInput struct {
	ID int64 `json:"id"`
}

// anomalyDetail is the full anomaly row returned by get_anomaly.
// The Details field is emitted as a parsed JSON object when it is valid JSON,
// otherwise only the raw string (on the embedded row) is present.
type anomalyDetail struct {
	db.Anomaly
	// ParsedDetails is set when Details is valid JSON; omitted otherwise.
	ParsedDetails any `json:"details_parsed,omitempty"`
}

func (s *MCPServer) addGetAnomalyTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "get_anomaly",
		Description: "Get the full anomaly row by ID. The details field is returned both as raw string and (when valid JSON) as a parsed object under details_parsed.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input getAnomalyInput) (*mcp.CallToolResult, any, error) {
		a, err := s.db.GetAnomaly(input.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("getting anomaly %d: %w", input.ID, err)
		}

		detail := anomalyDetail{Anomaly: a}

		// Attempt to parse Details as JSON; include as parsed object when valid.
		if a.Details != "" {
			var parsed any
			if jsonErr := json.Unmarshal([]byte(a.Details), &parsed); jsonErr == nil {
				detail.ParsedDetails = parsed
			}
		}

		r, _ := textResult(detail)
		return r, nil, nil
	})
}

// --- acknowledge_anomaly (write — only registered when !ReadOnly) ---

type acknowledgeAnomalyInput struct {
	ID     int64  `json:"id"`
	Action string `json:"action"` // "dismiss" or "mark_expected"
	Reason string `json:"reason,omitempty"`
	By     string `json:"by,omitempty"` // defaults to "mcp"
}

func (s *MCPServer) addAcknowledgeAnomalyTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name: "acknowledge_anomaly",
		Description: "Acknowledge an open anomaly. " +
			"action must be 'dismiss' (marks as acknowledged) or 'mark_expected' (marks as expected). " +
			"reason is optional. by identifies the acknowledging actor and defaults to 'mcp'.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input acknowledgeAnomalyInput) (*mcp.CallToolResult, any, error) {
		if input.Action != "dismiss" && input.Action != "mark_expected" {
			return nil, nil, fmt.Errorf("invalid action %q: must be 'dismiss' or 'mark_expected'", input.Action)
		}

		by := input.By
		if by == "" {
			by = "mcp"
		}

		acked, err := s.db.AckAnomaly(input.ID, input.Action, by, input.Reason, time.Now().UTC())
		if err != nil {
			return nil, nil, fmt.Errorf("acknowledging anomaly %d: %w", input.ID, err)
		}

		r, _ := textResult(map[string]any{
			"id":      input.ID,
			"acked":   acked,
			"success": true,
		})
		return r, nil, nil
	})
}

// addAnomalyTools adds the anomaly MCP tools to the server.
// The two read tools are always registered; the write tool is only registered
// when the server is NOT in ReadOnly mode.
func (s *MCPServer) addAnomalyTools() {
	s.addListAnomaliesTool()
	s.addGetAnomalyTool()

	if !s.config.ReadOnly {
		s.addAcknowledgeAnomalyTool()
	}
}
