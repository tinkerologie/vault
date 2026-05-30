package mcpserver

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// textResult marshals v to JSON and wraps it in a CallToolResult with text content.
func textResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}, nil
}

// compactTextResult marshals v to compact JSON (no indentation) and wraps it
// in a CallToolResult. Used for list responses where payload size matters.
func compactTextResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}, nil
}
