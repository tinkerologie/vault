package api

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/webdav"
	"modernc.org/sqlite"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/operatorproof"
	"github.com/tinkerologie/vault/internal/restorerequest"
	"github.com/tinkerologie/vault/internal/runner"
	"github.com/tinkerologie/vault/internal/storage"
)

var restoreStopTerminalWriteAttempts atomic.Int32

func init() {
	sqlite.MustRegisterScalarFunction("vault_test_restore_stop_write_attempt", 0,
		func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
			return int64(restoreStopTerminalWriteAttempts.Add(1)), nil
		})
}

func TestStopRestoreRunMatchesVerifiedOperatorAndExactActiveRun(t *testing.T) {
	database := testDB(t)
	job, _, root, target := requestFolderFixture(t, database)
	archive, err := os.Create(filepath.Join(root, "private-source/full/archive/data.tar"))
	requireFixture(t, archive, err)
	writer := tar.NewWriter(archive)
	if err := writer.WriteHeader(&tar.Header{Name: "restored.txt", Mode: 0o600}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(archive.Name())
	data = requireFixture(t, data, err)
	point := evidencePoint(t, database, job, 0, "full", "private-source/full", fmt.Sprintf(
		`{"item_sizes":{"archive":20},"item_types":{"archive":"folder"},"checksums":{"archive":{"data.tar":"%x"}}}`, sha256.Sum256(data)))

	type readGate struct {
		entered chan struct{}
		release chan struct{}
	}
	var nextGate, activeGate, terminalGate atomic.Pointer[readGate]
	files := webdav.Handler{FileSystem: webdav.Dir(root), LockSystem: webdav.NewMemLS()}
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/data.tar") {
			if gate := activeGate.Swap(nil); gate != nil {
				release := gate.release
				close(gate.entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
		}
		files.ServeHTTP(w, r)
	}))
	defer backup.Close()
	j, err := database.GetJob(job)
	j = requireFixture(t, j, err)
	dest, err := database.GetStorageDestination(j.StorageDestID)
	dest = requireFixture(t, dest, err)
	dest.Type, dest.Config = "webdav", fmt.Sprintf(`{"url":%q}`, backup.URL)
	if err := database.UpdateStorageDestination(dest); err != nil {
		t.Fatal(err)
	}

	key := [32]byte{81}
	server := NewServer(database, ServerConfig{OperatorProof: operatorproof.New(key, nil), RestoreRequestFlush: func() error {
		if gate := nextGate.Swap(nil); gate != nil {
			activeGate.Store(gate)
		}
		if gate := terminalGate.Swap(nil); gate != nil {
			release := gate.release
			close(gate.entered)
			<-release
		}
		return nil
	}})
	defer server.mcp.Close()
	adminKey := setTestAPIKey(t, database)
	assistant := issueAssistantKey(t, server, "Folder assistant")
	grantRestoreRequests(t, server, assistant.ID, true)

	var nonce int
	sign := func(action, id, digest string) string {
		nonce++
		return signedApproval(t, key, operatorproof.Action{
			Version: 1, Action: action, ID: id, ReviewDigest: digest, Operator: "verified-operator",
			Nonce: fmt.Sprintf("%032x", nonce), Timestamp: time.Now().Unix(),
		})
	}
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(8 * time.Second):
			t.Fatal("restore did not reach its storage read")
		}
	}
	approve := func(gate *readGate) (string, restorerequest.View) {
		t.Helper()
		path, review := prepareApprovalFixture(t, server, assistant.Key, job, point)
		if !review.Eligible {
			t.Fatalf("review is ineligible: %+v", review)
		}
		nextGate.Store(gate)
		approved := callRestoreRequest(server, http.MethodPost, path+"/approve", loopbackPeer, "", sign("approve", review.ID, review.ReviewDigest))
		var result restorerequest.View
		if err := json.Unmarshal(approved.Body.Bytes(), &result); err != nil || approved.Code != http.StatusAccepted || result.RunID == 0 {
			t.Fatalf("approve: %d %s", approved.Code, approved.Body)
		}
		wait(gate.entered)
		return path, result
	}

	firstGate := &readGate{entered: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		if firstGate.release != nil {
			close(firstGate.release)
		}
	}()
	firstPath, first := approve(firstGate)
	id := strconv.FormatInt(first.RunID, 10)
	stopPath := "/api/v1/restore-runs/" + id + "/stop"
	proof := sign("stop", id, "")
	for _, tc := range []struct{ label, peer, key, body string }{
		{"no proof", loopbackPeer, "", ""},
		{"shared API key", remotePeer, adminKey, "{}"},
		{"loopback assistant", loopbackPeer, assistant.Key, proof},
		{"remote assistant", remotePeer, assistant.Key, proof},
		{"other run", loopbackPeer, "", sign("stop", strconv.FormatInt(first.RunID+1, 10), "")},
		{"other action", loopbackPeer, "", sign("approve", id, "")},
	} {
		t.Run(tc.label, func(t *testing.T) {
			response := callRestoreRequest(server, http.MethodPost, stopPath, tc.peer, tc.key, tc.body)
			if response.Code != http.StatusForbidden {
				t.Fatalf("stop: %d %s", response.Code, response.Body)
			}
		})
	}
	forged := httptest.NewRequest(http.MethodPost, stopPath, strings.NewReader(`{}`))
	forged.RemoteAddr, forged.Host = loopbackPeer, daemonHost
	forged.Header.Set("X-Vault-Proxy", "unraid")
	if response := serve(server, forged); response.Code != http.StatusForbidden {
		t.Fatalf("forged proxy: %d %s", response.Code, response.Body)
	}
	jobCancel := callRestoreRequest(server, http.MethodPost, fmt.Sprintf("/api/v1/jobs/%d/cancel", job), loopbackPeer, "", "")
	if jobCancel.Code != http.StatusConflict {
		t.Fatalf("Job cancel: %d %s", jobCancel.Code, jobCancel.Body)
	}
	stop := func(path, proof string, code int, status string) {
		t.Helper()
		response := callRestoreRequest(server, http.MethodPost, path, loopbackPeer, "", proof)
		if response.Code != code || (status != "" && !strings.Contains(response.Body.String(), `"status":"`+status+`"`)) {
			t.Fatalf("stop: %d %s, want %d %q", response.Code, response.Body, code, status)
		}
	}
	restoreStopTerminalWriteAttempts.Store(0)
	// Fail the first terminal run-row write after the request update.
	// Both updates must be retried together after SQLite rolls them back.
	if _, err := database.Exec(`CREATE TRIGGER fail_one_restore_stop_write BEFORE UPDATE OF status ON job_runs
		WHEN OLD.status = 'running' AND NEW.status = 'cancelled' AND vault_test_restore_stop_write_attempt() = 1
		BEGIN SELECT RAISE(ABORT, 'fixture transient write failure'); END`); err != nil {
		t.Fatal(err)
	}
	stop(stopPath, proof, http.StatusAccepted, "stopping")
	stop(stopPath, proof, http.StatusForbidden, "")
	close(firstGate.release)
	firstGate.release = nil
	server.Runner().WaitForBackgroundWork()

	if got := restoreStopTerminalWriteAttempts.Load(); got != 2 {
		t.Fatalf("terminal write attempts = %d, want 2", got)
	}
	if _, err := os.Stat(filepath.Join(target, "restored.txt")); !os.IsNotExist(err) {
		t.Fatalf("cancelled restore changed the target: %v", err)
	}

	outcome := callRestoreRequest(server, http.MethodGet, firstPath, remotePeer, assistant.Key, "")
	if outcome.Code != http.StatusOK || !strings.Contains(outcome.Body.String(), `"status":"cancelled"`) ||
		!strings.Contains(outcome.Body.String(), `"run_id":`+id) ||
		!strings.Contains(outcome.Body.String(), `"stop_requested_at"`) ||
		!strings.Contains(outcome.Body.String(), `"item_results":[{"status":"cancelled"}]`) ||
		strings.Contains(outcome.Body.String(), "verified-operator") || strings.Contains(outcome.Body.String(), target) {
		t.Fatalf("assistant outcome: %d %s", outcome.Code, outcome.Body)
	}

	runRecord := callRestoreRequest(server, http.MethodGet, "/api/v1/runs/"+id, loopbackPeer, "", "")
	if runRecord.Code != http.StatusOK || !strings.Contains(runRecord.Body.String(), `"stopped_by":"verified-operator"`) {
		t.Fatalf("run stop audit: %d %s", runRecord.Code, runRecord.Body)
	}

	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = remotePeer
		server.router.ServeHTTP(w, r)
	}))
	defer mcp.Close()
	owner := assistantMCPClient{t: t, baseURL: mcp.URL + "/api/v1/mcp", key: assistant.Key}
	owner.initialize()
	status, result := owner.post(fmt.Sprintf(`{"jsonrpc":"2.0","id":44,"method":"tools/call","params":{"name":"get_restore_request","arguments":{"id":%q}}}`, first.ID))
	if status != http.StatusOK || !strings.Contains(result, `"run_id":`+id) ||
		!strings.Contains(result, `"status":"cancelled"`) || strings.Contains(result, target) ||
		strings.Contains(result, "verified-operator") {
		t.Fatalf("owner MCP result: %d %s", status, result)
	}

	secondGate := &readGate{entered: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		if secondGate.release != nil {
			close(secondGate.release)
		}
	}()
	secondPath, second := approve(secondGate)
	stop(stopPath, sign("stop", id, ""), http.StatusConflict, "cancelled")
	completion := &readGate{entered: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		if completion.release != nil {
			close(completion.release)
		}
	}()
	terminalGate.Store(completion)
	close(secondGate.release)
	secondGate.release = nil
	wait(completion.entered)
	secondID := strconv.FormatInt(second.RunID, 10)
	stop("/api/v1/restore-runs/"+secondID+"/stop", sign("stop", secondID, ""), http.StatusConflict, "completed")
	close(completion.release)
	completion.release = nil
	server.Runner().WaitForBackgroundWork()
	outcome = callRestoreRequest(server, http.MethodGet, secondPath, remotePeer, assistant.Key, "")
	if outcome.Code != http.StatusOK || !strings.Contains(outcome.Body.String(), `"status":"completed"`) {
		t.Fatalf("later run outcome: %d %s", outcome.Code, outcome.Body)
	}
	if _, err := os.Stat(filepath.Join(target, "restored.txt")); err != nil {
		t.Fatal(err)
	}

	// The Job action can still cancel an independently started manual restore.
	manualGate := &readGate{entered: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		if manualGate.release != nil {
			close(manualGate.release)
		}
	}()
	activeGate.Store(manualGate)
	restorePoint, err := database.GetRestorePoint(point)
	restorePoint = requireFixture(t, restorePoint, err)
	manualDone := make(chan struct{})
	go func() {
		defer close(manualDone)
		server.Runner().RunRestore(restorePoint, []runner.RestoreTarget{{Name: "archive", Type: "folder"}}, "", "")
	}()
	wait(manualGate.entered)
	stop(stopPath, sign("stop", id, ""), http.StatusConflict, "cancelled")
	jobCancel = callRestoreRequest(server, http.MethodPost, fmt.Sprintf("/api/v1/jobs/%d/cancel", job), loopbackPeer, "", "")
	if jobCancel.Code != http.StatusAccepted {
		t.Fatalf("manual Job cancel: %d %s", jobCancel.Code, jobCancel.Body)
	}
	close(manualGate.release)
	manualGate.release = nil
	wait(manualDone)
}

func TestStopRestoreRunRequiresProof(t *testing.T) {
	server := NewServer(testDB(t), ServerConfig{OperatorProof: operatorproof.New([32]byte{42}, nil)})
	defer server.mcp.Close()
	path := "/api/v1/restore-runs/1/stop"
	response := callRestoreRequest(server, http.MethodPost, path, loopbackPeer, "", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("stop without native proof: %d %s", response.Code, response.Body)
	}
}

func TestStopDeduplicatedRestorePersistsAuditAndCancelsInterruptedRead(t *testing.T) {
	database := testDB(t)
	jobID, _, root, target := requestFolderFixture(t, database)
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
	chunkID, err := repo.Put([]byte("data"))
	chunkID = requireFixture(t, chunkID, err)
	manifestID, err := repo.PutManifest("archive", dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		"restored.txt": {Size: 4, Mode: 0o600, Chunks: []dedup.ID{chunkID}},
	}})
	manifestID = requireFixture(t, manifestID, err)
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	point := evidencePoint(t, database, jobID, 0, "full", "private-source/chunked",
		fmt.Sprintf(`{"item_manifests":{"archive":"%x"},"item_types":{"archive":"folder"}}`, manifestID[:]))

	var armRead, blockRead, failFlush, saveSnapshot atomic.Bool
	readStarted, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	files := webdav.Handler{FileSystem: webdav.Dir(root), LockSystem: webdav.NewMemLS()}
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/"+dedup.RepoRoot+"/packs/") && blockRead.Swap(false) {
			close(readStarted)
			<-release
			// An interrupted adapter can return a storage error without a context error.
			w.WriteHeader(http.StatusForbidden)
			return
		}
		files.ServeHTTP(w, r)
	}))
	defer backup.Close()
	dest.Type, dest.Config = "webdav", fmt.Sprintf(`{"url":%q}`, backup.URL)
	if err := database.UpdateStorageDestination(dest); err != nil {
		t.Fatal(err)
	}

	snapshotPath := filepath.Join(t.TempDir(), "during-stop.db")
	proofKey := [32]byte{61}
	server := NewServer(database, ServerConfig{
		ServerKey: serverKey, OperatorProof: operatorproof.New(proofKey, nil),
		RestoreRequestFlush: func() error {
			if armRead.Swap(false) {
				blockRead.Store(true)
			}
			if failFlush.Swap(false) {
				return errors.New("fixture flash flush failure")
			}
			if saveSnapshot.Swap(false) {
				_, err := database.Exec("VACUUM INTO ?", snapshotPath)
				return err
			}
			return nil
		},
	})
	defer server.mcp.Close()
	defer func() {
		unblock()
		server.Runner().WaitForBackgroundWork()
	}()
	assistant := issueAssistantKey(t, server, "Chunked folder assistant")
	grantRestoreRequests(t, server, assistant.ID, true)
	path, review := prepareApprovalFixture(t, server, assistant.Key, jobID, point)
	if !review.Eligible {
		t.Fatalf("deduplicated folder was ineligible: %v", review.Reasons)
	}
	proof := func(action, id, digest, operator string, nonce int) string {
		return signedApproval(t, proofKey, operatorproof.Action{
			Version: 1, Action: action, ID: id, ReviewDigest: digest, Operator: operator,
			Nonce: fmt.Sprintf("%032x", nonce), Timestamp: time.Now().Unix(),
		})
	}
	armRead.Store(true)
	approved := callRestoreRequest(server, http.MethodPost, path+"/approve", loopbackPeer, "",
		proof("approve", review.ID, review.ReviewDigest, "approver", 1))
	var result restorerequest.View
	if err := json.Unmarshal(approved.Body.Bytes(), &result); err != nil || approved.Code != http.StatusAccepted || result.RunID == 0 {
		t.Fatalf("approve: %d %s", approved.Code, approved.Body)
	}
	select {
	case <-readStarted:
	case <-time.After(8 * time.Second):
		t.Fatal("restore did not reach its deduplicated storage read")
	}
	runID := strconv.FormatInt(result.RunID, 10)
	stopPath := "/api/v1/restore-runs/" + runID + "/stop"
	stop := func(operator string, nonce, want int) {
		t.Helper()
		res := callRestoreRequest(server, http.MethodPost, stopPath, loopbackPeer, "", proof("stop", runID, "", operator, nonce))
		if res.Code != want {
			t.Fatalf("stop: %d %s, want %d", res.Code, res.Body, want)
		}
	}
	if _, err := database.Exec(`CREATE TRIGGER fail_stop_audit BEFORE UPDATE OF stop_requested_at ON job_runs
		BEGIN SELECT RAISE(ABORT, 'fixture stop audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	stop("unrecorded-operator", 2, http.StatusInternalServerError)
	if server.Runner().Status().Cancelling {
		t.Fatal("runner signalled a stop whose audit failed to save")
	}
	if _, err := database.Exec(`DROP TRIGGER fail_stop_audit`); err != nil {
		t.Fatal(err)
	}
	failFlush.Store(true)
	stop("verified-stopper", 3, http.StatusServiceUnavailable)
	saveSnapshot.Store(true)
	stop("another-operator", 4, http.StatusAccepted)

	reopened, err := db.Open(snapshotPath)
	reopened = requireFixture(t, reopened, err)
	defer reopened.Close()
	if count, err := reopened.CleanupStaleRuns(); err != nil || count != 1 {
		t.Fatalf("restart cleanup: %d %v", count, err)
	}
	restarted := NewServer(reopened, ServerConfig{})
	defer restarted.mcp.Close()
	readOutcome := func(srv *Server, peer, key string) restorerequest.View {
		t.Helper()
		response := callRestoreRequest(srv, http.MethodGet, path, peer, key, "")
		var view restorerequest.View
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || response.Code != http.StatusOK || view.Run == nil {
			t.Fatalf("read outcome: %d %s", response.Code, response.Body)
		}
		return view
	}
	interrupted := readOutcome(restarted, loopbackPeer, "")
	if interrupted.Status != "interrupted" || interrupted.RunID != result.RunID || interrupted.Run.StopRequestedAt == nil || interrupted.Run.StoppedBy != "verified-stopper" {
		t.Fatalf("restart lost the accepted stop audit: status=%q run=%+v", interrupted.Status, interrupted.Run)
	}

	unblock()
	server.Runner().WaitForBackgroundWork()
	outcome := readOutcome(server, loopbackPeer, "")
	if outcome.Status != "cancelled" || outcome.TerminalReason != "stopped_by_operator" || outcome.Run.Status != "cancelled" ||
		outcome.Run.StopRequestedAt == nil || !outcome.Run.StopRequestedAt.Equal(*interrupted.Run.StopRequestedAt) || outcome.Run.StoppedBy != "verified-stopper" {
		t.Fatalf("interrupted deduplicated restore outcome: status=%q reason=%q run=%+v", outcome.Status, outcome.TerminalReason, outcome.Run)
	}
	if _, err := os.Stat(filepath.Join(target, "restored.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled staging wrote to the restore location: %v", err)
	}
}
