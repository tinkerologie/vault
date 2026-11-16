package api

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/operatorproof"
	"github.com/tinkerologie/vault/internal/restorerequest"
	"github.com/tinkerologie/vault/internal/runner"
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

type vmRestoreHostFixture struct {
	target string
}

func (fixture vmRestoreHostFixture) ReviewVMRestore(_ context.Context, in engine.VMRestoreReviewInput) (engine.VMRestoreEffects, string, error) {
	name := in.Name
	backup, err := engine.InspectVMBackup(in.XML, in.Metadata, in.Artifacts)
	if err != nil {
		return engine.VMRestoreEffects{}, "", err
	}
	data, err := os.ReadFile(fixture.target)
	if err != nil && !os.IsNotExist(err) {
		return engine.VMRestoreEffects{}, "", err
	}
	digest := sha256.Sum256(data)
	effects := engine.VMRestoreEffects{
		Name: name, UUID: backup.UUID,
		Disks: []engine.VMRestoreDiskEffect{
			{Target: "vda", Format: backup.Disks[0].Format, Destination: fixture.target, Replace: err == nil},
		},
		ReadinessMode: "running",
	}
	return effects, hex.EncodeToString(digest[:]), nil
}

func (fixture vmRestoreHostFixture) Restore(ctx context.Context, _ engine.BackupItem, stage string, _ engine.ProgressFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(stage, "recorded.img"))
	if err != nil {
		return err
	}
	return os.WriteFile(fixture.target, data, 0o600)
}

func (vmRestoreHostFixture) Close() error { return nil }

func TestVMRestoreRequestTracksOutcomeAfterApproval(t *testing.T) {
	for _, tt := range []struct {
		name              string
		changeAfterClaim  bool
		externalQCOW2Data bool
	}{
		{name: "reviewed VM disk restored"},
		{name: "disk changed after claim", changeAfterClaim: true},
		{name: "staged disk depends on external data", externalQCOW2Data: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			database := testDB(t)
			proofKey := [32]byte{29}
			target := filepath.Join(t.TempDir(), "target.img")
			var change atomic.Bool
			host := vmRestoreHostFixture{target: target}
			server := NewServer(database, ServerConfig{
				ServerKey: proofKey[:], OperatorProof: operatorproof.New(proofKey, nil),
				vmRestoreHost: func() (runner.VMRestoreHost, error) { return host, nil },
				RestoreRequestFlush: func() error {
					if change.Swap(false) {
						return os.WriteFile(target, []byte("disk appeared after approval"), 0o600)
					}
					return nil
				},
			})
			assistant := issueAssistantKey(t, server, "VM recovery assistant")
			grantRestoreRequests(t, server, assistant.ID, true)

			root := t.TempDir()
			dest, err := database.CreateStorageDestination(db.StorageDestination{
				Name: "Fixture VM storage", Type: "local", Config: fmt.Sprintf(`{"path":%q}`, root),
			})
			job, err := database.CreateJob(db.Job{Name: "VM fixture", StorageDestID: requireFixture(t, dest, err)})
			job = requireFixture(t, job, err)
			// The VM's full backup is valid without the absent Job parent.
			point := evidencePoint(t, database, job, 500, "incremental", "fixture-vm/selected",
				`{"item_sizes":{"guest":20},"item_types":{"guest":"vm"}}`)
			source := filepath.Join(root, "fixture-vm", "selected", "guest")
			if err := os.MkdirAll(source, 0o700); err != nil {
				t.Fatal(err)
			}
			savedDisk, format := "reviewed VM fixture data", "raw"
			if tt.externalQCOW2Data {
				var header [104]byte
				copy(header[:4], "QFI\xfb")
				binary.BigEndian.PutUint32(header[4:8], 3)
				binary.BigEndian.PutUint64(header[72:80], 1<<2)
				savedDisk, format = string(header[:]), "qcow2"
			}
			for file, payload := range map[string]string{
				"domain.xml":   fmt.Sprintf(`<domain><name>guest</name><uuid>22222222-3333-4444-aaaa-555555555555</uuid><devices><disk device="disk" type="file"><driver type=%q/><source file=%q/><target dev="vda"/></disk></devices></domain>`, format, target),
				"vm_meta.json": fmt.Sprintf(`{"state":"shutoff","backup_type":"full","disks":[{"target":"vda","backup_file":"recorded.img","format":%q}]}`, format),
				"recorded.img": savedDisk,
			} {
				if err := os.WriteFile(filepath.Join(source, file), []byte(payload), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(server.mcp.Close)
			client := legacyMCPClient(t, server, loopbackPeer, assistant.Key)
			args := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"guest","item_type":"vm"}`, job, point)
			legacy, toolError, body := legacyMCPRestore(t, client, args)
			if toolError || legacy.ID == "" || legacy.Status != "pending" || legacy.Reused || !legacy.Eligible ||
				legacy.ReviewLink == "" || legacy.RunID != 0 || legacy.VM != nil || strings.Contains(body, target) {
				t.Fatalf("restore_item did not prepare an eligible VM operation: %s", body)
			}
			input := strings.TrimSuffix(args, "}") + `,"write_mode":"replace"}`
			submission := callRestoreRequest(server, http.MethodPost, restoreRequestsPath, remotePeer, assistant.Key, input)
			var submitted restorerequest.View
			if submission.Code != http.StatusOK || json.Unmarshal(submission.Body.Bytes(), &submitted) != nil ||
				!submitted.Reused || submitted.ID != legacy.ID || submitted.ReviewLink != legacy.ReviewLink {
				t.Fatalf("REST did not reuse the same operation: %d %s", submission.Code, submission.Body)
			}
			if server.Runner().Status().Active {
				t.Fatal("restore_item started a VM restore")
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("restore_item wrote to the VM target: %v", err)
			}
			path := restoreRequestsPath + "/" + submitted.ID
			response := callRestoreRequest(server, http.MethodGet, path, loopbackPeer, "", "")
			var review restorerequest.View
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &review) != nil || review.VM == nil ||
				len(review.VM.Disks) != 1 || review.VM.Disks[0].Destination != target || len(review.Chain.Steps) != 1 {
				t.Fatalf("review VM: %d %s", response.Code, response.Body)
			}
			change.Store(tt.changeAfterClaim)
			signed := signedApproval(t, proofKey, operatorproof.Action{
				Version: 1, Action: "approve", ID: review.ID, ReviewDigest: review.ReviewDigest,
				Timestamp: time.Now().Unix(), Nonce: strings.Repeat("9", 32),
			})
			started := callRestoreRequest(server, http.MethodPost, path+"/approve", loopbackPeer, "", signed)
			if started.Code != http.StatusAccepted {
				t.Fatalf("approve VM: %d %s", started.Code, started.Body)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if err := server.Runner().Drain(ctx); err != nil {
				t.Fatal(err)
			}
			finished := callRestoreRequest(server, http.MethodGet, path, remotePeer, assistant.Key, "")
			var result restorerequest.View
			if finished.Code != http.StatusOK || json.Unmarshal(finished.Body.Bytes(), &result) != nil || result.RunID <= 0 ||
				result.Run == nil || result.Run.VMReadiness == nil || result.Run.VMReadiness.ApplicationHealth != "unchecked" {
				t.Fatalf("linked VM outcome: %d %s", finished.Code, finished.Body)
			}
			wantStatus, wantData := "completed", savedDisk
			if tt.changeAfterClaim || tt.externalQCOW2Data {
				wantStatus, wantData = "invalidated", "disk appeared after approval"
				if result.TerminalReason != "reviewed_operation_changed" || result.Run.ItemsDone != 0 {
					t.Fatalf("changed VM outcome: %s", finished.Body)
				}
			}
			if result.Status != wantStatus {
				t.Fatalf("VM outcome: %s, want %s", result.Status, wantStatus)
			}
			data, err := os.ReadFile(target)
			if tt.externalQCOW2Data {
				if !os.IsNotExist(err) {
					t.Fatalf("staged external image wrote to target: %v", err)
				}
				return
			}
			if err != nil || string(data) != wantData {
				t.Fatalf("VM target = %q, err = %v", data, err)
			}
		})
	}
}

func TestVMRestoreRequestRefusesInvalidInputsAndContradictedItemTypes(t *testing.T) {
	database := testDB(t)
	server := NewServer(database, ServerConfig{})
	assistant := issueAssistantKey(t, server, "VM recovery assistant")
	grantRestoreRequests(t, server, assistant.ID, true)
	dest, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "Fixture storage", Type: "local", Config: fmt.Sprintf(`{"path":%q}`, t.TempDir()),
	})
	job, err := database.CreateJob(db.Job{Name: "VM fixture", StorageDestID: requireFixture(t, dest, err)})
	job = requireFixture(t, job, err)
	submit := func(metadata string) *httptest.ResponseRecorder {
		point := evidencePoint(t, database, job, 0, "full", "private-fixture/typed", metadata)
		args := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"guest","item_type":"vm","write_mode":"replace"}`, job, point)
		return callRestoreRequest(server, http.MethodPost, restoreRequestsPath, remotePeer, assistant.Key, args)
	}
	typed := evidencePoint(t, database, job, 0, "full", "private-fixture/vm", `{"item_sizes":{"guest":20},"item_types":{"guest":"vm"}}`)
	for _, extra := range []string{`"write_mode":"merge"`, `"write_mode":"replace","destination":"/etc"`} {
		args := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"guest","item_type":"vm",%s}`, job, typed, extra)
		if response := callRestoreRequest(server, http.MethodPost, restoreRequestsPath, remotePeer, assistant.Key, args); response.Code != http.StatusBadRequest {
			t.Errorf("VM request with %s: %d %s", extra, response.Code, response.Body)
		}
	}
	// The item list is unknown, but the recorded type contradicts the request.
	for _, metadata := range []string{
		`{"item_types":{"guest":"folder"}}`,
		`{"manifest_items":[{"name":"guest","type":"container"}]}`,
	} {
		if response := submit(metadata); response.Code != http.StatusBadRequest {
			t.Errorf("VM request for a point recording %s: %d %s", metadata, response.Code, response.Body)
		}
	}
	// Without any recorded type the request stays reviewable but ineligible.
	response := submit(`{"item_types":{"other":"vm"}}`)
	var unknown restorerequest.View
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &unknown) != nil ||
		unknown.Eligible || unknown.ItemTypeEvidence != runner.Unknown {
		t.Errorf("VM request for an untyped point: %d %s", response.Code, response.Body)
	}
}
