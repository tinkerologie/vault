package api

import (
	"archive/tar"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"filippo.io/age"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/operatorproof"
	"github.com/tinkerologie/vault/internal/restorerequest"
	"github.com/tinkerologie/vault/internal/runner"
	"github.com/tinkerologie/vault/internal/storage"
)

func TestDirectRestoreApprovalIsForbidden(t *testing.T) {
	server := NewServer(testDB(t), ServerConfig{})
	request := httptest.NewRequest(http.MethodPost, restoreRequestsPath+"/0123456789abcdef0123456789abcdef/approve", strings.NewReader(`{}`))
	request.RemoteAddr, request.Host = loopbackPeer, daemonHost
	request.Header.Set("X-Vault-Proxy", "unraid")
	response := serve(server, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("direct approval: %d %s", response.Code, response.Body)
	}
}

func TestOperatorApprovesFolderRestoreOnce(t *testing.T) {
	database := testDB(t)
	key := [32]byte{17}
	server := NewServer(database, ServerConfig{OperatorProof: operatorproof.New(key, nil)})
	job, point, root, target := requestFolderFixture(t, database)
	var backup bytes.Buffer
	tarWriter := tar.NewWriter(&backup)
	const contents = "known backup content"
	if err := tarWriter.WriteHeader(&tar.Header{Name: "example.txt", Mode: 0o600, Uid: os.Getuid(), Gid: os.Getgid(), Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte(contents)); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	const passphrase = "fixture ephemeral folder secret"
	archive := filepath.Join(root, "private-source/full/archive/data.tar")
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		t.Fatal(err)
	}
	recipient.SetWorkFactor(10)
	var ciphertext bytes.Buffer
	encrypted, err := age.Encrypt(&ciphertext, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encrypted.Write(backup.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := encrypted.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive+".age", ciphertext.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}

	assistant := issueAssistantKey(t, server, "Restore assistant")
	grantRestoreRequests(t, server, assistant.ID, true)
	path, review := prepareApprovalFixture(t, server, assistant.Key, job, point)
	if !review.RequiresPassphrase {
		t.Fatal("encrypted folder review did not require a passphrase")
	}
	signed := func(nonce, secret string) string {
		return signedApproval(t, key, operatorproof.Action{
			Version: 1, Action: "approve", ID: review.ID, ReviewDigest: review.ReviewDigest,
			Timestamp: time.Now().Unix(), Nonce: nonce, Operator: "root", Passphrase: secret,
		})
	}
	wrong := callRestoreRequest(server, http.MethodPost, path+"/approve", loopbackPeer, "",
		signed(strings.Repeat("9", 32), "wrong fixture secret"))
	if wrong.Code != http.StatusBadRequest || strings.Contains(wrong.Body.String(), "wrong fixture secret") {
		t.Fatalf("incorrect passphrase: %d %s", wrong.Code, wrong.Body)
	}

	// A second daemon handle has independent in-process locks. The SQL
	// claim must still permit only one accepted admission.
	secondDB, err := db.Open(database.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer secondDB.Close()
	secondServer := NewServer(secondDB, ServerConfig{OperatorProof: operatorproof.New(key, nil)})
	defer func() { _ = secondServer.Runner().Drain(t.Context()) }()
	defer func() { _ = server.Runner().Drain(t.Context()) }()
	results := make(chan *httptest.ResponseRecorder, 2)
	for i, peer := range []*Server{server, secondServer} {
		go func() {
			nonce := fmt.Sprintf("%032x", i+1)
			results <- callRestoreRequest(peer, http.MethodPost, path+"/approve", loopbackPeer, "", signed(nonce, passphrase))
		}()
	}
	var accepted int
	var runID int64
	for range 2 {
		response := <-results
		if response.Code == http.StatusAccepted {
			accepted++
		} else if response.Code != http.StatusOK {
			t.Fatalf("approve: %d %s", response.Code, response.Body)
		}
		var result restorerequest.View
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.RunID <= 0 {
			t.Fatalf("approve did not link a run: %s", response.Body)
		}
		if runID != 0 && runID != result.RunID {
			t.Fatalf("approval created runs %d and %d", runID, result.RunID)
		}
		runID = result.RunID
	}
	if accepted != 1 {
		t.Fatalf("accepted approvals = %d", accepted)
	}

	duplicate := callRestoreRequest(server, http.MethodPost, path+"/approve", loopbackPeer, "",
		signed("123456789012345678901234567890ac", ""))
	if duplicate.Code != http.StatusOK || !strings.Contains(duplicate.Body.String(), fmt.Sprintf(`"run_id":%d`, runID)) {
		t.Fatalf("approval retry: %d %s", duplicate.Code, duplicate.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		result := callRestoreRequest(server, http.MethodGet, path, remotePeer, assistant.Key, "")
		if result.Code == http.StatusOK && strings.Contains(result.Body.String(), `"status":"completed"`) {
			if !strings.Contains(result.Body.String(), fmt.Sprintf(`"run_id":%d`, runID)) ||
				strings.Contains(result.Body.String(), target) || strings.Contains(result.Body.String(), passphrase) ||
				!strings.Contains(result.Body.String(), "required_backup_data_staged") {
				t.Fatalf("unexpected assistant result: %s", result.Body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restore did not complete: %d %s", result.Code, result.Body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(filepath.Join(target, "example.txt"))
	if err != nil || string(data) != contents {
		t.Fatalf("restore content: %q %v", data, err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = remotePeer
		server.router.ServeHTTP(w, r)
	}))
	defer ts.Close()
	defer server.mcp.Close()
	owner := assistantMCPClient{t: t, baseURL: ts.URL + "/api/v1/mcp", key: assistant.Key}
	owner.initialize()
	status, result := owner.post(fmt.Sprintf(`{"jsonrpc":"2.0","id":44,"method":"tools/call","params":{"name":"get_restore_request","arguments":{"id":%q}}}`, review.ID))
	if status != http.StatusOK || !strings.Contains(result, fmt.Sprintf(`"run_id":%d`, runID)) ||
		!strings.Contains(result, `"status":"completed"`) || strings.Contains(result, passphrase) ||
		strings.Contains(result, target) || strings.Contains(result, "operator_name") {
		t.Fatalf("owner MCP outcome: %d %s", status, result)
	}
	for _, suffix := range []string{"", "/logs"} {
		url := fmt.Sprintf("/api/v1/runs/%d%s", runID, suffix)
		run := callRestoreRequest(server, http.MethodGet, url, loopbackPeer, "", "")
		if run.Code != http.StatusOK || strings.Contains(run.Body.String(), passphrase) {
			t.Fatalf("operator run record: %d %s", run.Code, run.Body)
		}
		if scoped := callRestoreRequest(server, http.MethodGet, url, loopbackPeer, assistant.Key, ""); scoped.Code != http.StatusForbidden {
			t.Fatalf("Assistant Key reached raw run data: %d", scoped.Code)
		}
	}

}

func signedApproval(t *testing.T, key [32]byte, action operatorproof.Action) string {
	t.Helper()
	payload, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key[:])
	mac.Write(payload)
	envelope, err := json.Marshal(operatorproof.Envelope{Payload: string(payload), Signature: hex.EncodeToString(mac.Sum(nil))})
	if err != nil {
		t.Fatal(err)
	}
	return string(envelope)
}

func TestRestoreApprovalRejectsUnboundProofsAndChangedLocation(t *testing.T) {
	database := testDB(t)
	key := [32]byte{29}
	server := NewServer(database, ServerConfig{OperatorProof: operatorproof.New(key, nil)})
	assistant := issueAssistantKey(t, server, "Proof assistant")
	grantRestoreRequests(t, server, assistant.ID, true)
	job, point, root, target := requestFolderFixture(t, database)
	path, review := prepareApprovalFixture(t, server, assistant.Key, job, point)
	action := operatorproof.Action{
		Version: 1, Action: "approve", ID: review.ID, ReviewDigest: review.ReviewDigest,
		Timestamp: time.Now().Unix(), Nonce: strings.Repeat("a", 32),
	}
	approve := func(peer, apiKey string, proof string, status int) {
		t.Helper()
		got := callRestoreRequest(server, http.MethodPost, path+"/approve", peer, apiKey, proof)
		if got.Code != status {
			t.Fatalf("approve: got %d want %d: %s", got.Code, status, got.Body)
		}
	}
	approve(loopbackPeer, "", signedApproval(t, [32]byte{41}, action), http.StatusForbidden)
	approve(loopbackPeer, assistant.Key, signedApproval(t, key, action), http.StatusForbidden)
	expired := action
	expired.Timestamp -= 120
	approve(loopbackPeer, "", signedApproval(t, key, expired), http.StatusForbidden)
	wrongID := action
	wrongID.ID = "0123456789abcdef0123456789abcdef"
	approve(loopbackPeer, "", signedApproval(t, key, wrongID), http.StatusForbidden)
	wrongDigest := action
	wrongDigest.ReviewDigest = strings.Repeat("1", 64)
	approve(loopbackPeer, "", signedApproval(t, key, wrongDigest), http.StatusConflict)
	approve(loopbackPeer, "", signedApproval(t, key, wrongDigest), http.StatusForbidden)

	otherTarget := filepath.Join(root, "new-target")
	if err := os.Mkdir(otherTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(otherTarget, target); err != nil {
		t.Fatal(err)
	}
	action.Nonce = strings.Repeat("b", 32)
	approve(loopbackPeer, "", signedApproval(t, key, action), http.StatusGone)
	result := callRestoreRequest(server, http.MethodGet, path, remotePeer, assistant.Key, "")
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"status":"invalidated"`) ||
		strings.Contains(result.Body.String(), `"run_id"`) {
		t.Fatalf("changed location outcome: %d %s", result.Code, result.Body)
	}
	if entries, err := os.ReadDir(otherTarget); err != nil || len(entries) != 0 {
		t.Fatalf("unexpected writes after invalidation: %v %v", entries, err)
	}
}

func TestRestoreApprovalRunnerAndPersistenceBoundaries(t *testing.T) {
	for _, mode := range []string{"busy", "draining", "expired", "withdrawn", "flash failure", "restart"} {
		t.Run(mode, func(t *testing.T) {
			database := testDB(t)
			key := [32]byte{51}
			var failFlush atomic.Bool
			var clock atomic.Int64
			clock.Store(time.Now().UnixNano())
			var snapshotPath string
			var captured atomic.Bool
			flush := func() error {
				if !failFlush.Load() {
					return nil
				}
				if mode == "restart" && captured.CompareAndSwap(false, true) {
					_, err := database.Exec("VACUUM INTO ?", snapshotPath)
					if err != nil {
						t.Errorf("snapshot committed running request: %v", err)
					}
				}
				return errors.New("fixture flash flush unavailable")
			}
			server := NewServer(database, ServerConfig{
				OperatorProof: operatorproof.New(key, nil), RestoreRequestFlush: flush,
				RestoreRequestClock: func() time.Time { return time.Unix(0, clock.Load()) },
			})
			assistant := issueAssistantKey(t, server, "Boundary assistant")
			grantRestoreRequests(t, server, assistant.ID, true)
			job, point, _, target := requestFolderFixture(t, database)
			path, review := prepareApprovalFixture(t, server, assistant.Key, job, point)
			want, state := http.StatusConflict, "pending"
			switch mode {
			case "busy":
				entered, release := make(chan struct{}), make(chan struct{})
				done := make(chan error, 1)
				go func() {
					done <- server.Runner().AdmitApprovedRestore(func() (runner.ApprovedRestore, error) {
						close(entered)
						<-release
						return runner.ApprovedRestore{}, nil
					})
				}()
				<-entered
				defer func() {
					close(release)
					if err := <-done; err != nil {
						t.Errorf("runner reservation: %v", err)
					}
				}()
			case "draining":
				if err := server.Runner().Drain(t.Context()); err != nil {
					t.Fatal(err)
				}
			case "expired":
				clock.Add(int64(24 * time.Hour))
				want, state = http.StatusGone, "expired"
			case "withdrawn":
				withdrawn := callRestoreRequest(server, http.MethodPost, path+"/withdraw", remotePeer, assistant.Key, "")
				if withdrawn.Code != http.StatusOK || !strings.Contains(withdrawn.Body.String(), `"status":"withdrawn"`) {
					t.Fatalf("withdraw: %d %s", withdrawn.Code, withdrawn.Body)
				}
				want, state = http.StatusGone, "withdrawn"
			case "flash failure", "restart":
				snapshotPath = filepath.Join(t.TempDir(), "crash.db")
				failFlush.Store(true)
				want, state = http.StatusServiceUnavailable, "failed"
			}
			proof := func(nonce string) string {
				return signedApproval(t, key, operatorproof.Action{
					Version: 1, Action: "approve", ID: review.ID, ReviewDigest: review.ReviewDigest,
					Timestamp: time.Now().Unix(), Nonce: nonce,
				})
			}
			response := callRestoreRequest(server, http.MethodPost, path+"/approve", loopbackPeer, "", proof("0123456789abcdef0123456789abcdef"))
			if response.Code != want {
				t.Fatalf("approve: got %d want %d: %s", response.Code, want, response.Body)
			}
			if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("workload target was touched: %v", err)
			}
			if mode == "restart" {
				reopened, err := db.Open(snapshotPath)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				if count, err := reopened.CleanupStaleRuns(); err != nil || count != 1 {
					t.Fatalf("restart cleanup: %d %v", count, err)
				}
				server = NewServer(reopened, ServerConfig{OperatorProof: operatorproof.New(key, nil)})
				state = "interrupted"
			}
			result := callRestoreRequest(server, http.MethodGet, path, remotePeer, assistant.Key, "")
			if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), fmt.Sprintf(`"status":%q`, state)) {
				t.Fatalf("outcome: %d %s", result.Code, result.Body)
			}
			if mode == "restart" {
				retry := callRestoreRequest(server, http.MethodPost, path+"/approve", loopbackPeer, "", proof("0123456789abcdef0123456789abcdee"))
				if retry.Code != http.StatusOK || !strings.Contains(retry.Body.String(), `"status":"interrupted"`) {
					t.Fatalf("interrupted approval retry: %d %s", retry.Code, retry.Body)
				}
			}
		})
	}
}

func prepareApprovalFixture(t *testing.T, server *Server, key string, job, point int64) (string, restorerequest.View) {
	t.Helper()
	input := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"archive","write_mode":"merge"}`, job, point)
	submitted := callRestoreRequest(server, http.MethodPost, restoreRequestsPath, remotePeer, key, input)
	if submitted.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", submitted.Code, submitted.Body)
	}
	var view restorerequest.View
	if err := json.Unmarshal(submitted.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	path := restoreRequestsPath + "/" + view.ID
	review := callRestoreRequest(server, http.MethodGet, path, loopbackPeer, "", "")
	if err := json.Unmarshal(review.Body.Bytes(), &view); err != nil || review.Code != http.StatusOK || view.ReviewDigest == "" {
		t.Fatalf("review: %d %s", review.Code, review.Body)
	}
	return path, view
}

func TestApprovedFolderStagesLaterChainStepBeforeReplacingTarget(t *testing.T) {
	database := testDB(t)
	job, point, root, target := requestFolderFixture(t, database)
	incremental := evidencePoint(t, database, job, point, "incremental", "private-source/incremental", `{"item_sizes":{"archive":20}}`)
	fullArchive := filepath.Join(root, "private-source/full/archive/data.tar")
	laterArchive := filepath.Join(root, "private-source/incremental/archive/data.tar")
	for _, archive := range []string{fullArchive, laterArchive} {
		if err := os.MkdirAll(filepath.Dir(archive), 0o700); err != nil {
			t.Fatal(err)
		}
		var backup bytes.Buffer
		writer := tar.NewWriter(&backup)
		if err := writer.WriteHeader(&tar.Header{Name: "restored.txt", Mode: 0o600}); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archive, backup.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(existing, []byte("before restore"), 0o600); err != nil {
		t.Fatal(err)
	}

	var loseLaterStep atomic.Bool
	key := [32]byte{79}
	server := NewServer(database, ServerConfig{OperatorProof: operatorproof.New(key, nil), RestoreRequestFlush: func() error {
		if loseLaterStep.CompareAndSwap(true, false) {
			return os.Remove(laterArchive)
		}
		return nil
	}})
	defer func() { _ = server.Runner().Drain(t.Context()) }()
	assistant := issueAssistantKey(t, server, "Chain assistant")
	grantRestoreRequests(t, server, assistant.ID, true)
	input := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"archive","write_mode":"replace"}`, job, incremental)
	submitted := callRestoreRequest(server, http.MethodPost, restoreRequestsPath, remotePeer, assistant.Key, input)
	var view restorerequest.View
	if err := json.Unmarshal(submitted.Body.Bytes(), &view); err != nil || submitted.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", submitted.Code, submitted.Body)
	}
	path := restoreRequestsPath + "/" + view.ID
	review := callRestoreRequest(server, http.MethodGet, path, loopbackPeer, "", "")
	if err := json.Unmarshal(review.Body.Bytes(), &view); err != nil || !view.Eligible || view.EffectiveWriteMode != "replace" {
		t.Fatalf("review: %d %s", review.Code, review.Body)
	}
	loseLaterStep.Store(true)
	proof := signedApproval(t, key, operatorproof.Action{
		Version: 1, Action: "approve", ID: view.ID, ReviewDigest: view.ReviewDigest,
		Timestamp: time.Now().Unix(), Nonce: strings.Repeat("5", 32),
	})
	approved := callRestoreRequest(server, http.MethodPost, path+"/approve", loopbackPeer, "", proof)
	if approved.Code != http.StatusAccepted {
		t.Fatalf("approval: %d %s", approved.Code, approved.Body)
	}
	if err := server.Runner().Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	outcome := callRestoreRequest(server, http.MethodGet, path, remotePeer, assistant.Key, "")
	if outcome.Code != http.StatusOK || !strings.Contains(outcome.Body.String(), `"status":"invalidated"`) ||
		!strings.Contains(outcome.Body.String(), `"run_id"`) {
		t.Fatalf("chain outcome: %d %s", outcome.Code, outcome.Body)
	}
	var result restorerequest.View
	if err := json.Unmarshal(outcome.Body.Bytes(), &result); err != nil || result.RunID <= 0 {
		t.Fatalf("decode tracked outcome: %v", err)
	}
	const diagnostic = "A required restore file is missing."
	logsPath := fmt.Sprintf("/api/v1/runs/%d/logs", result.RunID)
	operatorLog := callRestoreRequest(server, http.MethodGet, logsPath, loopbackPeer, "", "")
	if operatorLog.Code != http.StatusOK || !strings.Contains(operatorLog.Body.String(), diagnostic) ||
		strings.Contains(operatorLog.Body.String(), laterArchive) {
		t.Fatalf("operator should get a safe explanation for the missing file: %d %s", operatorLog.Code, operatorLog.Body)
	}
	assistantLog := callRestoreRequest(server, http.MethodGet, logsPath, remotePeer, assistant.Key, "")
	if assistantLog.Code != http.StatusForbidden || strings.Contains(outcome.Body.String(), diagnostic) {
		t.Fatalf("operator diagnostic reached the assistant: logs %d, outcome %s", assistantLog.Code, outcome.Body)
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "before restore" {
		t.Fatalf("reviewed target changed: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(target, "restored.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first chain step wrote before the later step was staged: %v", err)
	}
}

func TestRestoreApprovalRestoresDeduplicatedFolder(t *testing.T) {
	database := testDB(t)
	jobID, _, _, target := requestFolderFixture(t, database)
	job, err := database.GetJob(jobID)
	job = requireFixture(t, job, err)
	dest, err := database.GetStorageDestination(job.StorageDestID)
	dest = requireFixture(t, dest, err)
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	adapter = requireFixture(t, adapter, err)
	defer storage.CloseAdapter(adapter)
	serverKey := bytes.Repeat([]byte{31}, 32)
	repo, err := dedup.InitRepo(database, adapter, dest.ID, serverKey)
	repo = requireFixture(t, repo, err)
	const contents = "known deduplicated content"
	chunkID, err := repo.Put([]byte(contents))
	chunkID = requireFixture(t, chunkID, err)
	manifestID, err := repo.PutManifest("archive", dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		"chunked.txt": {Mode: 0o600, Size: int64(len(contents)), Chunks: []dedup.ID{chunkID}},
	}})
	manifestID = requireFixture(t, manifestID, err)
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	point := evidencePoint(t, database, jobID, 0, "full", "private-source/chunked",
		fmt.Sprintf(`{"item_manifests":{"archive":"%x"},"item_types":{"archive":"folder"}}`, manifestID[:]))

	key := [32]byte{61}
	server := NewServer(database, ServerConfig{ServerKey: serverKey, OperatorProof: operatorproof.New(key, nil)})
	assistant := issueAssistantKey(t, server, "Chunked folder assistant")
	grantRestoreRequests(t, server, assistant.ID, true)
	path, review := prepareApprovalFixture(t, server, assistant.Key, jobID, point)
	if !review.Eligible {
		t.Fatalf("deduplicated folder was ineligible: %v", review.Reasons)
	}
	proof := signedApproval(t, key, operatorproof.Action{
		Version: 1, Action: "approve", ID: review.ID, ReviewDigest: review.ReviewDigest,
		Timestamp: time.Now().Unix(), Nonce: strings.Repeat("6", 32),
	})
	response := callRestoreRequest(server, http.MethodPost, path+"/approve", loopbackPeer, "", proof)
	if response.Code != http.StatusAccepted {
		t.Fatalf("approve: %d %s", response.Code, response.Body)
	}
	if err := server.Runner().Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "chunked.txt"))
	if err != nil || string(data) != contents {
		t.Fatalf("dedup restore content: %q %v", data, err)
	}
}
