package mcpserver

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ruaan-deysel/vault/internal/db"
	jobintake "github.com/ruaan-deysel/vault/internal/jobs"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// setupCountingReloads builds an MCP session whose Job Intake records every
// scheduler reload, so a test can assert that a Job written over MCP actually
// reached the scheduler.
func setupCountingReloads(t *testing.T) (*mcp.ClientSession, *db.DB, *atomic.Int64) {
	t.Helper()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	hub := ws.NewHub()
	go hub.Run()
	r := runner.New(database, hub, nil)

	var reloads atomic.Int64
	intake := jobintake.New(database, r, func() error {
		reloads.Add(1)
		return nil
	}, nil, nil)
	srv := New(database, r, intake, Config{Version: "reload-test"})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Server().Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "vault-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	t.Cleanup(func() { session.Close() })

	return session, database, &reloads
}

// TestMCPJobWritesReachTheScheduler is the regression test for the defect this
// module was built to close.
//
// The MCP tools wrote Jobs straight to the database and never reloaded the
// scheduler. The scheduler only loads Jobs at Start() and on an explicit
// Reload(), so a Job created through an AI assistant was persisted, displayed
// in the UI with its schedule, and silently never ran — until someone
// restarted the daemon. A changed schedule and a deleted Job were stale in the
// same way.
func TestMCPJobWritesReachTheScheduler(t *testing.T) {
	session, _, reloads := setupCountingReloads(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	storage := callTool(t, session, ctx, "create_storage", map[string]any{
		"name":   "S-reload",
		"type":   "local",
		"config": `{"path":"/tmp/reload-test"}`,
	})
	storageID := jsonField[float64](t, storage, "id")

	created := callTool(t, session, ctx, "create_job", map[string]any{
		"name":            "scheduled-over-mcp",
		"schedule":        "0 3 * * *",
		"storage_dest_id": storageID,
		"items": []map[string]any{
			{"item_type": "container", "item_name": "x", "item_id": "1"},
		},
	})
	jobID := jsonField[float64](t, created, "id")
	if got := reloads.Load(); got != 1 {
		t.Fatalf("scheduler reloads after create_job = %d, want 1 — the job would never run", got)
	}

	callTool(t, session, ctx, "update_job", map[string]any{
		"id": jobID, "schedule": "0 5 * * *",
	})
	if got := reloads.Load(); got != 2 {
		t.Fatalf("scheduler reloads after update_job = %d, want 2 — the old schedule would keep firing", got)
	}

	callTool(t, session, ctx, "delete_job", map[string]any{"id": jobID})
	if got := reloads.Load(); got != 3 {
		t.Fatalf("scheduler reloads after delete_job = %d, want 3 — the deleted job would keep firing", got)
	}
}

// TestMCPCreateJobValidates pins that create_job now refuses the same input the
// REST adapter refuses. It previously persisted anything: an unparseable
// schedule left a Job marked scheduled that the cron parser could never run,
// and a nonexistent storage destination surfaced as an opaque database error.
func TestMCPCreateJobValidates(t *testing.T) {
	session, _, _ := setupCountingReloads(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	items := []map[string]any{{"item_type": "container", "item_name": "x", "item_id": "1"}}
	cases := []struct {
		name string
		args map[string]any
	}{
		{"blank name", map[string]any{"name": "   ", "schedule": "0 3 * * *", "items": items}},
		{"unparseable schedule", map[string]any{"name": "bad-cron", "schedule": "every tuesday", "items": items}},
		{"missing storage destination", map[string]any{
			"name": "bad-dest", "schedule": "0 3 * * *", "storage_dest_id": float64(999999), "items": items,
		}},
		{"invalid compression", map[string]any{
			"name": "bad-comp", "schedule": "0 3 * * *", "compression": "lzma", "items": items,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if r := callToolRaw(t, session, ctx, "create_job", tc.args); !r.IsError {
				t.Errorf("create_job accepted %s, want rejection", tc.name)
			}
		})
	}
}

// TestMCPCreateJobUsesSharedDefaults pins that a Job created over MCP gets the
// same defaults as one created over REST. Each adapter used to carry its own
// copy, and they disagreed — a divergence invisible until the retention sweep
// deleted the wrong restore points.
func TestMCPCreateJobUsesSharedDefaults(t *testing.T) {
	session, database, _ := setupCountingReloads(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	storage := callTool(t, session, ctx, "create_storage", map[string]any{
		"name":   "S-defaults",
		"type":   "local",
		"config": `{"path":"/tmp/defaults-test"}`,
	})
	created := callTool(t, session, ctx, "create_job", map[string]any{
		"name":            "defaults",
		"schedule":        "0 3 * * *",
		"storage_dest_id": jsonField[float64](t, storage, "id"),
		"items":           []map[string]any{{"item_type": "container", "item_name": "x", "item_id": "1"}},
	})
	jobID := int64(jsonField[float64](t, created, "id"))

	got, err := database.GetJob(jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	want := db.DefaultJob()
	if got.BackupTypeChain != want.BackupTypeChain {
		t.Errorf("BackupTypeChain = %q, want %q", got.BackupTypeChain, want.BackupTypeChain)
	}
	if got.RetentionCount != want.RetentionCount || got.RetentionDays != want.RetentionDays {
		t.Errorf("retention = (%d, %d), want (%d, %d)",
			got.RetentionCount, got.RetentionDays, want.RetentionCount, want.RetentionDays)
	}
	if got.NotifyOn != want.NotifyOn {
		t.Errorf("NotifyOn = %q, want %q", got.NotifyOn, want.NotifyOn)
	}
}
