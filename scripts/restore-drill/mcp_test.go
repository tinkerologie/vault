package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tinkerologie/vault/internal/crypto"
)

func TestDrillMCPLegacySubmissionAndScopedRefusal(t *testing.T) {
	inspectKey, err := crypto.GenerateAssistantKey()
	if err != nil {
		t.Fatal(err)
	}
	requestKey, err := crypto.GenerateAssistantKey()
	if err != nil {
		t.Fatal(err)
	}
	type selection struct {
		JobID       int    `json:"job_id"`
		PointID     int    `json:"restore_point_id"`
		Item        string `json:"item_name"`
		Kind        string `json:"item_type"`
		Destination string `json:"destination,omitempty"`
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "test"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "restore_item"}, func(_ context.Context, _ *mcp.CallToolRequest, in selection) (*mcp.CallToolResult, map[string]any, error) {
		if in.JobID != 42 || in.PointID != 12 || in.Item != "fixture" || in.Kind != "folder" {
			t.Errorf("unexpected Restore Request selection: %+v", in)
		}
		return nil, map[string]any{"id": "fixture-request", "status": "pending", "review_link": "/review/fixture", "eligible_for_approval": true}, nil
	})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" && ctx.Value(testKeyContext{}) == inspectKey {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "Enable Allow Restore Requests for this Assistant Key in Vault Settings, then reconnect the MCP client"}}}, nil
			}
			return next(ctx, method, req)
		}
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		if key != inspectKey && key != requestKey {
			t.Errorf("expected scoped key header")
		}
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), testKeyContext{}, key)))
	}))
	defer httpServer.Close()

	keyFile := filepath.Join(t.TempDir(), "key")
	for _, tc := range []struct{ command, key, want string }{
		{"request", requestKey, "fixture-request"},
		{"negative", inspectKey, "Restore Request permission refused"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			if err := os.WriteFile(keyFile, []byte(tc.key+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			args := []string{tc.command, "-endpoint", httpServer.URL + "/api/v1/mcp", "-key-file", keyFile, "-job", "42", "-point", "12", "-type", "folder", "-item", "fixture"}
			if err := run(t.Context(), args, &output); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), tc.want) {
				t.Fatalf("expected %q in %s", tc.want, &output)
			}
		})
	}
}

type testKeyContext struct{}

func TestDrillMCPRefusesNonAssistantKey(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte("fixture-administrator-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"request", "-endpoint", "http://127.0.0.1:1/api/v1/mcp", "-key-file", keyFile, "-job", "42", "-point", "12", "-type", "folder", "-item", "fixture"}
	var output bytes.Buffer
	err := run(t.Context(), args, &output)
	if err == nil || !strings.Contains(err.Error(), "provide a Vault Assistant Key") {
		t.Fatalf("expected refusal before connecting to MCP, got %v", err)
	}
}
