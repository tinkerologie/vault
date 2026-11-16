package mcpserver

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/restorerequest"
)

func TestServeAssistantRefusesRevokedKey(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := New(database, nil, nil, Config{Version: "fldcov"})
	defer server.Close()
	w := httptest.NewRecorder()
	server.ServeAssistant(w, httptest.NewRequest(http.MethodPost, "/api/v1/mcp", nil), 1, "digest", "", func() bool { return false })
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked Assistant Key reached MCP: %d %s", w.Code, w.Body)
	}
}

func TestRestoreRequestToolErrorKeepsOnlySentinels(t *testing.T) {
	for _, sentinel := range []error{restorerequest.ErrPermission, restorerequest.ErrBusy} {
		got := restoreRequestToolError(fmt.Errorf("fixture private detail: %w", sentinel))
		if got != sentinel {
			t.Fatalf("wrapped %v returned %v", sentinel, got)
		}
	}
	if got := restoreRequestToolError(errors.New("fixture private detail")); got == nil || got.Error() == "fixture private detail" {
		t.Fatalf("unexpected error leaked: %v", got)
	}
}
