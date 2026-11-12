package mcpserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tinkerologie/vault/internal/restorerequest"
)

const legacyRestoreItemTool = "restore_item"

type restoreItemInput struct {
	RestorePointID   int64  `json:"restore_point_id"`
	JobID            int64  `json:"job_id"`
	ItemName         string `json:"item_name" jsonschema:"Exact name of one whole Backup Item"`
	ItemType         string `json:"item_type" jsonschema:"folder, container, or vm"`
	Destination      string `json:"destination,omitempty" jsonschema:"Alternate restore location. Omit to use the configured original location"`
	CleanDestination *bool  `json:"clean_destination,omitempty" jsonschema:"Replace destination contents (default true). Set false to merge, where supported."`
}

func registerLegacyRestoreItem(server *mcp.Server, service *restorerequest.Service) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        legacyRestoreItemTool,
		Description: "Prepare a pending Restore Request for one whole folder, Docker container, or VM. Open the returned review link in Unraid for operator approval. This call does not start a restore.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input restoreItemInput) (*mcp.CallToolResult, restorerequest.View, error) {
		if service == nil {
			return nil, restorerequest.View{}, errors.New("Restore Requests are unavailable")
		}
		switch input.ItemType {
		case "folder", "container", "vm":
		default:
			return nil, restorerequest.View{}, errors.New("choose one whole folder, Docker container, or VM, or use the manual Restore wizard")
		}
		mode := "replace"
		if input.CleanDestination != nil && !*input.CleanDestination {
			mode = "merge"
		}
		result, err := service.SubmitLegacyMCP(ctx, restorerequest.PrincipalFrom(ctx), restorerequest.Input{
			JobID: input.JobID, RestorePointID: input.RestorePointID, ItemName: input.ItemName,
			ItemType: input.ItemType, Destination: input.Destination, WriteMode: mode,
		})
		return nil, result, restoreRequestToolError(err)
	})
}

// Refuse sensitive fields before SDK schema validation so clients get guidance
// without advertising those fields in the tool schema or echoing their values.
func guardRestoreToolCalls(server *mcp.Server, canRequest bool) {
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			call, ok := req.(*mcp.CallToolRequest)
			if method != "tools/call" || !ok || call.Params == nil {
				return next(ctx, method, req)
			}
			if call.Params.Name != legacyRestoreItemTool && call.Params.Name != submitRestoreRequestTool {
				return next(ctx, method, req)
			}
			if !canRequest {
				return restoreToolRefusal("Enable Allow Restore Requests for this Assistant Key in Vault Settings, then reconnect the MCP client")
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(call.Params.Arguments, &fields) == nil {
				switch {
				case fields["passphrase"] != nil || fields["backup_passphrase"] != nil:
					return restoreToolRefusal("Enter the Backup Passphrase in Vault when reviewing and approving the request")
				case fields["allow_host_privileges"] != nil || fields["allow_unpinned_image"] != nil:
					return restoreToolRefusal("Use the manual Restore wizard in Vault for container overrides")
				}
			}
			return next(ctx, method, req)
		}
	})
}

func restoreToolRefusal(message string) (*mcp.CallToolResult, error) {
	var result mcp.CallToolResult
	result.SetError(errors.New(message))
	return &result, nil
}
