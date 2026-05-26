package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruaan-deysel/vault/internal/db"
)

// callToolRaw returns the result without failing the test on IsError,
// so tests can assert on error paths.
func callToolRaw(t *testing.T, session *mcp.ClientSession, ctx context.Context, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) transport error: %v", name, err)
	}
	return result
}

// resultText concats all text content blocks for inspection.
func resultText(r *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// TestNewDefaultVersion exercises the New() branch that fills in the default Version.
func TestNewDefaultVersion(t *testing.T) {
	t.Parallel()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	defer database.Close()

	// Empty Version → default "dev"
	s := New(database, nil, Config{Version: ""})
	if s.config.Version != "dev" {
		t.Errorf("default Version = %q, want dev", s.config.Version)
	}

	// No configs at all
	s2 := New(database, nil)
	if s2.config.Version != "dev" {
		t.Errorf("no-config Version = %q, want dev", s2.config.Version)
	}

	// ReadOnly mode propagates to get_health "mode" field.
	s3 := New(database, nil, Config{Version: "v1", ReadOnly: true})
	if !s3.config.ReadOnly {
		t.Errorf("ReadOnly was lost")
	}
	if s3.Server() == nil {
		t.Error("Server() returned nil")
	}
}

// TestHTTPHandler stands up an HTTP test server backed by the MCP HTTPHandler,
// sends a malformed POST, and asserts the handler responds (does not panic
// and does not return 0/transport-level error).
func TestHTTPHandler(t *testing.T) {
	t.Parallel()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	defer database.Close()

	srv := New(database, nil, Config{Version: "http-test"})
	handler := srv.HTTPHandler()
	if handler == nil {
		t.Fatal("HTTPHandler returned nil")
	}

	ts := httptest.NewServer(handler)
	defer ts.Close()

	// Send any HTTP request — we only care that the handler is wired
	// and responds without panicking. The specific status code is an
	// implementation detail of the MCP SDK.
	req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http POST: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == 0 {
		t.Errorf("response status code is 0")
	}
}

// TestRunWithCanceledContext verifies the Run() entry point returns when the
// context is canceled before it begins blocking on stdio reads. We can't
// actually feed stdio in a test, but cancelling the context first should
// drive the early-return path.
func TestRunWithCanceledContext(t *testing.T) {
	t.Parallel()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	defer database.Close()

	srv := New(database, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	done := make(chan error, 1)
	go func() {
		done <- srv.Run(ctx)
	}()

	select {
	case <-done:
		// Run returned. Either nil or err — both acceptable.
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

// TestGetJobNotFound triggers the error path of addGetJobTool.
func TestGetJobNotFound(t *testing.T) {
	t.Parallel()
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := callToolRaw(t, session, ctx, "get_job", map[string]any{"id": float64(99999)})
	if !r.IsError {
		t.Errorf("expected error for missing job, got: %s", resultText(r))
	}
}

// TestDeleteJobNotFound triggers the error path of addDeleteJobTool.
// SQLite delete on missing row may not fail, so we ensure we exercise it
// with a valid-looking id and at least cover the code path.
func TestDeleteJobErrorPath(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Close DB so delete returns an error
	_ = database.Close()
	r := callToolRaw(t, session, ctx, "delete_job", map[string]any{"id": float64(1)})
	if !r.IsError {
		t.Errorf("expected delete_job error after DB close, got: %s", resultText(r))
	}
}

// TestDeleteStorageNotFoundError exercises the error path when CountJobsByStorageDestID
// fails (e.g., DB closed) or when the storage destination cannot be deleted.
func TestDeleteStorageDBClosed(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = database.Close()
	r := callToolRaw(t, session, ctx, "delete_storage", map[string]any{"id": float64(1)})
	if !r.IsError {
		t.Errorf("expected error after DB close, got: %s", resultText(r))
	}
}

// TestUpdateStorageNotFound exercises the GetStorageDestination error branch.
func TestUpdateStorageNotFound(t *testing.T) {
	t.Parallel()
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := callToolRaw(t, session, ctx, "update_storage", map[string]any{
		"id":   float64(9999),
		"name": "Nope",
	})
	if !r.IsError {
		t.Errorf("expected error for missing storage, got: %s", resultText(r))
	}
}

// TestUpdateStorageAllFields covers the three optional branches in
// addUpdateStorageTool simultaneously.
func TestUpdateStorageAllFields(t *testing.T) {
	t.Parallel()
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	createResult := callTool(t, session, ctx, "create_storage", map[string]any{
		"name":   "Orig",
		"type":   "local",
		"config": `{"path":"/tmp/orig"}`,
	})
	id := jsonField[float64](t, createResult, "id")

	// Hit all three optional fields at once
	r := callToolRaw(t, session, ctx, "update_storage", map[string]any{
		"id":     id,
		"name":   "NewName",
		"type":   "local",
		"config": `{"path":"/tmp/newpath"}`,
	})
	if r.IsError {
		t.Fatalf("update_storage failed: %s", resultText(r))
	}
	getResult := callTool(t, session, ctx, "get_storage", map[string]any{"id": id})
	if jsonField[string](t, getResult, "name") != "NewName" {
		t.Errorf("name not updated")
	}
}

// TestUpdateJobAllFields exercises every optional branch of addUpdateJobTool.
func TestUpdateJobAllFields(t *testing.T) {
	t.Parallel()
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	storageResult := callTool(t, session, ctx, "create_storage", map[string]any{
		"name":   "S",
		"type":   "local",
		"config": `{"path":"/tmp/upj"}`,
	})
	storageID := jsonField[float64](t, storageResult, "id")

	storageResult2 := callTool(t, session, ctx, "create_storage", map[string]any{
		"name":   "S2",
		"type":   "local",
		"config": `{"path":"/tmp/upj2"}`,
	})
	storageID2 := jsonField[float64](t, storageResult2, "id")

	createResult := callTool(t, session, ctx, "create_job", map[string]any{
		"name":            "J",
		"schedule":        "0 1 * * *",
		"storage_dest_id": storageID,
		"items": []map[string]any{
			{"item_type": "container", "item_name": "x", "item_id": "1"},
		},
	})
	jobID := jsonField[float64](t, createResult, "id")

	enabled := false
	verify := false
	retCount := 7
	retDays := 14
	r := callToolRaw(t, session, ctx, "update_job", map[string]any{
		"id":                jobID,
		"name":              "J-new",
		"description":       "desc",
		"enabled":           enabled,
		"schedule":          "0 2 * * *",
		"retention_count":   retCount,
		"retention_days":    retDays,
		"compression":       "gzip",
		"encryption":        "aes",
		"storage_dest_id":   storageID2,
		"backup_type_chain": "incremental",
		"container_mode":    "parallel",
		"vm_mode":           "all_at_once",
		"notify_on":         "always",
		"verify_backup":     verify,
	})
	if r.IsError {
		t.Fatalf("update_job failed: %s", resultText(r))
	}

	getResult := callTool(t, session, ctx, "get_job", map[string]any{"id": jobID})
	name := jsonNestedField[string](t, getResult, "job", "name")
	if name != "J-new" {
		t.Errorf("name = %q, want J-new", name)
	}
}

// TestUpdateJobNotFound triggers the GetJob error branch.
func TestUpdateJobNotFound(t *testing.T) {
	t.Parallel()
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := callToolRaw(t, session, ctx, "update_job", map[string]any{
		"id":   float64(8888),
		"name": "X",
	})
	if !r.IsError {
		t.Errorf("expected error, got: %s", resultText(r))
	}
}

// TestGetStorageNotFound triggers GetStorageDestination error.
func TestGetStorageNotFound(t *testing.T) {
	t.Parallel()
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := callToolRaw(t, session, ctx, "get_storage", map[string]any{"id": float64(7777)})
	if !r.IsError {
		t.Errorf("expected error for missing storage, got: %s", resultText(r))
	}
}

// TestGetReplicationNotFound triggers GetReplicationSource error.
func TestGetReplicationNotFound(t *testing.T) {
	t.Parallel()
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := callToolRaw(t, session, ctx, "get_replication", map[string]any{"id": float64(6666)})
	if !r.IsError {
		t.Errorf("expected error for missing replication source, got: %s", resultText(r))
	}
}

// TestTestStorageBadType covers the NewAdapter error branch of addTestStorageTool.
func TestTestStorageBadType(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "Bad",
		Type:   "nonsense-type",
		Config: `{}`,
	})
	if err != nil {
		t.Fatalf("creating storage: %v", err)
	}

	res := callTool(t, session, ctx, "test_storage", map[string]any{"id": float64(id)})
	var data map[string]any
	if jerr := json.Unmarshal([]byte(res), &data); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if data["success"] != false {
		t.Errorf("expected success=false for bad adapter type, got: %v", data)
	}
}

// TestTestStorageNotFound triggers the GetStorageDestination error.
func TestTestStorageNotFound(t *testing.T) {
	t.Parallel()
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := callToolRaw(t, session, ctx, "test_storage", map[string]any{"id": float64(4242)})
	if !r.IsError {
		t.Errorf("expected error, got: %s", resultText(r))
	}
}

// TestTestStorageLocalSuccess covers the happy "TestConnection ok" branch
// of addTestStorageTool using a real local adapter against a temp dir.
func TestTestStorageLocalSuccess(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dir := t.TempDir()
	id, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "ok",
		Type:   "local",
		Config: `{"path":"` + dir + `"}`,
	})
	if err != nil {
		t.Fatalf("creating storage: %v", err)
	}

	res := callTool(t, session, ctx, "test_storage", map[string]any{"id": float64(id)})
	var data map[string]any
	if err := json.Unmarshal([]byte(res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["success"] != true {
		t.Errorf("expected success=true, got: %v", data)
	}
}

// TestListStorageFiles covers the happy and error paths of addListStorageFilesTool.
func TestListStorageFiles(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Not-found path
	r := callToolRaw(t, session, ctx, "list_storage_files", map[string]any{"id": float64(9000)})
	if !r.IsError {
		t.Errorf("expected error for missing storage")
	}

	// Bad-adapter path
	idBad, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "bad",
		Type:   "garbage",
		Config: `{}`,
	})
	if err != nil {
		t.Fatalf("creating bad storage: %v", err)
	}
	r2 := callToolRaw(t, session, ctx, "list_storage_files", map[string]any{"id": float64(idBad)})
	if !r2.IsError {
		t.Errorf("expected error for bad adapter type")
	}

	// Happy path
	dir := t.TempDir()
	id, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "ok",
		Type:   "local",
		Config: `{"path":"` + dir + `"}`,
	})
	if err != nil {
		t.Fatalf("creating storage: %v", err)
	}
	res := callTool(t, session, ctx, "list_storage_files", map[string]any{"id": float64(id)})
	// res should be a JSON array (possibly empty)
	var arr []any
	if jerr := json.Unmarshal([]byte(res), &arr); jerr != nil {
		t.Errorf("list_storage_files did not return JSON array: %v (raw: %s)", jerr, res)
	}
}

// TestRunJobHappyPath exercises the success branch of addRunJobTool.
// It accepts that the goroutine may not complete in test, only verifying that
// the synchronous part returns "backup started".
func TestRunJobHappyPath(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "rj",
		Type:   "local",
		Config: `{"path":"` + t.TempDir() + `"}`,
	})
	if err != nil {
		t.Fatalf("creating storage: %v", err)
	}
	jobID, err := database.CreateJob(db.Job{
		Name:          "rj",
		StorageDestID: destID,
		Schedule:      "0 0 * * *",
	})
	if err != nil {
		t.Fatalf("creating job: %v", err)
	}

	res := callTool(t, session, ctx, "run_job", map[string]any{"id": float64(jobID)})
	if !strings.Contains(res, "backup started") {
		t.Errorf("run_job result = %s, want 'backup started'", res)
	}
	// Best-effort: give the goroutine a moment to start then ignore.
	time.Sleep(50 * time.Millisecond)
}

// TestRunJobNotFound triggers the GetJob error branch of addRunJobTool.
func TestRunJobNotFound(t *testing.T) {
	t.Parallel()
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := callToolRaw(t, session, ctx, "run_job", map[string]any{"id": float64(99999)})
	if !r.IsError {
		t.Errorf("expected error for missing job, got: %s", resultText(r))
	}
}

// TestRestoreItemNotFound exercises the early-return when the requested
// restore_point_id is not in the list for the given job.
func TestRestoreItemNotFound(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "r",
		Type:   "local",
		Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := database.CreateJob(db.Job{Name: "j", StorageDestID: destID})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	r := callToolRaw(t, session, ctx, "restore_item", map[string]any{
		"restore_point_id": float64(42424242),
		"job_id":           float64(jobID),
		"item_name":        "x",
		"item_type":        "container",
	})
	if !r.IsError {
		t.Errorf("expected error for missing restore point, got: %s", resultText(r))
	}
}

// TestRestoreItemFound exercises the happy-path that fires the restore goroutine.
func TestRestoreItemFound(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "r",
		Type:   "local",
		Config: `{"path":"` + t.TempDir() + `"}`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := database.CreateJob(db.Job{Name: "j", StorageDestID: destID})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	runID, err := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	rpID, err := database.CreateRestorePoint(db.RestorePoint{
		JobRunID:    runID,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "/tmp/rp",
		Metadata:    "{}",
	})
	if err != nil {
		t.Fatalf("create restore point: %v", err)
	}

	res := callTool(t, session, ctx, "restore_item", map[string]any{
		"restore_point_id": float64(rpID),
		"job_id":           float64(jobID),
		"item_name":        "x",
		"item_type":        "container",
		"destination":      "/tmp/dst",
	})
	if !strings.Contains(res, "restore started") {
		t.Errorf("restore_item result = %s, want 'restore started'", res)
	}
	// Give the goroutine a moment (it will fail because there's no actual
	// archive; the log line is best-effort).
	time.Sleep(50 * time.Millisecond)
}

// TestListRestorePointsBadJob covers the GetJob error path of
// addListRestorePointsTool.
func TestListRestorePointsBadJob(t *testing.T) {
	t.Parallel()
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := callToolRaw(t, session, ctx, "list_restore_points", map[string]any{"job_id": float64(99999)})
	if !r.IsError {
		t.Errorf("expected error for missing job, got: %s", resultText(r))
	}
}

// TestHealthSummaryWithData exercises the loop branches inside addGetHealthSummaryTool:
// totals, protected pct, success/failure mix, lastSuccessTime.
func TestHealthSummaryWithData(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "d", Type: "local", Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("dest: %v", err)
	}
	jobID, err := database.CreateJob(db.Job{Name: "j", StorageDestID: destID, Enabled: true})
	if err != nil {
		t.Fatalf("job: %v", err)
	}
	if _, err := database.AddJobItem(db.JobItem{JobID: jobID, ItemType: "container", ItemName: "x", ItemID: "1", Settings: "{}"}); err != nil {
		t.Fatalf("job item: %v", err)
	}
	if _, err := database.AddJobItem(db.JobItem{JobID: jobID, ItemType: "container", ItemName: "y", ItemID: "2", Settings: "{}"}); err != nil {
		t.Fatalf("job item: %v", err)
	}

	// Mix a successful and a failed run. CreateJobRun ignores CompletedAt,
	// so use the imported variant which preserves both started/completed.
	completed := time.Now().UTC()
	if _, err := database.CreateImportedJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"}, completed); err != nil {
		t.Fatalf("success run: %v", err)
	}
	if _, err := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "failed", BackupType: "full"}); err != nil {
		t.Fatalf("failed run: %v", err)
	}
	if _, err := database.CreateImportedJobRun(db.JobRun{JobID: jobID, Status: "completed", BackupType: "full"}, completed); err != nil {
		t.Fatalf("completed run: %v", err)
	}

	res := callTool(t, session, ctx, "get_health_summary", nil)
	var data map[string]any
	if err := json.Unmarshal([]byte(res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	total, _ := data["total_items"].(float64)
	if math.Abs(total-2) > 0.001 {
		t.Errorf("total_items = %v, want 2", total)
	}
	protected, _ := data["protected_items"].(float64)
	if math.Abs(protected-2) > 0.001 {
		t.Errorf("protected_items = %v, want 2", protected)
	}
	if _, ok := data["last_success_at"].(string); !ok {
		t.Errorf("last_success_at missing or wrong type: %T", data["last_success_at"])
	}
}

// TestTextResultMarshalError covers the err branch of textResult().
// Channels can't be marshaled to JSON, so this is a reliable failure case.
func TestTextResultMarshalError(t *testing.T) {
	t.Parallel()
	ch := make(chan int)
	_, err := textResult(ch)
	if err == nil {
		t.Errorf("expected marshal error for chan, got nil")
	}
}

// TestGetActivityLogDefaultLimit checks that the limit-default branch
// (limit <= 0 → 50) is exercised.
func TestGetActivityLogDefaultLimit(t *testing.T) {
	t.Parallel()
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// No limit provided
	res := callTool(t, session, ctx, "get_activity_log", map[string]any{})
	var arr []any
	if err := json.Unmarshal([]byte(res), &arr); err != nil {
		t.Errorf("not a JSON array: %v", err)
	}

	// Explicit limit
	res2 := callTool(t, session, ctx, "get_activity_log", map[string]any{"limit": float64(10)})
	if err := json.Unmarshal([]byte(res2), &arr); err != nil {
		t.Errorf("not a JSON array: %v", err)
	}
}

// TestGetJobHistoryDefaultLimit covers the limit-default branch.
func TestGetJobHistoryDefaultLimit(t *testing.T) {
	t.Parallel()
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "x", Type: "local", Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("dest: %v", err)
	}
	jobID, err := database.CreateJob(db.Job{Name: "j", StorageDestID: destID})
	if err != nil {
		t.Fatalf("job: %v", err)
	}
	res := callTool(t, session, ctx, "get_job_history", map[string]any{"id": float64(jobID)})
	var arr []any
	if err := json.Unmarshal([]byte(res), &arr); err != nil {
		t.Errorf("not a JSON array: %v", err)
	}
}

// TestGetHealthReadOnly covers the ReadOnly→"replica" mode branch.
func TestGetHealthReadOnly(t *testing.T) {
	t.Parallel()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	srv := New(database, nil, Config{Version: "ro-test", ReadOnly: true})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = srv.Server().Run(ctx, serverTransport)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "x", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })

	res := callTool(t, session, ctx, "get_health", nil)
	if !strings.Contains(res, `"mode": "replica"`) {
		t.Errorf("expected replica mode, got: %s", res)
	}
}
