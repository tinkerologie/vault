package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/operatorproof"
)

func TestAssistantKeyHandlersFailClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "assistant-keys.revoked"), 0o700); err != nil {
		t.Fatal(err)
	}
	broken := NewServer(testDB(t), ServerConfig{CredentialDir: dir})
	create := serve(broken, asLocal(httptest.NewRequest(http.MethodPost, assistantKeysPath, strings.NewReader(`{"name":"Fixture"}`))))
	if create.Code != http.StatusInternalServerError || strings.Contains(create.Body.String(), "vault_ak_") {
		t.Fatalf("create without revocation ledger: %d %s", create.Code, create.Body)
	}

	server := NewServer(testDB(t), ServerConfig{})
	missing := callRestoreRequest(server, http.MethodPatch, assistantKeysPath+"/424242", loopbackPeer, "", `{"restore_request":true}`)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown Assistant Key: %d %s", missing.Code, missing.Body)
	}
}

func TestRestoreRequestOperatorHandlersRejectMalformedProofs(t *testing.T) {
	key := [32]byte{91}
	server := NewServer(testDB(t), ServerConfig{OperatorProof: operatorproof.New(key, nil)})
	id := strings.Repeat("f", 32)
	for _, path := range []string{restoreRequestsPath + "/" + id + "/approve", restoreRequestsPath + "/" + id + "/review"} {
		response := callRestoreRequest(server, http.MethodPost, path, loopbackPeer, "", `{"payload":`)
		if response.Code < 400 || response.Code >= 500 {
			t.Fatalf("%s malformed envelope: %d %s", path, response.Code, response.Body)
		}
	}
	if response := callRestoreRequest(server, http.MethodPost, "/api/v1/restore-runs/not-a-number/stop", loopbackPeer, "", `{}`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid run id: %d %s", response.Code, response.Body)
	}
	proof := signedApproval(t, key, operatorproof.Action{
		Version: 1, Action: "stop", ID: "424242", Timestamp: time.Now().Unix(), Nonce: strings.Repeat("7", 32), Operator: "root",
	})
	if response := callRestoreRequest(server, http.MethodPost, "/api/v1/restore-runs/424242/stop", loopbackPeer, "", proof); response.Code != http.StatusNotFound {
		t.Fatalf("unknown approved run: %d %s", response.Code, response.Body)
	}

	unconfigured := NewServer(testDB(t), ServerConfig{})
	review := callRestoreRequest(unconfigured, http.MethodPost, restoreRequestsPath+"/"+id+"/review", loopbackPeer, "", `{}`)
	if review.Code != http.StatusForbidden {
		t.Fatalf("review without operator proof: %d %s", review.Code, review.Body)
	}
}
