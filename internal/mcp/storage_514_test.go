package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The MCP storage tools validate configs like the REST API, so an unsafe
// local root (#514) is never persisted.
func TestStorageToolsRejectUnsafeLocalRoot514(t *testing.T) {
	t.Parallel()
	session, _ := setupTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, cfg := range []string{`{"path":""}`, `{"path":"/"}`, `{"path":"/usr/local/emhttp"}`, `{"path":"relative"}`} {
		r := callToolRaw(t, session, ctx, "create_storage", map[string]any{"name": "bad", "type": "local", "config": cfg})
		if !r.IsError {
			t.Errorf("create_storage %s: expected an error, got %s", cfg, resultText(r))
		}
	}

	created := callTool(t, session, ctx, "create_storage", map[string]any{"name": "ok", "type": "local", "config": `{"path":"/tmp/vault-mcp-514"}`})
	id := jsonField[float64](t, created, "id")
	r := callToolRaw(t, session, ctx, "update_storage", map[string]any{"id": id, "config": `{"path":"/"}`})
	if !r.IsError || !strings.Contains(resultText(r), "protected") {
		t.Errorf("update_storage to /: expected a protected-path error, got %s", resultText(r))
	}
	// A rename alone does not revalidate (and succeeds).
	if r := callToolRaw(t, session, ctx, "update_storage", map[string]any{"id": id, "name": "renamed"}); r.IsError {
		t.Errorf("rename failed: %s", resultText(r))
	}
}
