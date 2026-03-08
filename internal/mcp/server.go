// Package mcpserver provides an MCP (Model Context Protocol) server for Vault.
// It exposes backup job management, storage destinations, and status
// operations as MCP tools for AI assistants to interact with.
package mcpserver

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ruaandeysel/vault/internal/db"
	"github.com/ruaandeysel/vault/internal/engine"
	"github.com/ruaandeysel/vault/internal/runner"
	"github.com/ruaandeysel/vault/internal/storage"
)

// MCPServer wraps the MCP server with access to Vault's database and runner.
type MCPServer struct {
	db     *db.DB
	runner *runner.Runner
	server *mcp.Server
	config Config
}

// Config controls MCP metadata exposed to clients.
type Config struct {
	Version  string
	ReadOnly bool
}

// New creates a new MCP server with all Vault tools registered.
func New(database *db.DB, r *runner.Runner, configs ...Config) *MCPServer {
	cfg := Config{Version: "dev"}
	if len(configs) > 0 {
		cfg = configs[0]
		if cfg.Version == "" {
			cfg.Version = "dev"
		}
	}

	s := &MCPServer{
		db:     database,
		runner: r,
		config: cfg,
	}

	mcpSrv := mcp.NewServer(
		&mcp.Implementation{
			Name:    "vault",
			Version: cfg.Version,
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
	// Job tools
	s.addListJobsTool()
	s.addGetJobTool()
	s.addCreateJobTool()
	s.addUpdateJobTool()
	s.addDeleteJobTool()
	s.addRunJobTool()
	s.addGetJobHistoryTool()

	// Storage tools
	s.addListStorageTool()
	s.addGetStorageTool()
	s.addCreateStorageTool()
	s.addUpdateStorageTool()
	s.addDeleteStorageTool()
	s.addTestStorageTool()
	s.addListStorageFilesTool()

	// Discover tools
	s.addListContainersTool()
	s.addListVMsTool()
	s.addListFoldersTool()
	s.addListPluginsTool()

	// Health and status tools
	s.addGetHealthTool()
	s.addGetHealthSummaryTool()
	s.addGetRunnerStatusTool()
	s.addGetActivityLogTool()

	// Restore tools
	s.addListRestorePointsTool()
	s.addRestoreItemTool()

	// Replication tools
	s.addListReplicationTool()
	s.addGetReplicationTool()
	s.addDeleteReplicationTool()
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
		mode := "daemon"
		if s.config.ReadOnly {
			mode = "replica"
		}
		r, _ := textResult(map[string]string{
			"status":  "ok",
			"version": s.config.Version,
			"mode":    mode,
		})
		return r, nil, nil
	})
}

type getRunnerStatusInput struct{}

func (s *MCPServer) addGetRunnerStatusTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "get_runner_status",
		Description: "Get the current backup or restore runner status, including queued jobs",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ getRunnerStatusInput) (*mcp.CallToolResult, any, error) {
		r, _ := textResult(s.runner.Status())
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
		job, err := s.db.GetJob(input.JobID)
		if err != nil {
			return nil, nil, fmt.Errorf("getting job %d: %w", input.JobID, err)
		}
		rps, err := s.db.ListRestorePoints(input.JobID)
		if err != nil {
			return nil, nil, fmt.Errorf("listing restore points: %w", err)
		}
		r, _ := textResult(runner.AnnotateRestorePoints(job, rps))
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

// --- Update Job Tool ---

type updateJobInput struct {
	ID              int64  `json:"id"`
	Name            string `json:"name,omitempty"`
	Description     string `json:"description,omitempty"`
	Enabled         *bool  `json:"enabled,omitempty"`
	Schedule        string `json:"schedule,omitempty"`
	RetentionCount  *int   `json:"retention_count,omitempty"`
	RetentionDays   *int   `json:"retention_days,omitempty"`
	Compression     string `json:"compression,omitempty"`
	Encryption      string `json:"encryption,omitempty"`
	StorageDestID   *int64 `json:"storage_dest_id,omitempty"`
	BackupTypeChain string `json:"backup_type_chain,omitempty"`
	ContainerMode   string `json:"container_mode,omitempty"`
	VMMode          string `json:"vm_mode,omitempty"`
	NotifyOn        string `json:"notify_on,omitempty"`
	VerifyBackup    *bool  `json:"verify_backup,omitempty"`
}

func (s *MCPServer) addUpdateJobTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "update_job",
		Description: "Update an existing backup job. Only provided fields are changed.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input updateJobInput) (*mcp.CallToolResult, any, error) {
		job, err := s.db.GetJob(input.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("getting job %d: %w", input.ID, err)
		}
		if input.Name != "" {
			job.Name = input.Name
		}
		if input.Description != "" {
			job.Description = input.Description
		}
		if input.Enabled != nil {
			job.Enabled = *input.Enabled
		}
		if input.Schedule != "" {
			job.Schedule = input.Schedule
		}
		if input.RetentionCount != nil {
			job.RetentionCount = *input.RetentionCount
		}
		if input.RetentionDays != nil {
			job.RetentionDays = *input.RetentionDays
		}
		if input.Compression != "" {
			job.Compression = input.Compression
		}
		if input.Encryption != "" {
			job.Encryption = input.Encryption
		}
		if input.StorageDestID != nil {
			job.StorageDestID = *input.StorageDestID
		}
		if input.BackupTypeChain != "" {
			job.BackupTypeChain = input.BackupTypeChain
		}
		if input.ContainerMode != "" {
			job.ContainerMode = input.ContainerMode
		}
		if input.VMMode != "" {
			job.VMMode = input.VMMode
		}
		if input.NotifyOn != "" {
			job.NotifyOn = input.NotifyOn
		}
		if input.VerifyBackup != nil {
			job.VerifyBackup = *input.VerifyBackup
		}
		if err := s.db.UpdateJob(job); err != nil {
			return nil, nil, fmt.Errorf("updating job %d: %w", input.ID, err)
		}
		r, _ := textResult(map[string]any{"message": "job updated", "id": input.ID})
		return r, nil, nil
	})
}

// --- Job History Tool ---

type getJobHistoryInput struct {
	ID    int64 `json:"id"`
	Limit int   `json:"limit,omitempty"`
}

func (s *MCPServer) addGetJobHistoryTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "get_job_history",
		Description: "Get the run history for a specific backup job",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input getJobHistoryInput) (*mcp.CallToolResult, any, error) {
		limit := input.Limit
		if limit <= 0 {
			limit = 20
		}
		runs, err := s.db.GetJobRuns(input.ID, limit)
		if err != nil {
			return nil, nil, fmt.Errorf("getting job history: %w", err)
		}
		r, _ := textResult(runs)
		return r, nil, nil
	})
}

// --- Storage CRUD Tools ---

type createStorageInput struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Config string `json:"config"`
}

func (s *MCPServer) addCreateStorageTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "create_storage",
		Description: "Create a new storage destination. Type is one of: local, smb, nfs, sftp. Config is a JSON string with the backend-specific configuration.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input createStorageInput) (*mcp.CallToolResult, any, error) {
		dest := db.StorageDestination{
			Name:   input.Name,
			Type:   input.Type,
			Config: input.Config,
		}
		id, err := s.db.CreateStorageDestination(dest)
		if err != nil {
			return nil, nil, fmt.Errorf("creating storage: %w", err)
		}
		r, _ := textResult(map[string]any{"id": id, "message": "storage created"})
		return r, nil, nil
	})
}

type updateStorageInput struct {
	ID     int64  `json:"id"`
	Name   string `json:"name,omitempty"`
	Type   string `json:"type,omitempty"`
	Config string `json:"config,omitempty"`
}

func (s *MCPServer) addUpdateStorageTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "update_storage",
		Description: "Update an existing storage destination. Only provided fields are changed.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input updateStorageInput) (*mcp.CallToolResult, any, error) {
		dest, err := s.db.GetStorageDestination(input.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("getting storage %d: %w", input.ID, err)
		}
		if input.Name != "" {
			dest.Name = input.Name
		}
		if input.Type != "" {
			dest.Type = input.Type
		}
		if input.Config != "" {
			dest.Config = input.Config
		}
		if err := s.db.UpdateStorageDestination(dest); err != nil {
			return nil, nil, fmt.Errorf("updating storage %d: %w", input.ID, err)
		}
		r, _ := textResult(map[string]any{"message": "storage updated", "id": input.ID})
		return r, nil, nil
	})
}

type deleteStorageInput struct {
	ID int64 `json:"id"`
}

func (s *MCPServer) addDeleteStorageTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "delete_storage",
		Description: "Delete a storage destination. Fails if jobs still reference it.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input deleteStorageInput) (*mcp.CallToolResult, any, error) {
		count, err := s.db.CountJobsByStorageDestID(input.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("checking dependent jobs: %w", err)
		}
		if count > 0 {
			return nil, nil, fmt.Errorf("cannot delete storage %d: %d jobs still reference it", input.ID, count)
		}
		if err := s.db.DeleteStorageDestination(input.ID); err != nil {
			return nil, nil, fmt.Errorf("deleting storage %d: %w", input.ID, err)
		}
		r, _ := textResult(map[string]any{"message": "storage deleted"})
		return r, nil, nil
	})
}

type listStorageFilesInput struct {
	ID     int64  `json:"id"`
	Prefix string `json:"prefix,omitempty"`
}

func (s *MCPServer) addListStorageFilesTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "list_storage_files",
		Description: "List files and directories in a storage destination, optionally filtered by prefix",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input listStorageFilesInput) (*mcp.CallToolResult, any, error) {
		dest, err := s.db.GetStorageDestination(input.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("getting storage %d: %w", input.ID, err)
		}
		adapter, err := storage.NewAdapter(dest.Type, dest.Config)
		if err != nil {
			return nil, nil, fmt.Errorf("creating adapter: %w", err)
		}
		files, err := adapter.List(input.Prefix)
		if err != nil {
			return nil, nil, fmt.Errorf("listing files: %w", err)
		}
		r, _ := textResult(files)
		return r, nil, nil
	})
}

// --- Discover Tools ---

type listContainersInput struct{}

func (s *MCPServer) addListContainersTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "list_containers",
		Description: "List all Docker containers available for backup",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listContainersInput) (*mcp.CallToolResult, any, error) {
		handler, err := engine.NewContainerHandler()
		if err != nil {
			r, _ := textResult(map[string]any{"items": []any{}, "available": false, "error": err.Error()})
			return r, nil, nil
		}
		items, err := handler.ListItems()
		if err != nil {
			r, _ := textResult(map[string]any{"items": []any{}, "available": false, "error": err.Error()})
			return r, nil, nil
		}
		r, _ := textResult(map[string]any{"items": items, "available": true})
		return r, nil, nil
	})
}

type listVMsInput struct{}

func (s *MCPServer) addListVMsTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "list_vms",
		Description: "List all libvirt virtual machines available for backup",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listVMsInput) (*mcp.CallToolResult, any, error) {
		handler, err := engine.NewVMHandler() //nolint:staticcheck // platform-dependent
		if err != nil {                       //nolint:staticcheck // platform-dependent: stub always returns error on non-Linux
			r, _ := textResult(map[string]any{"items": []any{}, "available": false, "error": err.Error()})
			return r, nil, nil
		}
		items, err := handler.ListItems() //nolint:staticcheck // platform-dependent
		if err != nil {                   //nolint:staticcheck // platform-dependent: stub always returns error on non-Linux
			r, _ := textResult(map[string]any{"items": []any{}, "available": false, "error": err.Error()})
			return r, nil, nil
		}
		r, _ := textResult(map[string]any{"items": items, "available": true})
		return r, nil, nil
	})
}

type listFoldersInput struct{}

func (s *MCPServer) addListFoldersTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "list_folders",
		Description: "List folder presets available for backup (e.g. Flash Drive)",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listFoldersInput) (*mcp.CallToolResult, any, error) {
		handler, err := engine.NewFolderHandler()
		if err != nil {
			r, _ := textResult(map[string]any{"items": []any{}, "available": false, "error": err.Error()})
			return r, nil, nil
		}
		items, err := handler.ListItems()
		if err != nil {
			r, _ := textResult(map[string]any{"items": []any{}, "available": false, "error": err.Error()})
			return r, nil, nil
		}
		r, _ := textResult(map[string]any{"items": items, "available": true})
		return r, nil, nil
	})
}

type listPluginsInput struct{}

func (s *MCPServer) addListPluginsTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "list_plugins",
		Description: "List installed Unraid plugins available for backup",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listPluginsInput) (*mcp.CallToolResult, any, error) {
		handler, err := engine.NewPluginHandler() //nolint:staticcheck // platform-dependent
		if err != nil {                           //nolint:staticcheck // platform-dependent: stub returns error on non-Linux
			r, _ := textResult(map[string]any{"items": []any{}, "available": false, "error": err.Error()})
			return r, nil, nil
		}
		items, err := handler.ListItems() //nolint:staticcheck // platform-dependent
		if err != nil {                   //nolint:staticcheck // platform-dependent: stub returns error on non-Linux
			r, _ := textResult(map[string]any{"items": []any{}, "available": false, "error": err.Error()})
			return r, nil, nil
		}
		r, _ := textResult(map[string]any{"items": items, "available": true})
		return r, nil, nil
	})
}

// --- Health Summary Tool ---

type getHealthSummaryInput struct{}

func (s *MCPServer) addGetHealthSummaryTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "get_health_summary",
		Description: "Get an aggregated health summary including health score, protection rate, and recent backup statistics",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ getHealthSummaryInput) (*mcp.CallToolResult, any, error) {
		jobs, err := s.db.ListJobs()
		if err != nil {
			return nil, nil, fmt.Errorf("listing jobs: %w", err)
		}

		var totalItems, protectedItems int
		var recentSuccess, recentFailed int
		var lastSuccessTime *time.Time

		for _, job := range jobs {
			items, err := s.db.GetJobItems(job.ID)
			if err != nil {
				continue
			}
			totalItems += len(items)
			if job.Enabled {
				protectedItems += len(items)
			}
			runs, err := s.db.GetJobRuns(job.ID, 10)
			if err != nil {
				continue
			}
			for _, run := range runs {
				switch run.Status {
				case "success", "completed":
					recentSuccess++
					if run.CompletedAt != nil && (lastSuccessTime == nil || run.CompletedAt.After(*lastSuccessTime)) {
						lastSuccessTime = run.CompletedAt
					}
				case "failed", "error":
					recentFailed++
				}
			}
		}

		totalRuns := recentSuccess + recentFailed
		successRate := 0
		if totalRuns > 0 {
			successRate = (recentSuccess * 100) / totalRuns
		}
		protectionPct := 0
		if totalItems > 0 {
			protectionPct = (protectedItems * 100) / totalItems
		}
		healthScore := (protectionPct*40 + successRate*60) / 100

		result := map[string]any{
			"health_score":    healthScore,
			"total_items":     totalItems,
			"protected_items": protectedItems,
			"protection_pct":  protectionPct,
			"success_rate":    successRate,
			"recent_success":  recentSuccess,
			"recent_failed":   recentFailed,
			"last_success_at": lastSuccessTime,
		}
		r, _ := textResult(result)
		return r, nil, nil
	})
}

// --- Replication Tools ---

type listReplicationInput struct{}

func (s *MCPServer) addListReplicationTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "list_replication",
		Description: "List all configured replication sources",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listReplicationInput) (*mcp.CallToolResult, any, error) {
		sources, err := s.db.ListReplicationSources()
		if err != nil {
			return nil, nil, fmt.Errorf("listing replication sources: %w", err)
		}
		// Redact API keys.
		for i := range sources {
			sources[i].APIKey = "••••••••"
		}
		r, _ := textResult(sources)
		return r, nil, nil
	})
}

type getReplicationInput struct {
	ID int64 `json:"id"`
}

func (s *MCPServer) addGetReplicationTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "get_replication",
		Description: "Get details of a specific replication source by ID",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input getReplicationInput) (*mcp.CallToolResult, any, error) {
		src, err := s.db.GetReplicationSource(input.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("getting replication source %d: %w", input.ID, err)
		}
		src.APIKey = "••••••••"

		// Include replicated jobs.
		jobs, err := s.db.ListReplicatedJobs(input.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("listing replicated jobs: %w", err)
		}
		result := map[string]any{"source": src, "replicated_jobs": jobs}
		r, _ := textResult(result)
		return r, nil, nil
	})
}

type deleteReplicationInput struct {
	ID int64 `json:"id"`
}

func (s *MCPServer) addDeleteReplicationTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "delete_replication",
		Description: "Delete a replication source and its replicated jobs",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input deleteReplicationInput) (*mcp.CallToolResult, any, error) {
		if err := s.db.DeleteReplicatedJobs(input.ID); err != nil {
			log.Printf("mcp: warning: failed to delete replicated jobs for source %d: %v", input.ID, err)
		}
		if err := s.db.DeleteReplicationSource(input.ID); err != nil {
			return nil, nil, fmt.Errorf("deleting replication source %d: %w", input.ID, err)
		}
		r, _ := textResult(map[string]any{"message": "replication source deleted"})
		return r, nil, nil
	})
}
