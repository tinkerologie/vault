package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
)

// TestToolsSurfaceDBErrors_458 closes the database under a connected session
// and asserts every DB-backed tool reports a tool error rather than a
// misleading success.
func TestToolsSurfaceDBErrors_458(t *testing.T) {
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	calls := []struct {
		name string
		args map[string]any
	}{
		{"list_jobs", map[string]any{}},
		{"get_job", map[string]any{"id": 1}},
		{"delete_job", map[string]any{"id": 1}},
		{"get_job_history", map[string]any{"id": 1}},
		{"list_storage", map[string]any{}},
		{"get_storage", map[string]any{"id": 1}},
		{"create_storage", map[string]any{"name": "s", "type": "local", "config": `{"path":"/tmp"}`}},
		{"update_storage", map[string]any{"id": 1, "name": "s"}},
		{"delete_storage", map[string]any{"id": 1}},
		{"test_storage", map[string]any{"id": 1}},
		{"list_storage_files", map[string]any{"id": 1}},
		{"get_health_summary", map[string]any{}},
		{"get_activity_log", map[string]any{}},
		{"get_run_logs", map[string]any{"run_id": 1}},
		{"list_restore_points", map[string]any{"job_id": 1}},
		{"list_replication", map[string]any{}},
		{"get_replication", map[string]any{"id": 1}},
		{"delete_replication", map[string]any{"id": 1}},
		{"list_anomalies", map[string]any{}},
		{"acknowledge_anomaly", map[string]any{"id": 1, "action": "dismiss"}},
	}
	for _, c := range calls {
		r := callToolRaw(t, session, ctx, c.name, c.args)
		if !r.IsError {
			t.Errorf("%s: expected tool error on closed DB, got %s", c.name, resultText(r))
		}
	}
}

func TestToolInputValidation_458(t *testing.T) {
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cases := []struct {
		name string
		args map[string]any
	}{
		{"get_activity_log", map[string]any{"before_id": -1}},
		{"get_run_logs", map[string]any{"run_id": 0}},
		{"get_run_logs", map[string]any{"run_id": 1, "after": -1}},
		{"get_run_logs", map[string]any{"run_id": 1, "limit": -1}},
		{"get_run_logs", map[string]any{"run_id": 999}},
		{"delete_replication", map[string]any{"id": 999}},
	}
	for _, c := range cases {
		r := callToolRaw(t, session, ctx, c.name, c.args)
		if c.name == "delete_replication" {
			// Deleting a missing source is idempotent.
			if r.IsError {
				t.Errorf("delete_replication missing id: %s", resultText(r))
			}
			continue
		}
		if !r.IsError {
			t.Errorf("%s %v: expected validation error, got %s", c.name, c.args, resultText(r))
		}
	}
}

func TestCompactTextResultMarshalError_458(t *testing.T) {
	if _, err := compactTextResult(make(chan int)); err == nil {
		t.Fatal("expected marshal error")
	}
	r, err := compactTextResult(map[string]int{"a": 1})
	if err != nil || len(r.Content) != 1 {
		t.Fatalf("compactTextResult = %+v, %v", r, err)
	}
}

// TestToolsSurfaceSecondQueryErrors_458 lets the first lookup succeed and
// drops the table the follow-up query reads, so the second error branch of
// each multi-query tool is exercised.
func TestToolsSurfaceSecondQueryErrors_458(t *testing.T) {
	session, database := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	jobID, err := database.CreateJob(db.Job{Name: "j"})
	if err != nil {
		t.Fatal(err)
	}
	destID, err := database.CreateStorageDestination(db.StorageDestination{Name: "bad", Type: "bogus", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}

	// Unknown adapter type: list_storage_files reports the adapter error.
	if r := callToolRaw(t, session, ctx, "list_storage_files", map[string]any{"id": destID}); !r.IsError {
		t.Errorf("list_storage_files bad adapter: %s", resultText(r))
	}

	for _, stmt := range []string{"DROP TABLE job_items", "DROP TABLE verify_runs", "DROP TABLE restore_points"} {
		if _, err := database.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if r := callToolRaw(t, session, ctx, "get_job", map[string]any{"id": jobID}); !r.IsError {
		t.Errorf("get_job without job_items: %s", resultText(r))
	}
	if r := callToolRaw(t, session, ctx, "list_restore_points", map[string]any{"job_id": jobID}); !r.IsError {
		t.Errorf("list_restore_points without restore_points: %s", resultText(r))
	}
}
