// Package mcpserver provides an MCP (Model Context Protocol) server for Vault.
// It exposes backup job management, storage destinations, and status
// operations as MCP tools for AI assistants to interact with.
package mcpserver

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ruaandeysel/vault/internal/db"
	"github.com/ruaandeysel/vault/internal/runner"
	"github.com/ruaandeysel/vault/internal/storage"
)

// MCPServer wraps the MCP server with access to Vault's database and runner.
type MCPServer struct {
	db     *db.DB
	runner *runner.Runner
	server *mcp.Server
}

// New creates a new MCP server with all Vault tools registered.
func New(database *db.DB, r *runner.Runner) *MCPServer {
	s := &MCPServer{
		db:     database,
		runner: r,
	}

	mcpSrv := mcp.NewServer(
		&mcp.Implementation{
			Name:    "vault",
			Version: "1.0.0",
		},
		nil,
	)

	s.server = mcpSrv
	s.registerTools()
	return s
}

// Server returns the underlying MCP server.
func (s *MCPServer) Server() *mcp.Server {
	return s.server
}

// Run starts the MCP server with stdio transport (for CLI use).
func (s *MCPServer) Run(ctx context.Context) error {
	return s.server.Run(ctx, &mcp.StdioTransport{})
}

// HTTPHandler returns an http.Handler for the Streamable HTTP transport.
func (s *MCPServer) HTTPHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(
		func(_ *http.Request) *mcp.Server { return s.server },
		nil,
	)
}

// registerTools adds all Vault management tools to the MCP server.
func (s *MCPServer) registerTools() {
	s.addListJobsTool()
	s.addGetJobTool()
	s.addRunJobTool()
	s.addCreateJobTool()
	s.addDeleteJobTool()

	s.addListStorageTool()
	s.addGetStorageTool()
	s.addTestStorageTool()

	s.addGetHealthTool()
	s.addGetActivityLogTool()

	s.addListRestorePointsTool()
	s.addRestoreItemTool()
}

// --- Job Tools ---

type listJobsInput struct{}

func (s *MCPServer) addListJobsTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "list_jobs",
		Description: "List all backup jobs configured in Vault",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listJobsInput) (*mcp.CallToolResult, any, error) {
		jobs, err := s.db.ListJobs()
		if err != nil {
			return nil, nil, fmt.Errorf("listing jobs: %w", err)
		}
		r, _ := textResult(jobs)
		return r, nil, nil
	})
}

type getJobInput struct {
	ID int64 `json:"id"`
}

func (s *MCPServer) addGetJobTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "get_job",
		Description: "Get a specific backup job with its items by ID",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input getJobInput) (*mcp.CallToolResult, any, error) {
		job, err := s.db.GetJob(input.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("getting job %d: %w", input.ID, err)
		}
		items, err := s.db.GetJobItems(input.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("getting job items: %w", err)
		}
		result := map[string]any{"job": job, "items": items}
		r, _ := textResult(result)
		return r, nil, nil
	})
}

type runJobInput struct {
	ID int64 `json:"id"`
}

func (s *MCPServer) addRunJobTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "run_job",
		Description: "Trigger an immediate backup run for a specific job",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input runJobInput) (*mcp.CallToolResult, any, error) {
		_, err := s.db.GetJob(input.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("job %d not found: %w", input.ID, err)
		}
		go s.runner.RunJob(input.ID)
		r, _ := textResult(map[string]any{"message": "backup started", "job_id": input.ID})
		return r, nil, nil
	})
}

type createJobInput struct {
	Name          string        `json:"name"`
	Description   string        `json:"description,omitempty"`
	Schedule      string        `json:"schedule"`
	StorageDestID int64         `json:"storage_dest_id"`
	Compression   string        `json:"compression,omitempty"`
	Encryption    string        `json:"encryption,omitempty"`
	Items         []jobItemSpec `json:"items"`
}

type jobItemSpec struct {
	ItemType string `json:"item_type"`
	ItemName string `json:"item_name"`
	ItemID   string `json:"item_id"`
	Settings string `json:"settings,omitempty"`
}

func (s *MCPServer) addCreateJobTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "create_job",
		Description: "Create a new backup job with items",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input createJobInput) (*mcp.CallToolResult, any, error) {
		comp := input.Compression
		if comp == "" {
			comp = "zstd"
		}
		enc := input.Encryption
		if enc == "" {
			enc = "none"
		}
		job := db.Job{
			Name:            input.Name,
			Description:     input.Description,
			Enabled:         true,
			Schedule:        input.Schedule,
			BackupTypeChain: "full",
			RetentionCount:  5,
			RetentionDays:   30,
			Compression:     comp,
			Encryption:      enc,
			ContainerMode:   "one_by_one",
			NotifyOn:        "failure",
			VerifyBackup:    true,
			StorageDestID:   input.StorageDestID,
		}
		id, err := s.db.CreateJob(job)
		if err != nil {
			return nil, nil, fmt.Errorf("creating job: %w", err)
		}
		for i, spec := range input.Items {
			settings := spec.Settings
			if settings == "" {
				settings = "{}"
			}
			item := db.JobItem{
				JobID:     id,
				ItemType:  spec.ItemType,
				ItemName:  spec.ItemName,
				ItemID:    spec.ItemID,
				Settings:  settings,
				SortOrder: i,
			}
			if _, err := s.db.AddJobItem(item); err != nil {
				return nil, nil, fmt.Errorf("adding job item %s: %w", spec.ItemName, err)
			}
		}
		r, _ := textResult(map[string]any{"id": id, "message": "job created"})
		return r, nil, nil
	})
}

type deleteJobInput struct {
	ID int64 `json:"id"`
}

func (s *MCPServer) addDeleteJobTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "delete_job",
		Description: "Delete a backup job and all its items",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input deleteJobInput) (*mcp.CallToolResult, any, error) {
		if err := s.db.DeleteJob(input.ID); err != nil {
			return nil, nil, fmt.Errorf("deleting job %d: %w", input.ID, err)
		}
		r, _ := textResult(map[string]any{"message": "job deleted"})
		return r, nil, nil
	})
}

// --- Storage Tools ---

type listStorageInput struct{}

func (s *MCPServer) addListStorageTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "list_storage",
		Description: "List all configured storage destinations",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listStorageInput) (*mcp.CallToolResult, any, error) {
		dests, err := s.db.ListStorageDestinations()
		if err != nil {
			return nil, nil, fmt.Errorf("listing storage: %w", err)
		}
		r, _ := textResult(dests)
		return r, nil, nil
	})
}

type getStorageInput struct {
	ID int64 `json:"id"`
}

func (s *MCPServer) addGetStorageTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "get_storage",
		Description: "Get details of a specific storage destination",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input getStorageInput) (*mcp.CallToolResult, any, error) {
		dest, err := s.db.GetStorageDestination(input.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("getting storage %d: %w", input.ID, err)
		}
		r, _ := textResult(dest)
		return r, nil, nil
	})
}

type testStorageInput struct {
	ID int64 `json:"id"`
}

func (s *MCPServer) addTestStorageTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "test_storage",
		Description: "Test connectivity to a storage destination",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input testStorageInput) (*mcp.CallToolResult, any, error) {
		dest, err := s.db.GetStorageDestination(input.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("getting storage %d: %w", input.ID, err)
		}
		adapter, err := storage.NewAdapter(dest.Type, dest.Config)
		if err != nil {
			r, _ := textResult(map[string]any{"success": false, "error": err.Error()})
			return r, nil, nil
		}
		if err := adapter.TestConnection(); err != nil {
			r, _ := textResult(map[string]any{"success": false, "error": err.Error()})
			return r, nil, nil
		}
		r, _ := textResult(map[string]any{"success": true, "message": "connection successful"})
		return r, nil, nil
	})
}

// --- Status Tools ---

type getHealthInput struct{}

func (s *MCPServer) addGetHealthTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "get_health",
		Description: "Check the Vault server health status",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ getHealthInput) (*mcp.CallToolResult, any, error) {
		r, _ := textResult(map[string]string{"status": "ok"})
		return r, nil, nil
	})
}

type getActivityLogInput struct {
	Limit    int    `json:"limit,omitempty"`
	Category string `json:"category,omitempty"`
}

func (s *MCPServer) addGetActivityLogTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "get_activity_log",
		Description: "Retrieve recent activity log entries from Vault",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input getActivityLogInput) (*mcp.CallToolResult, any, error) {
		limit := input.Limit
		if limit <= 0 {
			limit = 50
		}
		entries, err := s.db.ListActivityLogs(limit, input.Category)
		if err != nil {
			return nil, nil, fmt.Errorf("listing activity log: %w", err)
		}
		r, _ := textResult(entries)
		return r, nil, nil
	})
}

// --- Restore Tools ---

type listRestorePointsInput struct {
	JobID int64 `json:"job_id"`
}

func (s *MCPServer) addListRestorePointsTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "list_restore_points",
		Description: "List available restore points for a backup job",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input listRestorePointsInput) (*mcp.CallToolResult, any, error) {
		rps, err := s.db.ListRestorePoints(input.JobID)
		if err != nil {
			return nil, nil, fmt.Errorf("listing restore points: %w", err)
		}
		r, _ := textResult(rps)
		return r, nil, nil
	})
}

type restoreItemInput struct {
	RestorePointID int64  `json:"restore_point_id"`
	JobID          int64  `json:"job_id"`
	ItemName       string `json:"item_name"`
	ItemType       string `json:"item_type"`
	Destination    string `json:"destination,omitempty"`
	Passphrase     string `json:"passphrase,omitempty"`
}

func (s *MCPServer) addRestoreItemTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "restore_item",
		Description: "Restore an item from a specific restore point",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input restoreItemInput) (*mcp.CallToolResult, any, error) {
		rps, err := s.db.ListRestorePoints(input.JobID)
		if err != nil {
			return nil, nil, fmt.Errorf("listing restore points: %w", err)
		}
		var found *db.RestorePoint
		for i := range rps {
			if rps[i].ID == input.RestorePointID {
				found = &rps[i]
				break
			}
		}
		if found == nil {
			return nil, nil, fmt.Errorf("restore point %d not found", input.RestorePointID)
		}
		go func() {
			if err := s.runner.RestoreItem(*found, input.ItemName, input.ItemType, input.Destination, input.Passphrase); err != nil {
				log.Printf("mcp: restore failed: %v", err)
			}
		}()
		r, _ := textResult(map[string]any{"message": "restore started"})
		return r, nil, nil
	})
}
