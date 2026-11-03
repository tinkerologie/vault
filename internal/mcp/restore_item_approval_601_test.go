package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
	jobintake "github.com/tinkerologie/vault/internal/jobs"
	"github.com/tinkerologie/vault/internal/runner"
	"github.com/tinkerologie/vault/internal/ws"
)

// Issue #601: restore_item returns a refusal that needs approval as an error
// naming the Vault UI step, instead of "restore started".
func TestRestoreItemReturnsApprovalRefusals(t *testing.T) {
	refusals := map[string]string{
		"host privileges": "restoring web would grant host access from a backup this server cannot vouch for (imported or replicated): privileged mode",
		"unpinned image":  "cannot restore web with the image it was backed up with: the backup recorded no digest for this image (API: allow_unpinned_image)",
	}
	for name, reason := range refusals {
		t.Run(name, func(t *testing.T) {
			session, database, srv := setupRestoreTest(t)
			var got []runner.RestoreItemOptions
			srv.startRestore = func(_ context.Context, _ db.RestorePoint, _, _, _, _ string, opts ...runner.RestoreItemOptions) error {
				got = opts
				return fmt.Errorf("this restore needs approval in the Vault UI (Restore → Container approvals); it cannot be approved through this interface: %w: %s", engine.ErrRestoreNeedsApproval, reason)
			}
			jobID, rpID := restorePointFixture(t, database)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			args := map[string]any{
				"restore_point_id": float64(rpID),
				"job_id":           float64(jobID),
				"item_name":        "web",
				"item_type":        "container",
			}
			res := callToolRaw(t, session, ctx, "restore_item", args)
			text := resultText(res)
			if !res.IsError || strings.Contains(text, "restore started") {
				t.Fatalf("result = %s (error %v)", text, res.IsError)
			}
			if !strings.Contains(text, "Vault UI (Restore → Container approvals)") || !strings.Contains(text, reason) {
				t.Errorf("refusal = %s", text)
			}
			if len(got) != 1 {
				t.Fatalf("options = %+v", got)
			}
			// Nor can the caller approve it by sending the approval keys.
			got = nil
			args["allow_host_privileges"] = true
			args["allow_unpinned_image"] = true
			if res := callToolRaw(t, session, ctx, "restore_item", args); !res.IsError || got != nil {
				t.Errorf("approval keys accepted: %s", resultText(res))
			}
		})
	}
}

func setupRestoreTest(t *testing.T) (*mcp.ClientSession, *db.DB, *MCPServer) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	hub := ws.NewHub()
	go hub.Run()
	r := runner.New(database, hub, nil)
	srv := New(database, r, jobintake.New(database, r, nil, nil, nil), Config{Version: "test"})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Server().Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "vault-test", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, database, srv
}

func restorePointFixture(t *testing.T, database *db.DB) (jobID, rpID int64) {
	t.Helper()
	destID, err := database.CreateStorageDestination(db.StorageDestination{Name: "r", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if jobID, err = database.CreateJob(db.Job{Name: "j", StorageDestID: destID}); err != nil {
		t.Fatal(err)
	}
	runID, err := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if rpID, err = database.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "j/rp", Metadata: `{"imported":true}`}); err != nil {
		t.Fatal(err)
	}
	return jobID, rpID
}
