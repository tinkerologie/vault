package mcpserver

import (
	"context"
	"errors"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/restorerequest"
)

func registerRestoreRequests(server *mcp.Server, service *restorerequest.Service) {
	type requestID struct {
		ID string `json:"id" jsonschema:"Restore Request ID"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "submit_restore_request",
		Description: "Prepare one whole folder Restore Request for operator review",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input restorerequest.Input) (*mcp.CallToolResult, restorerequest.View, error) {
		result, err := service.Submit(ctx, restorerequest.PrincipalFrom(ctx), input)
		return nil, result, restoreRequestToolError(err)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_restore_request",
		Description: "Read a Restore Request owned by this Assistant Key",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input requestID) (*mcp.CallToolResult, restorerequest.View, error) {
		result, err := service.Get(ctx, restorerequest.PrincipalFrom(ctx), input.ID)
		return nil, result, restoreRequestToolError(err)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "withdraw_restore_request",
		Description: "Withdraw a pending Restore Request owned by this Assistant Key",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input requestID) (*mcp.CallToolResult, restorerequest.View, error) {
		result, err := service.Withdraw(ctx, restorerequest.PrincipalFrom(ctx), input.ID)
		return nil, result, restoreRequestToolError(err)
	})
}

func restoreRequestToolError(err error) error {
	var validation restorerequest.Invalid
	switch {
	case err == nil, errors.As(err, &validation), errors.Is(err, restorerequest.ErrPermission), errors.Is(err, restorerequest.ErrBusy), errors.Is(err, restorerequest.ErrLimit):
		return err
	case errors.Is(err, db.ErrNotFound):
		return errors.New("restore request or selected restore point not found")
	default:
		log.Printf("MCP Restore Request: %v", err)
		return errors.New("could not access Restore Request")
	}
}
