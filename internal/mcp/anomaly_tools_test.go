package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// setupReadOnlyTest creates an MCP server in ReadOnly mode and connects a client.
func setupReadOnlyTest(t *testing.T) (*mcp.ClientSession, *db.DB) {
	t.Helper()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	hub := ws.NewHub()
	go hub.Run()

	r := runner.New(database, hub, nil)
	srv := New(database, r, Config{Version: "test-readonly", ReadOnly: true})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		_ = srv.Server().Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "vault-test",
		Version: "1.0.0",
	}, nil)

	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	t.Cleanup(func() { session.Close() })

	return session, database
}

// seedAnomaly inserts a single open anomaly with the given suffix for uniqueness.
func seedAnomaly(t *testing.T, database *db.DB, suffix string) int64 {
	t.Helper()
	now := time.Now().UTC()
	fp := fmt.Sprintf("fp-%s", suffix)
	a := db.Anomaly{
		Fingerprint: fp,
		Detector:    "test-detector",
		Severity:    "warning",
		ScopeKind:   "job",
		ScopeID:     1,
		Metric:      "size_bytes",
		Observed:    100.0,
		Summary:     fmt.Sprintf("Test anomaly %s", suffix),
		Details:     `{"reason":"test","value":42}`,
		State:       "open",
		FirstSeenAt: now,
		LastSeenAt:  now,
	}
	inserted, err := database.InsertOpenAnomaly(a)
	if err != nil {
		t.Fatalf("seeding anomaly %s: %v", suffix, err)
	}
	if !inserted {
		t.Fatalf("anomaly %s was not inserted (duplicate?)", suffix)
	}
	// Fetch the inserted row directly by fingerprint to recover its ID.
	row, err := database.GetOpenAnomalyByFingerprint(fp)
	if err != nil {
		t.Fatalf("fetching seeded anomaly %s: %v", suffix, err)
	}
	return row.ID
}

// --- Registration tests ---

// TestAnomalyToolsRegistered verifies that the two read-only anomaly tools are
// present in both normal and ReadOnly mode, and that the write tool is present
// only in normal mode.
func TestAnomalyToolsRegistered(t *testing.T) {
	t.Parallel()

	t.Run("normal mode", func(t *testing.T) {
		t.Parallel()
		session, _ := setupTest(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		result, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		names := make(map[string]bool, len(result.Tools))
		for _, tool := range result.Tools {
			names[tool.Name] = true
		}

		for _, name := range []string{"list_anomalies", "get_anomaly", "acknowledge_anomaly"} {
			if !names[name] {
				t.Errorf("normal mode missing tool: %s", name)
			}
		}
	})

	t.Run("readonly mode", func(t *testing.T) {
		t.Parallel()
		session, _ := setupReadOnlyTest(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		result, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		names := make(map[string]bool, len(result.Tools))
		for _, tool := range result.Tools {
			names[tool.Name] = true
		}

		// Read tools must be present.
		for _, name := range []string{"list_anomalies", "get_anomaly"} {
			if !names[name] {
				t.Errorf("readonly mode missing read tool: %s", name)
			}
		}

		// Write tool must NOT be present.
		if names["acknowledge_anomaly"] {
			t.Errorf("readonly mode must NOT expose acknowledge_anomaly")
		}
	})
}

// --- list_anomalies tests ---

func TestListAnomaliesEmpty(t *testing.T) {
	t.Parallel()
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res := callTool(t, session, ctx, "list_anomalies", nil)
	var arr []any
	if err := json.Unmarshal([]byte(res), &arr); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, res)
	}
	if len(arr) != 0 {
		t.Errorf("expected 0 anomalies in fresh DB, got %d", len(arr))
	}
}

func TestListAnomaliesSummaryShape(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	seedAnomaly(t, database, "shape-01")

	res := callTool(t, session, ctx, "list_anomalies", nil)
	var arr []map[string]any
	if err := json.Unmarshal([]byte(res), &arr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(arr) != 1 {
		t.Fatalf("expected 1 anomaly, got %d", len(arr))
	}

	entry := arr[0]
	for _, key := range []string{"id", "severity", "summary", "scope_kind", "scope_id", "first_seen_at"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("compact summary missing key: %s", key)
		}
	}
	// Heavy fields must NOT be present in the compact list.
	for _, key := range []string{"fingerprint", "details", "deviation", "ack_action"} {
		if _, ok := entry[key]; ok {
			t.Errorf("compact summary must not include: %s", key)
		}
	}
}

func TestListAnomaliesFilterBySeverity(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	seedAnomaly(t, database, "sev-warning")

	// Seed a critical one manually.
	now := time.Now().UTC()
	_, err := database.InsertOpenAnomaly(db.Anomaly{
		Fingerprint: "fp-critical-unique",
		Detector:    "d",
		Severity:    "critical",
		ScopeKind:   "job",
		ScopeID:     1,
		Metric:      "m",
		Observed:    1,
		Summary:     "critical anomaly",
		Details:     "{}",
		State:       "open",
		FirstSeenAt: now,
		LastSeenAt:  now,
	})
	if err != nil {
		t.Fatalf("insert critical: %v", err)
	}

	// Filter to only warning.
	res := callTool(t, session, ctx, "list_anomalies", map[string]any{
		"severity": []any{"warning"},
	})
	var arr []map[string]any
	if err := json.Unmarshal([]byte(res), &arr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, entry := range arr {
		if entry["severity"] != "warning" {
			t.Errorf("filter returned non-warning severity: %v", entry["severity"])
		}
	}
}

func TestListAnomaliesLimitCappedAt100(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Seed 110 anomalies.
	for i := 0; i < 110; i++ {
		seedAnomaly(t, database, fmt.Sprintf("cap-%03d", i))
	}

	// Request more than the cap.
	res := callTool(t, session, ctx, "list_anomalies", map[string]any{
		"limit": float64(200),
	})
	var arr []any
	if err := json.Unmarshal([]byte(res), &arr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(arr) > 100 {
		t.Errorf("list_anomalies returned %d rows, must be ≤ 100", len(arr))
	}
}

// TestListAnomaliesPayloadUnder16KB seeds exactly 100 anomalies and asserts
// the marshaled JSON response is under 16 384 bytes.
func TestListAnomaliesPayloadUnder16KB(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for i := 0; i < 100; i++ {
		seedAnomaly(t, database, fmt.Sprintf("payload-%03d", i))
	}

	res := callTool(t, session, ctx, "list_anomalies", map[string]any{
		"limit": float64(100),
	})

	const maxBytes = 16384
	if len(res) >= maxBytes {
		t.Errorf("100-row list payload = %d bytes, must be < %d", len(res), maxBytes)
	}
}

func TestListAnomaliesInvalidSince(t *testing.T) {
	t.Parallel()
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := callToolRaw(t, session, ctx, "list_anomalies", map[string]any{
		"since": "not-a-timestamp",
	})
	if !r.IsError {
		t.Errorf("expected error for invalid 'since' value, got: %s", resultText(r))
	}
}

func TestListAnomaliesReadOnly(t *testing.T) {
	t.Parallel()
	session, database := setupReadOnlyTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	seedAnomaly(t, database, "ro-01")

	res := callTool(t, session, ctx, "list_anomalies", nil)
	var arr []any
	if err := json.Unmarshal([]byte(res), &arr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(arr) != 1 {
		t.Errorf("readonly list_anomalies: expected 1, got %d", len(arr))
	}
}

// --- get_anomaly tests ---

func TestGetAnomalyNotFound(t *testing.T) {
	t.Parallel()
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := callToolRaw(t, session, ctx, "get_anomaly", map[string]any{"id": float64(999999)})
	if !r.IsError {
		t.Errorf("expected error for missing anomaly, got: %s", resultText(r))
	}
}

func TestGetAnomalyFullRow(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := seedAnomaly(t, database, "get-full")

	res := callTool(t, session, ctx, "get_anomaly", map[string]any{"id": float64(id)})

	var data map[string]any
	if err := json.Unmarshal([]byte(res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Full row fields must be present.
	for _, key := range []string{"id", "severity", "summary", "scope_kind", "scope_id",
		"first_seen_at", "last_seen_at", "state", "details", "fingerprint"} {
		if _, ok := data[key]; !ok {
			t.Errorf("get_anomaly missing key: %s", key)
		}
	}
}

func TestGetAnomalyWithParsedDetails(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := seedAnomaly(t, database, "parsed-details")
	// seedAnomaly sets Details = `{"reason":"test","value":42}`

	res := callTool(t, session, ctx, "get_anomaly", map[string]any{"id": float64(id)})

	var data map[string]any
	if err := json.Unmarshal([]byte(res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// details_parsed must be present and be an object.
	parsed, ok := data["details_parsed"]
	if !ok {
		t.Errorf("details_parsed not present in response")
	} else if _, isMap := parsed.(map[string]any); !isMap {
		t.Errorf("details_parsed is %T, want object", parsed)
	}

	// Raw details must also be present.
	if _, ok := data["details"]; !ok {
		t.Errorf("details (raw) not present in response")
	}
}

func TestGetAnomalyNonJSONDetails(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Insert anomaly with non-JSON details.
	now := time.Now().UTC()
	_, err := database.InsertOpenAnomaly(db.Anomaly{
		Fingerprint: "fp-non-json-details",
		Detector:    "d",
		Severity:    "info",
		ScopeKind:   "job",
		ScopeID:     1,
		Metric:      "m",
		Observed:    1,
		Summary:     "non-json details",
		Details:     "plain text, not JSON",
		State:       "open",
		FirstSeenAt: now,
		LastSeenAt:  now,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	row, err := database.GetOpenAnomalyByFingerprint("fp-non-json-details")
	if err != nil {
		t.Fatalf("find anomaly: %v", err)
	}
	id := row.ID

	res := callTool(t, session, ctx, "get_anomaly", map[string]any{"id": float64(id)})
	var data map[string]any
	if err := json.Unmarshal([]byte(res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// details_parsed must NOT be present.
	if _, ok := data["details_parsed"]; ok {
		t.Errorf("details_parsed should not be present for non-JSON details")
	}

	// Raw details must be present.
	if rawDetails, ok := data["details"].(string); !ok {
		t.Errorf("details should be a string")
	} else if !strings.Contains(rawDetails, "plain text") {
		t.Errorf("details = %q, want 'plain text...'", rawDetails)
	}
}

func TestGetAnomalyReadOnly(t *testing.T) {
	t.Parallel()
	session, database := setupReadOnlyTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := seedAnomaly(t, database, "get-ro")

	res := callTool(t, session, ctx, "get_anomaly", map[string]any{"id": float64(id)})
	var data map[string]any
	if err := json.Unmarshal([]byte(res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["id"] != float64(id) {
		t.Errorf("get_anomaly id = %v, want %v", data["id"], id)
	}
}

// --- acknowledge_anomaly tests ---

func TestAcknowledgeAnomalyDismiss(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := seedAnomaly(t, database, "ack-dismiss")

	res := callTool(t, session, ctx, "acknowledge_anomaly", map[string]any{
		"id":     float64(id),
		"action": "dismiss",
		"reason": "handled manually",
	})

	var data map[string]any
	if err := json.Unmarshal([]byte(res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["acked"] != true {
		t.Errorf("acked = %v, want true", data["acked"])
	}
	if data["success"] != true {
		t.Errorf("success = %v, want true", data["success"])
	}

	// Verify the row is now acknowledged.
	a, err := database.GetAnomaly(id)
	if err != nil {
		t.Fatalf("get anomaly after ack: %v", err)
	}
	if a.State != "acknowledged" {
		t.Errorf("state = %q, want 'acknowledged'", a.State)
	}
}

func TestAcknowledgeAnomalyMarkExpected(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := seedAnomaly(t, database, "ack-expected")

	callTool(t, session, ctx, "acknowledge_anomaly", map[string]any{
		"id":     float64(id),
		"action": "mark_expected",
	})

	a, err := database.GetAnomaly(id)
	if err != nil {
		t.Fatalf("get anomaly after mark_expected: %v", err)
	}
	if a.State != "expected" {
		t.Errorf("state = %q, want 'expected'", a.State)
	}
}

// TestAcknowledgeAnomalyDefaultBy verifies that when "by" is omitted, it defaults to "mcp".
func TestAcknowledgeAnomalyDefaultBy(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := seedAnomaly(t, database, "ack-default-by")

	// Acknowledge without specifying "by".
	callTool(t, session, ctx, "acknowledge_anomaly", map[string]any{
		"id":     float64(id),
		"action": "dismiss",
	})

	a, err := database.GetAnomaly(id)
	if err != nil {
		t.Fatalf("get anomaly: %v", err)
	}
	if a.AckBy != "mcp" {
		t.Errorf("ack_by = %q, want 'mcp'", a.AckBy)
	}
}

// TestAcknowledgeAnomalyCustomBy verifies that a provided "by" value is persisted.
func TestAcknowledgeAnomalyCustomBy(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := seedAnomaly(t, database, "ack-custom-by")

	callTool(t, session, ctx, "acknowledge_anomaly", map[string]any{
		"id":     float64(id),
		"action": "dismiss",
		"by":     "claude-assistant",
	})

	a, err := database.GetAnomaly(id)
	if err != nil {
		t.Fatalf("get anomaly: %v", err)
	}
	if a.AckBy != "claude-assistant" {
		t.Errorf("ack_by = %q, want 'claude-assistant'", a.AckBy)
	}
}

// TestAcknowledgeAnomalyInvalidAction verifies that an invalid action is rejected.
func TestAcknowledgeAnomalyInvalidAction(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := seedAnomaly(t, database, "ack-invalid-action")

	r := callToolRaw(t, session, ctx, "acknowledge_anomaly", map[string]any{
		"id":     float64(id),
		"action": "explode",
	})
	if !r.IsError {
		t.Errorf("expected error for invalid action, got: %s", resultText(r))
	}
}

// TestAcknowledgeAnomalyAlreadyAcked verifies that acknowledging a non-open anomaly
// returns acked=false (not an error).
func TestAcknowledgeAnomalyAlreadyAcked(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := seedAnomaly(t, database, "ack-already")

	// First ack.
	callTool(t, session, ctx, "acknowledge_anomaly", map[string]any{
		"id":     float64(id),
		"action": "dismiss",
	})

	// Second ack (should not be an error, just acked=false).
	res := callTool(t, session, ctx, "acknowledge_anomaly", map[string]any{
		"id":     float64(id),
		"action": "dismiss",
	})
	var data map[string]any
	if err := json.Unmarshal([]byte(res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["acked"] != false {
		t.Errorf("second ack: acked = %v, want false", data["acked"])
	}
}

// TestAcknowledgeAnomalyNotAvailableInReadOnly verifies that acknowledge_anomaly
// is not listed as an available tool in ReadOnly mode.
func TestAcknowledgeAnomalyNotAvailableInReadOnly(t *testing.T) {
	t.Parallel()
	session, _ := setupReadOnlyTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range result.Tools {
		if tool.Name == "acknowledge_anomaly" {
			t.Errorf("acknowledge_anomaly must not be registered in ReadOnly mode")
		}
	}
}
