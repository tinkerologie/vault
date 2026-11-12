package api

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/operatorproof"
	"github.com/tinkerologie/vault/internal/restorerequest"
)

func legacyMCPClient(t *testing.T, server *Server, peer, key string) assistantMCPClient {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr, r.Host = peer, daemonHost
		server.router.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	client := assistantMCPClient{t: t, baseURL: ts.URL + "/api/v1/mcp", key: key}
	client.initialize()
	return client
}

func legacyMCPRestore(t *testing.T, client assistantMCPClient, args string) (restorerequest.View, bool, string) {
	t.Helper()
	status, body := client.post(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"restore_item","arguments":%s}}`, args))
	_, payload, found := strings.Cut(body, "data: ")
	var rpc struct {
		Result struct {
			Structured restorerequest.View `json:"structuredContent"`
			IsError    bool                `json:"isError"`
		} `json:"result"`
	}
	if status != http.StatusOK || !found || json.Unmarshal([]byte(payload), &rpc) != nil {
		t.Fatalf("restore_item: %d %s", status, body)
	}
	return rpc.Result.Structured, rpc.Result.IsError, body
}

func TestLegacyMCPRestorePreparesFolderForEveryPermittedClient(t *testing.T) {
	database := testDB(t)
	admin := setTestAPIKey(t, database)
	server := NewServer(database, ServerConfig{OperatorProof: operatorproof.New([32]byte{46}, nil)})
	t.Cleanup(server.mcp.Close)
	assistant := issueAssistantKey(t, server, "Permitted restore assistant")
	grantRestoreRequests(t, server, assistant.ID, true)
	job, point, root, target := requestFolderFixture(t, database)
	before := recoveryTree(t, root)
	ids := make(map[string]bool)
	for _, tc := range []struct{ name, peer, key string }{
		{"global API Key", remotePeer, admin},
		{"host-local MCP", loopbackPeer, ""},
		{"permitted Assistant Key over a tunnel", loopbackPeer, assistant.Key},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := legacyMCPClient(t, server, tc.peer, tc.key)
			args := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_type":"folder","item_name":"archive","clean_destination":false}`, job, point)
			view, toolError, body := legacyMCPRestore(t, client, args)
			if toolError || view.ID == "" || view.Status != "pending" || !view.Eligible || view.RunID != 0 ||
				view.WriteMode != "merge" || view.ExpiresAt.IsZero() || ids[view.ID] ||
				view.ReviewLink != "/plugins/vault/include/app.php#/restore?request="+view.ID ||
				strings.Contains(body, target) || strings.Contains(body, "restore started") {
				t.Fatalf("legacy restore response: %s", body)
			}
			ids[view.ID] = true
			if tc.key == assistant.Key {
				status, body := client.post(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_restore_request","arguments":{"id":%q}}}`, view.ID))
				if status != http.StatusOK || !strings.Contains(body, view.ID) || !strings.Contains(body, `"status":"pending"`) {
					t.Fatalf("owner cannot follow the legacy submission: %d %s", status, body)
				}
			}
			review := callRestoreRequest(server, http.MethodGet, restoreRequestsPath+"/"+view.ID, loopbackPeer, "", "")
			if review.Code != http.StatusOK || !strings.Contains(review.Body.String(), target) {
				t.Fatalf("operator review fixture: %d %s", review.Code, review.Body)
			}
			unsigned := callRestoreRequest(server, http.MethodPost, restoreRequestsPath+"/"+view.ID+"/approve", tc.peer, tc.key, `{}`)
			if unsigned.Code != http.StatusForbidden {
				t.Fatalf("caller could approve without native proof: %d %s", unsigned.Code, unsigned.Body)
			}
			again, toolError, body := legacyMCPRestore(t, client, args)
			if toolError || again.ID != view.ID || !again.Reused {
				t.Fatalf("retry did not reuse pending request: %s", body)
			}
		})
	}
	if server.Runner().Status().Active || !reflect.DeepEqual(before, recoveryTree(t, root)) {
		t.Fatal("submitting a request ran a restore or changed storage")
	}
}

func TestLegacyMCPRestoreRefusesScopeAndUnsupportedInputs(t *testing.T) {
	database := testDB(t)
	admin := setTestAPIKey(t, database)
	server := NewServer(database, ServerConfig{})
	t.Cleanup(server.mcp.Close)
	inspector := issueAssistantKey(t, server, "Inspection assistant")
	job, point, root, _ := requestFolderFixture(t, database)
	before := recoveryTree(t, root)
	args := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_type":"folder","item_name":"archive"}`, job, point)
	for _, tc := range []struct{ name, peer, key, args, want string }{
		{"inspection over a tunnel", loopbackPeer, inspector.Key, args, "Allow Restore Requests"},
		{"unknown point", remotePeer, admin, strings.Replace(args, fmt.Sprintf(`"restore_point_id":%d`, point), `"restore_point_id":99999`, 1), "restore point not found"},
		{"unsupported type", remotePeer, admin, strings.Replace(args, "folder", "zfs", 1), "folder, Docker container, or VM"},
		{"global key passphrase", remotePeer, admin, strings.TrimSuffix(args, "}") + `,"passphrase":"secret-fixture"}`, "Backup Passphrase in Vault"},
		{"host-local passphrase", loopbackPeer, "", strings.TrimSuffix(args, "}") + `,"passphrase":"secret-fixture"}`, "Backup Passphrase in Vault"},
		{"host privilege override", loopbackPeer, "", strings.TrimSuffix(args, "}") + `,"allow_host_privileges":true}`, "manual Restore wizard"},
		{"unpinned image override", remotePeer, admin, strings.TrimSuffix(args, "}") + `,"allow_unpinned_image":true}`, "manual Restore wizard"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := legacyMCPClient(t, server, tc.peer, tc.key)
			_, toolError, body := legacyMCPRestore(t, client, tc.args)
			if !toolError || !strings.Contains(body, tc.want) || strings.Contains(body, "secret-fixture") {
				t.Fatalf("restore_item refusal: %s", body)
			}
		})
	}
	if w := callRestoreRequest(server, http.MethodGet, restoreRequestsPath, loopbackPeer, "", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"requests":[]`) {
		t.Fatalf("refusal stored a pending request: %d %s", w.Code, w.Body)
	}
	if server.Runner().Status().Active || !reflect.DeepEqual(before, recoveryTree(t, root)) {
		t.Fatal("refusal ran a restore or changed storage")
	}
}

func TestManualRESTRestoreStillRunsFolder(t *testing.T) {
	database := testDB(t)
	server := NewServer(database, ServerConfig{})
	assistant := issueAssistantKey(t, server, "Permitted restore assistant")
	grantRestoreRequests(t, server, assistant.ID, true)
	job, point, root, target := requestFolderFixture(t, database)
	const contents = "known manual restore content"
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "example.txt", Mode: 0o600, Uid: os.Getuid(), Gid: os.Getgid(), Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(contents)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "private-source/full/archive/data.tar"), archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/jobs/%d/restore", job)
	body := fmt.Sprintf(`{"restore_point_id":%d,"items":["archive"]}`, point)
	if w := callRestoreRequest(server, http.MethodPost, path, loopbackPeer, assistant.Key, body); w.Code != http.StatusForbidden {
		t.Fatalf("Assistant Key could use manual restore: %d %s", w.Code, w.Body)
	}
	if w := callRestoreRequest(server, http.MethodPost, path, loopbackPeer, "", body); w.Code != http.StatusAccepted {
		t.Fatalf("manual restore: %d %s", w.Code, w.Body)
	}
	if err := server.Runner().Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(target, "example.txt")); err != nil || string(data) != contents {
		t.Fatalf("manual restore file = %q, error = %v", data, err)
	}
}
