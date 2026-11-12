package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/operatorproof"
	"github.com/tinkerologie/vault/internal/restorerequest"
)

func TestVMRestoreRequestBlocksMissingChainThroughRESTAndMCP(t *testing.T) {
	database := testDB(t)
	proofKey := [32]byte{15}
	server := NewServer(database, ServerConfig{ServerKey: proofKey[:], OperatorProof: operatorproof.New(proofKey, nil)})
	assistant := issueAssistantKey(t, server, "VM recovery assistant")
	grantRestoreRequests(t, server, assistant.ID, true)

	root := t.TempDir()
	dest, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "Fixture storage", Type: "local", Config: fmt.Sprintf(`{"path":%q}`, root),
	})
	job, err := database.CreateJob(db.Job{Name: "VM fixture", StorageDestID: requireFixture(t, dest, err)})
	job = requireFixture(t, job, err)
	const missingParent = 500
	point := evidencePoint(t, database, job, missingParent, "incremental", "private-fixture/selected",
		`{"item_sizes":{"guest":20},"item_types":{"guest":"vm"}}`)
	source := filepath.Join(root, "private-fixture", "selected", "guest")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "disk.img")
	for file, payload := range map[string]string{
		"domain.xml":   fmt.Sprintf(`<domain><name>guest</name><uuid>22222222-3333-4444-aaaa-555555555555</uuid><devices><disk device="disk" type="file"><driver type="qcow2"/><source file=%q/><target dev="vda"/></disk><graphics passwd="private-graphics-pass"/></devices></domain>`, target),
		"vm_meta.json": `{"state":"running","backup_type":"incremental","checkpoint":"child","parent_checkpoint":"parent","disks":[{"target":"vda","backup_file":"vdisk0.qcow2","format":"qcow2"}]}`,
		"vdisk0.qcow2": "fixture-data",
	} {
		if err := os.WriteFile(filepath.Join(source, file), []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := recoveryTree(t, root)
	args := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"guest","item_type":"vm","write_mode":"replace"}`, job, point)
	response := callRestoreRequest(server, http.MethodPost, restoreRequestsPath, remotePeer, assistant.Key, args)
	var submitted restorerequest.View
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &submitted) != nil ||
		submitted.Eligible || submitted.Chain.Status != "missing" || submitted.Chain.MissingRestorePointID != missingParent {
		t.Fatalf("submit incomplete VM: %d %s", response.Code, response.Body)
	}
	for _, secret := range []string{root, target, "private-graphics-pass", "private-fixture/selected"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("assistant view exposed %q", secret)
		}
	}
	path := restoreRequestsPath + "/" + submitted.ID
	reviewResponse := callRestoreRequest(server, http.MethodGet, path, loopbackPeer, "", "")
	var review restorerequest.View
	if reviewResponse.Code != http.StatusOK || json.Unmarshal(reviewResponse.Body.Bytes(), &review) != nil ||
		review.Eligible || review.ReviewDigest == "" {
		t.Fatalf("operator review: %d %s", reviewResponse.Code, reviewResponse.Body)
	}
	signed := signedApproval(t, proofKey, operatorproof.Action{
		Version: 1, Action: "approve", ID: review.ID, ReviewDigest: review.ReviewDigest,
		Timestamp: time.Now().Unix(), Nonce: strings.Repeat("8", 32),
	})
	refused := callRestoreRequest(server, http.MethodPost, path+"/approve", loopbackPeer, "", signed)
	if refused.Code != http.StatusBadRequest || server.Runner().Status().Active {
		t.Fatalf("incomplete VM approval: %d %s", refused.Code, refused.Body)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("restore wrote to the VM target: %v", err)
	}
	if after := recoveryTree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("request changed the backup storage")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = remotePeer
		server.router.ServeHTTP(w, r)
	}))
	defer ts.Close()
	defer server.mcp.Close()
	client := assistantMCPClient{t: t, baseURL: ts.URL + "/api/v1/mcp", key: assistant.Key}
	client.initialize()
	message := fmt.Sprintf(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"submit_restore_request","arguments":%s}}`, args)
	status, body := client.post(message)
	if status != http.StatusOK || !strings.Contains(body, submitted.ID) || !strings.Contains(body, "missing_parent") ||
		strings.Contains(body, target) || strings.Contains(body, "private-graphics-pass") {
		t.Fatalf("MCP incomplete VM: %d %s", status, body)
	}
}
