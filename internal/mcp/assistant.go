package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/recovery"
	"github.com/tinkerologie/vault/internal/runner"
)

type assistantTransport struct {
	server  *mcp.Server
	handler http.Handler
	stop    context.Context
	cancel  context.CancelFunc
}

func (entry *assistantTransport) close() {
	entry.cancel()
	closeSessions(entry.server)
}

// ServeAssistant handles an already authenticated request on the transport for
// its key. Session IDs and tools from other transports are unavailable here.
func (s *MCPServer) ServeAssistant(w http.ResponseWriter, r *http.Request, keyID int64, permitted func() bool) {
	entry := s.assistantTransport(keyID, permitted)
	if entry == nil {
		http.Error(w, "valid API key required", http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stopCancel := context.AfterFunc(entry.stop, cancel)
	defer stopCancel()
	defer cancel()
	defer func() {
		// A request can be initializing a session when revocation takes its
		// snapshot of the SDK's session set. Close that session as well.
		if entry.stop.Err() != nil {
			closeSessions(entry.server)
		}
	}()
	entry.handler.ServeHTTP(w, r.WithContext(ctx))
}

func (s *MCPServer) assistantTransport(keyID int64, permitted func() bool) *assistantTransport {
	s.assistantMu.Lock()
	defer s.assistantMu.Unlock()
	// Check the durable revocation decision under the registry lock. A request
	// authenticated before revocation cannot recreate a retired transport.
	if s.assistantsClosed || !permitted() {
		return nil
	}
	if entry := s.assistants[keyID]; entry != nil {
		return entry
	}

	stop, cancel := context.WithCancel(s.assistantStop)
	server := mcp.NewServer(&mcp.Implementation{Name: "vault", Version: s.config.Version}, nil)
	stopSubscriptionsOnShutdown(server, stop)
	registerRecoveryCatalog(server, s.db)
	registerRecoveryEvidence(server, s.db)
	entry := &assistantTransport{
		server: server,
		handler: mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return server },
			&mcp.StreamableHTTPOptions{SessionTimeout: 15 * time.Minute}),
		stop:   stop,
		cancel: cancel,
	}
	s.assistants[keyID] = entry
	return entry
}

// CloseAssistant closes the sessions associated with one revoked key.
func (s *MCPServer) CloseAssistant(keyID int64) {
	s.assistantMu.Lock()
	entry := s.assistants[keyID]
	delete(s.assistants, keyID)
	s.assistantMu.Unlock()
	if entry != nil {
		entry.close()
	}
}

func registerRecoveryCatalog(server *mcp.Server, database *db.DB) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_recovery_catalog",
		Description: "List recorded recovery metadata across Jobs and Restore Points",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, opts recovery.CatalogOptions) (*mcp.CallToolResult, recovery.Catalog, error) {
		catalog, err := recovery.BuildCatalog(ctx, database, opts)
		if err != nil && !errors.Is(err, recovery.ErrInvalidCatalogOptions) {
			err = errors.New("could not read recovery catalog")
		}
		return nil, catalog, err
	})
}

func registerRecoveryEvidence(server *mcp.Server, database *db.DB) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_restore_points",
		Description: "Find recorded Restore Points across Jobs and check selected Backup Item membership",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, opts recovery.FindOptions) (*mcp.CallToolResult, recovery.FindResult, error) {
		result, err := recovery.FindRestorePoints(ctx, database, opts)
		if err != nil && !errors.Is(err, runner.ErrInvalidSelection) && !errors.Is(err, recovery.ErrInvalidFindOptions) {
			err = errors.New("could not find Restore Points")
		}
		return nil, result, err
	})
	type evidenceInput struct {
		runner.ItemSelection
		RestorePointID int64 `json:"restore_point_id" jsonschema:"Restore Point ID"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_recovery_evidence",
		Description: "Inspect recorded Backup Item membership, required Backup Chain, storage evidence and Verification for one Restore Point",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input evidenceInput) (*mcp.CallToolResult, runner.RecoveryEvidence, error) {
		if input.RestorePointID <= 0 {
			return nil, runner.RecoveryEvidence{}, errors.New("restore_point_id must be positive")
		}
		result, err := recovery.GetEvidence(ctx, database, input.RestorePointID, input.ItemSelection)
		switch {
		case errors.Is(err, db.ErrNotFound):
			err = errors.New("restore point not found")
		case err != nil && !errors.Is(err, runner.ErrInvalidSelection):
			err = errors.New("could not read recovery evidence")
		}
		return nil, result, err
	})
}
