package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruaandeysel/vault/internal/db"
	"github.com/ruaandeysel/vault/internal/runner"
	"github.com/ruaandeysel/vault/internal/ws"
)

// setupTest creates a test MCPServer with an in-memory DB and connects a client.
func setupTest(t *testing.T) (*mcp.ClientSession, *db.DB) {
	t.Helper()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	hub := ws.NewHub()
	go hub.Run()

	r := runner.New(database, hub, nil)
	srv := New(database, r)

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

func TestListTools(t *testing.T) {
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	expected := []string{
		"list_jobs", "get_job", "create_job", "update_job", "delete_job", "run_job", "get_job_history",
		"list_storage", "get_storage", "create_storage", "update_storage", "delete_storage", "test_storage", "list_storage_files",
		"list_containers", "list_vms", "list_folders",
		"get_health", "get_health_summary", "get_activity_log",
		"list_restore_points", "restore_item",
		"list_replication", "get_replication", "delete_replication",
	}

	got := make(map[string]bool)
	for _, tool := range result.Tools {
		got[tool.Name] = true
	}

	for _, name := range expected {
		if !got[name] {
			t.Errorf("missing tool: %s", name)
		}
	}

	if len(result.Tools) != len(expected) {
		t.Errorf("tool count = %d, want %d", len(result.Tools), len(expected))
	}
}

func TestJobToolsCRUD(t *testing.T) {
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create a storage destination first (required by create_job).
	createStorageResult := callTool(t, session, ctx, "create_storage", map[string]any{
		"name":   "test-local",
		"type":   "local",
		"config": `{"path":"/tmp/vault-test"}`,
	})
	storageID := jsonField[float64](t, createStorageResult, "id")

	// create_job
	createResult := callTool(t, session, ctx, "create_job", map[string]any{
		"name":            "Test Backup",
		"description":     "A test job",
		"schedule":        "0 2 * * *",
		"storage_dest_id": storageID,
		"compression":     "zstd",
		"items": []map[string]any{
			{"item_type": "container", "item_name": "test-container", "item_id": "abc123"},
		},
	})
	jobID := jsonField[float64](t, createResult, "id")
	if jobID == 0 {
		t.Fatal("create_job returned id=0")
	}

	// get_job
	getResult := callTool(t, session, ctx, "get_job", map[string]any{"id": jobID})
	name := jsonNestedField[string](t, getResult, "job", "name")
	if name != "Test Backup" {
		t.Errorf("get_job name = %q, want %q", name, "Test Backup")
	}

	// update_job
	callTool(t, session, ctx, "update_job", map[string]any{
		"id":          jobID,
		"name":        "Updated Backup",
		"description": "Updated desc",
	})

	getResult2 := callTool(t, session, ctx, "get_job", map[string]any{"id": jobID})
	name2 := jsonNestedField[string](t, getResult2, "job", "name")
	if name2 != "Updated Backup" {
		t.Errorf("after update, name = %q, want %q", name2, "Updated Backup")
	}

	// list_jobs
	listResult := callTool(t, session, ctx, "list_jobs", nil)
	jobs := jsonArray(t, listResult)
	if len(jobs) != 1 {
		t.Errorf("list_jobs count = %d, want 1", len(jobs))
	}

	// get_job_history (should be empty)
	historyResult := callTool(t, session, ctx, "get_job_history", map[string]any{"id": jobID})
	runs := jsonArray(t, historyResult)
	if len(runs) != 0 {
		t.Errorf("get_job_history count = %d, want 0", len(runs))
	}

	// delete_job
	callTool(t, session, ctx, "delete_job", map[string]any{"id": jobID})

	listResult2 := callTool(t, session, ctx, "list_jobs", nil)
	jobs2 := jsonArray(t, listResult2)
	if len(jobs2) != 0 {
		t.Errorf("after delete, list_jobs count = %d, want 0", len(jobs2))
	}
}

func TestStorageToolsCRUD(t *testing.T) {
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// create_storage
	createResult := callTool(t, session, ctx, "create_storage", map[string]any{
		"name":   "My Local",
		"type":   "local",
		"config": `{"path":"/tmp/test-storage"}`,
	})
	storageID := jsonField[float64](t, createResult, "id")
	if storageID == 0 {
		t.Fatal("create_storage returned id=0")
	}

	// get_storage
	getResult := callTool(t, session, ctx, "get_storage", map[string]any{"id": storageID})
	name := jsonField[string](t, getResult, "name")
	if name != "My Local" {
		t.Errorf("get_storage name = %q, want %q", name, "My Local")
	}

	// update_storage
	callTool(t, session, ctx, "update_storage", map[string]any{
		"id":   storageID,
		"name": "Renamed Storage",
	})

	getResult2 := callTool(t, session, ctx, "get_storage", map[string]any{"id": storageID})
	name2 := jsonField[string](t, getResult2, "name")
	if name2 != "Renamed Storage" {
		t.Errorf("after update, name = %q, want %q", name2, "Renamed Storage")
	}

	// list_storage
	listResult := callTool(t, session, ctx, "list_storage", nil)
	items := jsonArray(t, listResult)
	if len(items) != 1 {
		t.Errorf("list_storage count = %d, want 1", len(items))
	}

	// delete_storage
	callTool(t, session, ctx, "delete_storage", map[string]any{"id": storageID})

	listResult2 := callTool(t, session, ctx, "list_storage", nil)
	items2 := jsonArray(t, listResult2)
	if len(items2) != 0 {
		t.Errorf("after delete, list_storage count = %d, want 0", len(items2))
	}
}

func TestDeleteStorageWithDependentJobs(t *testing.T) {
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create storage.
	createResult := callTool(t, session, ctx, "create_storage", map[string]any{
		"name":   "Local",
		"type":   "local",
		"config": `{"path":"/tmp/x"}`,
	})
	storageID := int64(jsonField[float64](t, createResult, "id"))

	// Create a job referencing this storage.
	_, err := database.CreateJob(db.Job{
		Name:          "Dep Job",
		StorageDestID: storageID,
	})
	if err != nil {
		t.Fatalf("creating job: %v", err)
	}

	// Attempt to delete storage - should fail.
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "delete_storage",
		Arguments: map[string]any{"id": float64(storageID)},
	})
	if err == nil && result != nil && !result.IsError {
		t.Fatal("expected error when deleting storage with dependent jobs")
	}
}

func TestHealthTools(t *testing.T) {
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// get_health
	healthResult := callTool(t, session, ctx, "get_health", nil)
	status := jsonField[string](t, healthResult, "status")
	if status != "ok" {
		t.Errorf("get_health status = %q, want %q", status, "ok")
	}

	// get_health_summary
	summaryResult := callTool(t, session, ctx, "get_health_summary", nil)
	score := jsonField[float64](t, summaryResult, "health_score")
	// With no jobs, score should be 0.
	if score != 0 {
		t.Errorf("get_health_summary health_score = %v, want 0", score)
	}
}

func TestDiscoverTools(t *testing.T) {
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// These tools should return gracefully even if Docker/libvirt aren't available.
	for _, tool := range []string{"list_containers", "list_vms", "list_folders"} {
		t.Run(tool, func(t *testing.T) {
			result := callTool(t, session, ctx, tool, nil)
			// Should have "items" and "available" keys, not an error.
			var data map[string]any
			if err := json.Unmarshal([]byte(result), &data); err != nil {
				t.Fatalf("unmarshal %s result: %v", tool, err)
			}
			if _, ok := data["items"]; !ok {
				t.Errorf("%s result missing 'items' key", tool)
			}
			if _, ok := data["available"]; !ok {
				t.Errorf("%s result missing 'available' key", tool)
			}
		})
	}
}

func TestReplicationTools(t *testing.T) {
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create a storage destination first (FK requirement).
	storageID, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "Repl Storage",
		Type:   "local",
		Config: `{"path":"/tmp/repl"}`,
	})
	if err != nil {
		t.Fatalf("creating storage: %v", err)
	}

	// Create a replication source directly in DB (create_replication not exposed via MCP).
	id, err := database.CreateReplicationSource(db.ReplicationSource{
		Name:          "Test Source",
		URL:           "http://remote:24085",
		APIKey:        "secret-key-123",
		StorageDestID: storageID,
		Schedule:      "0 * * * *",
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("creating replication source: %v", err)
	}

	// list_replication
	listResult := callTool(t, session, ctx, "list_replication", nil)
	sources := jsonArray(t, listResult)
	if len(sources) != 1 {
		t.Fatalf("list_replication count = %d, want 1", len(sources))
	}
	// Verify API key is redacted.
	src := sources[0].(map[string]any)
	if apiKey, ok := src["api_key"].(string); ok && apiKey != "\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022" {
		t.Errorf("list_replication api_key = %q, want redacted", apiKey)
	}

	// get_replication
	getResult := callTool(t, session, ctx, "get_replication", map[string]any{"id": float64(id)})
	var getData map[string]any
	if err := json.Unmarshal([]byte(getResult), &getData); err != nil {
		t.Fatalf("unmarshal get_replication: %v", err)
	}
	if source, ok := getData["source"].(map[string]any); ok {
		if apiKey, ok := source["api_key"].(string); ok && apiKey != "\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022" {
			t.Errorf("get_replication api_key = %q, want redacted", apiKey)
		}
	}

	// delete_replication
	callTool(t, session, ctx, "delete_replication", map[string]any{"id": float64(id)})

	listResult2 := callTool(t, session, ctx, "list_replication", nil)
	sources2 := jsonArray(t, listResult2)
	if len(sources2) != 0 {
		t.Errorf("after delete, list_replication count = %d, want 0", len(sources2))
	}
}

func TestActivityLog(t *testing.T) {
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := callTool(t, session, ctx, "get_activity_log", nil)
	entries := jsonArray(t, result)
	// Fresh DB should have no activity.
	if len(entries) != 0 {
		t.Errorf("get_activity_log count = %d, want 0", len(entries))
	}
}

// --- Helpers ---

// callTool calls an MCP tool and returns the text content as a string.
// It fails the test if the tool returns an error.
func callTool(t *testing.T, session *mcp.ClientSession, ctx context.Context, name string, args map[string]any) string {
	t.Helper()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if result.IsError {
		t.Fatalf("CallTool(%s) returned error: %v", name, result.Content)
	}
	if len(result.Content) == 0 {
		t.Fatalf("CallTool(%s) returned empty content", name)
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("CallTool(%s) content is not TextContent, got %T", name, result.Content[0])
	}
	return tc.Text
}

// jsonField extracts a top-level field from a JSON string.
func jsonField[T any](t *testing.T, jsonStr string, key string) T {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		var zero T
		t.Fatalf("unmarshal: %v", err)
		return zero
	}
	val, ok := data[key]
	if !ok {
		var zero T
		t.Fatalf("key %q not found in %s", key, jsonStr)
		return zero
	}
	typed, ok := val.(T)
	if !ok {
		var zero T
		t.Fatalf("key %q is %T, want %T", key, val, zero)
		return zero
	}
	return typed
}

// jsonNestedField extracts a nested field from a JSON string (e.g., "job" -> "name").
func jsonNestedField[T any](t *testing.T, jsonStr string, outerKey string, innerKey string) T {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		var zero T
		t.Fatalf("unmarshal: %v", err)
		return zero
	}
	outer, ok := data[outerKey].(map[string]any)
	if !ok {
		var zero T
		t.Fatalf("key %q not found or not object in %s", outerKey, jsonStr)
		return zero
	}
	val, ok := outer[innerKey]
	if !ok {
		var zero T
		t.Fatalf("key %q.%q not found in %s", outerKey, innerKey, jsonStr)
		return zero
	}
	typed, ok := val.(T)
	if !ok {
		var zero T
		t.Fatalf("key %q.%q is %T, want %T", outerKey, innerKey, val, zero)
		return zero
	}
	return typed
}

// jsonArray parses a JSON array string.
func jsonArray(t *testing.T, jsonStr string) []any {
	t.Helper()
	var arr []any
	if err := json.Unmarshal([]byte(jsonStr), &arr); err != nil {
		t.Fatalf("unmarshal array: %v (raw: %s)", err, jsonStr)
	}
	return arr
}
