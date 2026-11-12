package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/restorerequest"

	"github.com/tinkerologie/vault/internal/db"
)

const restoreRequestsPath = "/api/v1/restore-requests"

func requestFolderFixture(t *testing.T, d *db.DB) (job, point int64, root, target string) {
	t.Helper()
	root = t.TempDir()
	target = filepath.Join(root, "target-private-fixture")
	cfg, err := json.Marshal(map[string]string{"path": root})
	cfg = requireFixture(t, cfg, err)
	dest, err := d.CreateStorageDestination(db.StorageDestination{Name: "Test backup storage", Type: "local", Config: string(cfg)})
	job, err = d.CreateJob(db.Job{Name: "Folder restore Job", StorageDestID: requireFixture(t, dest, err)})
	job = requireFixture(t, job, err)
	settings, err := json.Marshal(map[string]string{"path": target})
	settings = requireFixture(t, settings, err)
	item, err := d.AddJobItem(db.JobItem{JobID: job, ItemType: "folder", ItemName: "archive", Settings: string(settings)})
	requireFixture(t, item, err)
	point = evidencePoint(t, d, job, 0, "full", "private-source/full", `{"item_sizes":{"archive":20}}`)
	dir := filepath.Join(root, "private-source", "full", "archive")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.tar"), []byte("fixture-private-backup-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	return job, point, root, target
}

func callRestoreRequest(srv *Server, method, path, peer, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr, req.Host = peer, daemonHost
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	return serve(srv, req)
}

func grantRestoreRequests(t *testing.T, srv *Server, id int64, enabled bool) {
	t.Helper()
	w := callRestoreRequest(srv, http.MethodPatch, fmt.Sprintf("%s/%d", assistantKeysPath, id), loopbackPeer, "", fmt.Sprintf(`{"restore_request":%t}`, enabled))
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("change Restore Request permission: %d %s", w.Code, w.Body)
	}
}

func TestRestoreRequestPreparesFolderWithoutWorkloadWrites(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Folder assistant")
	job, point, root, target := requestFolderFixture(t, d)
	before := recoveryTree(t, root)
	body := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"archive","write_mode":"replace"}`, job, point)
	w := callRestoreRequest(srv, http.MethodPost, restoreRequestsPath, loopbackPeer, key.Key, body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("inspection key submitted a request: %d %s", w.Code, w.Body)
	}
	grantRestoreRequests(t, srv, key.ID, true)
	w = callRestoreRequest(srv, http.MethodPost, restoreRequestsPath, loopbackPeer, "", body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("keyless local principal could submit a request: %d %s", w.Code, w.Body)
	}
	w = callRestoreRequest(srv, http.MethodPost, restoreRequestsPath, remotePeer, key.Key, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", w.Code, w.Body)
	}
	var submitted struct {
		ID         string `json:"id"`
		ReviewLink string `json:"review_link"`
		Status     string `json:"status"`
		Eligible   bool   `json:"eligible_for_approval"`
		Warnings   []string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &submitted); err != nil {
		t.Fatal(err)
	}
	if submitted.ID == "" || submitted.Status != "pending" || !submitted.Eligible ||
		!reflect.DeepEqual(submitted.Warnings, []string{"unverified_backup"}) ||
		submitted.ReviewLink != "/plugins/vault/include/app.php#/restore?request="+submitted.ID {
		t.Fatalf("unexpected submission: %s", w.Body)
	}
	for _, private := range []string{root, "private-source", key.Key, "key_digest"} {
		if strings.Contains(w.Body.String(), private) {
			t.Errorf("assistant projection exposed %q", private)
		}
	}
	w = callRestoreRequest(srv, http.MethodGet, restoreRequestsPath+"/"+submitted.ID, loopbackPeer, "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), target) ||
		!strings.Contains(w.Body.String(), "clear_target_contents") {
		t.Fatalf("operator review: %d %s", w.Code, w.Body)
	}
	if after := recoveryTree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("request submission changed the workload or backup storage")
	}
}

func submitFolderRequest(t *testing.T, srv *Server, key string, job, point int64, extra string) restorerequest.View {
	t.Helper()
	input := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"archive","write_mode":"merge"%s}`, job, point, extra)
	w := callRestoreRequest(srv, http.MethodPost, restoreRequestsPath, remotePeer, key, input)
	if w.Code != http.StatusCreated {
		t.Fatalf("submit folder: %d %s", w.Code, w.Body)
	}
	var view restorerequest.View
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestRestoreRequestOwnerLifecycleAndHistorySurviveReopen(t *testing.T) {
	d := testDB(t)
	now := time.Date(2026, 11, 11, 6, 0, 0, 1, time.UTC)
	flushes := 0
	flushFails := false
	srv := NewServer(d, ServerConfig{RestoreRequestClock: func() time.Time { return now }, RestoreRequestFlush: func() error {
		flushes++
		if flushFails {
			return errors.New("private snapshot failure")
		}
		return nil
	}})
	key := issueAssistantKey(t, srv, "Owner")
	other := issueAssistantKey(t, srv, "Other assistant")
	grantRestoreRequests(t, srv, key.ID, true)
	grantRestoreRequests(t, srv, other.ID, true)
	job, point, _, target := requestFolderFixture(t, d)
	withdrawn := submitFolderRequest(t, srv, key.Key, job, point, "")
	flushFails = true
	expired := submitFolderRequest(t, srv, key.Key, job, point, fmt.Sprintf(`,"destination":%q`, target))
	flushFails = false
	if expired.PersistenceWarning == "" || strings.Contains(expired.PersistenceWarning, "private") ||
		!expired.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("creation durability or expiry: %+v", expired)
	}
	admin := setTestAPIKey(t, d)
	for _, tc := range []struct {
		method, path, peer, key string
		status                  int
	}{
		{http.MethodGet, restoreRequestsPath + "/" + withdrawn.ID, loopbackPeer, other.Key, http.StatusNotFound},
		{http.MethodPost, restoreRequestsPath + "/" + withdrawn.ID + "/withdraw", remotePeer, other.Key, http.StatusNotFound},
		{http.MethodPost, restoreRequestsPath + "/" + withdrawn.ID + "/withdraw", loopbackPeer, "", http.StatusForbidden},
		{http.MethodGet, restoreRequestsPath, loopbackPeer, key.Key, http.StatusForbidden},
		{http.MethodGet, restoreRequestsPath + "/" + withdrawn.ID, remotePeer, admin, http.StatusForbidden},
		{http.MethodGet, restoreRequestsPath, remotePeer, admin, http.StatusForbidden},
	} {
		w := callRestoreRequest(srv, tc.method, tc.path, tc.peer, tc.key, "")
		if w.Code != tc.status {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
	withdraw := func(id, want string) {
		t.Helper()
		w := callRestoreRequest(srv, http.MethodPost, restoreRequestsPath+"/"+id+"/withdraw", remotePeer, key.Key, "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"`+want+`"`) {
			t.Fatalf("withdraw: %d %s", w.Code, w.Body)
		}
	}
	withdraw(withdrawn.ID, "withdrawn")
	withdraw(withdrawn.ID, "withdrawn")
	now = expired.ExpiresAt
	withdraw(expired.ID, "expired")
	if flushes != 4 {
		t.Fatalf("flushes = %d, want 4 committed transitions", flushes)
	}
	if err := d.Reopen(); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteJob(job); err != nil {
		t.Fatal(err)
	}
	history := restoreRequestsPath + "?status=withdrawn,expired&limit=1"
	seen := map[string]string{}
	for range 2 {
		w := callRestoreRequest(srv, http.MethodGet, history, loopbackPeer, "", "")
		var page restorerequest.Page
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Requests) != 1 {
			t.Fatalf("history page: %d %s", w.Code, w.Body)
		}
		seen[page.Requests[0].ID] = page.Requests[0].Status
		history += "&before=" + page.NextBefore
	}
	if seen[withdrawn.ID] != "withdrawn" || seen[expired.ID] != "expired" {
		t.Fatalf("history missed terminal requests: %v", seen)
	}
}

func TestRestoreRequestPermissionDowngradeSurvivesSnapshotRollback(t *testing.T) {
	d := testDB(t)
	cfg := ServerConfig{CredentialDir: t.TempDir()}
	srv := NewServer(d, cfg)
	key := issueAssistantKey(t, srv, "Assistant")
	grantRestoreRequests(t, srv, key.ID, true)
	job, point, _, _ := requestFolderFixture(t, d)
	saved := submitFolderRequest(t, srv, key.Key, job, point, "")
	backup := filepath.Join(t.TempDir(), "prior.db")
	if err := d.BackupToFile(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	grantRestoreRequests(t, srv, key.ID, false)
	rolledBack, err := db.Open(backup)
	rolledBack = requireFixture(t, rolledBack, err)
	defer rolledBack.Close()
	restarted := NewServer(rolledBack, cfg)
	path := restoreRequestsPath + "/" + saved.ID
	for _, peer := range []string{loopbackPeer, remotePeer} {
		w := callRestoreRequest(restarted, http.MethodGet, path, peer, key.Key, "")
		if w.Code != http.StatusForbidden {
			t.Errorf("downgraded key after rollback: %d %s", w.Code, w.Body)
		}
		w = callRestoreRequest(restarted, http.MethodGet, "/api/v1/recovery/catalog", peer, key.Key, "")
		if w.Code != http.StatusOK {
			t.Errorf("downgraded key lost inspection: %d", w.Code)
		}
	}
	grantRestoreRequests(t, restarted, key.ID, true)
	w := callRestoreRequest(restarted, http.MethodGet, path, remotePeer, key.Key, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), saved.ID) {
		t.Fatalf("re-granted key lost ownership: %d %s", w.Code, w.Body)
	}
}

func TestRestoreRequestMCPChecksEveryCallsKeyAndOwner(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{})
	first := issueAssistantKey(t, srv, "First requester")
	second := issueAssistantKey(t, srv, "Second requester")
	inspection := issueAssistantKey(t, srv, "Inspection")
	grantRestoreRequests(t, srv, first.ID, true)
	grantRestoreRequests(t, srv, second.ID, true)
	job, point, root, _ := requestFolderFixture(t, d)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = remotePeer
		srv.router.ServeHTTP(w, r)
	}))
	defer ts.Close()
	defer srv.mcp.Close()
	newClient := func(key string) assistantMCPClient {
		t.Helper()
		c := assistantMCPClient{t: t, baseURL: ts.URL + "/api/v1/mcp", key: key}
		c.initialize()
		return c
	}
	owner := newClient(first.Key)
	other := newClient(second.Key)
	inspector := newClient(inspection.Key)
	var rpcID atomic.Int64
	rpcID.Store(10)
	tool := func(name, args string) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, rpcID.Add(1), name, args)
	}
	args := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"archive","write_mode":"merge"}`, job, point)
	status, response := owner.post(tool("submit_restore_request", args))
	_, payload, found := strings.Cut(response, "data: ")
	if status != http.StatusOK || !found || strings.Contains(response, root) {
		t.Fatalf("MCP submission: %d %s", status, response)
	}
	var rpc struct {
		Result struct {
			Structured restorerequest.View `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(payload), &rpc); err != nil || rpc.Result.Structured.Status != "pending" {
		t.Fatalf("MCP did not prepare request: %s", payload)
	}
	input := fmt.Sprintf(`{"id":%q}`, rpc.Result.Structured.ID)
	for _, op := range []string{"get_restore_request", "withdraw_restore_request"} {
		if status, body := other.post(tool(op, input)); status != http.StatusOK || !strings.Contains(body, "not found") || strings.Contains(body, rpc.Result.Structured.ID) {
			t.Errorf("MCP other owner %s = %d %s", op, status, body)
		}
		if _, body := inspector.post(tool(op, input)); !strings.Contains(body, `"error"`) {
			t.Errorf("inspection MCP could call %s: %s", op, body)
		}
	}
	var calls sync.WaitGroup
	for range 3 {
		calls.Go(func() {
			if code, body := owner.post(tool("get_restore_request", input)); code != http.StatusOK || !strings.Contains(body, rpc.Result.Structured.ID) {
				t.Errorf("concurrent owner follow: %d %s", code, body)
			}
		})
		calls.Go(func() {
			if code, body := other.post(tool("get_restore_request", input)); code != http.StatusOK || !strings.Contains(body, "not found") {
				t.Errorf("concurrent other-key follow: %d %s", code, body)
			}
		})
	}
	calls.Wait()
	for _, entry := range []struct {
		key  string
		want int
	}{{"", http.StatusUnauthorized}, {second.Key, http.StatusNotFound}} {
		replay := owner
		replay.key = entry.key
		if status, _ := replay.post(tool("get_restore_request", input)); status != entry.want {
			t.Errorf("foreign session status = %d, want %d", status, entry.want)
		}
	}
	sensitive := strings.TrimSuffix(args, "}") + `,"backup_passphrase":"fixture-passphrase-private"}`
	if _, body := owner.post(tool("submit_restore_request", sensitive)); strings.Contains(body, "fixture-passphrase-private") || !strings.Contains(body, `"isError":true`) {
		t.Errorf("unsupported MCP passphrase field: %s", body)
	}
	if status, body := owner.post(tool("withdraw_restore_request", input)); status != http.StatusOK || !strings.Contains(body, `"status":"withdrawn"`) {
		t.Errorf("MCP owner withdrawal: %d %s", status, body)
	}
	grantRestoreRequests(t, srv, first.ID, false)
	if status, _ := owner.post(tool("get_restore_request", input)); status != http.StatusNotFound {
		t.Errorf("downgraded MCP session status = %d", status)
	}
	owner = newClient(first.Key)
	if _, body := owner.post(tool("get_restore_request", input)); !strings.Contains(body, `"error"`) {
		t.Errorf("downgraded MCP session could follow: %s", body)
	}
	if w := callRestoreRequest(srv, http.MethodDelete, fmt.Sprintf("%s/%d", assistantKeysPath, first.ID), loopbackPeer, "", ""); w.Code != http.StatusNoContent {
		t.Fatalf("revoke key: %d", w.Code)
	}
	if status, _ := owner.post(tool("get_restore_request", input)); status != http.StatusUnauthorized {
		t.Errorf("revoked MCP session status = %d", status)
	}
}

func TestRestoreRequestUsesPointEvidenceAndValidatesFolderOnlyInputs(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Assistant")
	grantRestoreRequests(t, srv, key.ID, true)
	job, point, _, _ := requestFolderFixture(t, d)
	legacy := evidencePoint(t, d, job, 0, "full", "missing/legacy", `{"item_sizes":{"archive":20}}`)
	malformed := evidencePoint(t, d, job, 0, "full", "missing/malformed", `{`)
	const typedFolder = `{"item_sizes":{"archive":20},"item_types":{"archive":"folder"}}`
	missingParent := evidencePoint(t, d, job, 8675309, "incremental", "missing/chain", typedFolder)
	wrongType := evidencePoint(t, d, job, 0, "full", "missing/not-folder", `{"item_sizes":{"archive":20},"item_types":{"archive":"vm"}}`)
	knownAbsent := evidencePoint(t, d, job, 0, "full", "missing/absent", `{"item_sizes":{"other":20}}`)
	missingArtifact := evidencePoint(t, d, job, 0, "full", "missing/artifact", typedFolder)
	for _, tc := range []struct {
		id     int64
		reason string
	}{
		{legacy, "folder_type_unconfirmed"}, {malformed, "membership_unknown"},
		{missingParent, "chain_unconfirmed"}, {missingArtifact, "required_data_unusable"},
	} {
		result := submitFolderRequest(t, srv, key.Key, job, tc.id, "")
		if result.Eligible || !strings.Contains(strings.Join(result.Reasons, ","), tc.reason) {
			t.Errorf("point %d: reasons %v, eligible %v", tc.id, result.Reasons, result.Eligible)
		}
	}
	for _, tc := range []struct {
		job, point int64
		extra      string
	}{
		{job, wrongType, ""}, {job, knownAbsent, ""}, {job + 999, point, ""},
		{job, point, `,"destination":"/etc"`}, {job, point, `,"file_paths":["archive"]`},
		{job, point, `,"passphrase":"fixture-secret"`}, {job, point, `,"item_type":"vm"`},
	} {
		body := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"archive","write_mode":"replace"%s}`, tc.job, tc.point, tc.extra)
		w := callRestoreRequest(srv, http.MethodPost, restoreRequestsPath, remotePeer, key.Key, body)
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "fixture-secret") {
			t.Errorf("invalid submission: %d %s", w.Code, w.Body)
		}
	}
	alt := submitFolderRequest(t, srv, key.Key, job, point, `,"destination":"/mnt/user/recovery/fixture"`)
	if alt.TargetChoice != "alternate" || alt.ResolvedPath != "" || alt.EffectiveWriteMode != "merge" || !alt.Eligible {
		t.Errorf("alternate merge: %+v", alt)
	}
	replica := NewServer(d, ServerConfig{ReadOnly: true})
	if err := replica.ReconcileRestoreRequests(t.Context()); err != nil {
		t.Fatalf("replica expiry reconciliation: %v", err)
	}
	if w := callRestoreRequest(replica, http.MethodGet, restoreRequestsPath, loopbackPeer, "", ""); w.Code != http.StatusNotFound {
		t.Errorf("replica request listing status = %d", w.Code)
	}
}

func TestRestoreRequestReconcilesExpiryAfterDowntimeAndOnRead(t *testing.T) {
	d := testDB(t)
	now := time.Date(2026, 11, 11, 6, 0, 0, 1, time.UTC)
	var flushes int
	config := ServerConfig{RestoreRequestClock: func() time.Time { return now }, RestoreRequestFlush: func() error {
		flushes++
		return nil
	}}
	srv := NewServer(d, config)
	key := issueAssistantKey(t, srv, "Assistant")
	grantRestoreRequests(t, srv, key.ID, true)
	job, point, _, target := requestFolderFixture(t, d)
	first := submitFolderRequest(t, srv, key.Key, job, point, "")
	now = now.Add(time.Hour)
	second := submitFolderRequest(t, srv, key.Key, job, point, fmt.Sprintf(`,"destination":%q`, target))
	now = first.ExpiresAt
	if err := d.Reopen(); err != nil {
		t.Fatal(err)
	}
	restarted := NewServer(d, config)
	if err := restarted.ReconcileRestoreRequests(t.Context()); err != nil {
		t.Fatal(err)
	}
	if flushes != 3 {
		t.Fatalf("startup expiry did not persist once, got %d flushes", flushes)
	}
	pending := callRestoreRequest(restarted, http.MethodGet, restoreRequestsPath, loopbackPeer, "", "")
	if pending.Code != http.StatusOK || !strings.Contains(pending.Body.String(), second.ID) || strings.Contains(pending.Body.String(), first.ID) {
		t.Fatalf("pending requests after startup: %d %s", pending.Code, pending.Body)
	}

	now = second.ExpiresAt
	path := restoreRequestsPath + "/" + second.ID
	read := callRestoreRequest(restarted, http.MethodGet, path, remotePeer, key.Key, "")
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"status":"expired"`) || flushes != 4 {
		t.Fatalf("expiry on read: %d %s, flushes %d", read.Code, read.Body, flushes)
	}
	if err := restarted.ReconcileRestoreRequests(t.Context()); err != nil || flushes != 4 {
		t.Fatalf("repeated expiry: %v, flushes %d", err, flushes)
	}
}

func TestRestoreRequestReviewsShallowReplacementAsMerge(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Flash recovery assistant")
	grantRestoreRequests(t, srv, key.ID, true)
	job, point, _, _ := requestFolderFixture(t, d)
	items, err := d.GetJobItems(job)
	items = requireFixture(t, items, err)
	if err := d.UpdateJobItemSettings(items[0].ID, `{"path":"/boot"}`); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"archive","write_mode":"replace"}`, job, point)
	w := callRestoreRequest(srv, http.MethodPost, restoreRequestsPath, remotePeer, key.Key, body)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"effective_write_mode":"merge"`) ||
		!strings.Contains(w.Body.String(), "target_too_shallow_to_clear") || strings.Contains(w.Body.String(), "/boot") {
		t.Fatalf("shallow replacement: %d %s", w.Code, w.Body)
	}
}

func TestRestoreRequestValidationAndMaintenanceRefusals(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Recovery assistant")
	keyURL := fmt.Sprintf("%s/%d", assistantKeysPath, key.ID)
	for _, tc := range []struct {
		url, body string
		want      int
	}{
		{assistantKeysPath + "/not-a-number", `{"restore_request":true}`, http.StatusBadRequest},
		{keyURL, "{}", http.StatusBadRequest},
		{keyURL, `{"restore_request":"yes"}`, http.StatusBadRequest},
		{keyURL, `{"restore_request":false} {}`, http.StatusBadRequest},
		{assistantKeysPath + "/9999", `{"restore_request":true}`, http.StatusNotFound},
	} {
		w := callRestoreRequest(srv, http.MethodPatch, tc.url, loopbackPeer, "", tc.body)
		if w.Code != tc.want {
			t.Errorf("permission refusal: %d, want %d: %s", w.Code, tc.want, w.Body)
		}
	}
	grantRestoreRequests(t, srv, key.ID, true)
	job, point, _, _ := requestFolderFixture(t, d)
	request := submitFolderRequest(t, srv, key.Key, job, point, "")
	for _, query := range []string{"?status=unknown", "?before=invalid", "?limit=0", "?limit=abc", "?limit=201"} {
		w := callRestoreRequest(srv, http.MethodGet, restoreRequestsPath+query, loopbackPeer, "", "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("invalid query %s: %d %s", query, w.Code, w.Body)
		}
	}

	valid := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"archive","write_mode":"merge"}`, job, point)
	for _, tc := range []struct {
		body string
		want int
	}{
		{strings.Replace(valid, "archive", "../archive", 1), http.StatusBadRequest},
		{strings.Replace(valid, "merge", "clean", 1), http.StatusBadRequest},
		{strings.Replace(valid, fmt.Sprintf(`"restore_point_id":%d`, point), `"restore_point_id":9999`, 1), http.StatusNotFound},
	} {
		w := callRestoreRequest(srv, http.MethodPost, restoreRequestsPath, remotePeer, key.Key, tc.body)
		if w.Code != tc.want {
			t.Errorf("invalid submit: %d, want %d: %s", w.Code, tc.want, w.Body)
		}
	}
	lock := srv.storageHandler.MaintenanceLock()
	lock.Lock()
	defer lock.Unlock()
	for _, tc := range []struct {
		method, path, peer, key, body string
	}{
		{http.MethodPost, restoreRequestsPath, remotePeer, key.Key, valid},
		{http.MethodPost, restoreRequestsPath + "/" + request.ID + "/withdraw", remotePeer, key.Key, ""},
		{http.MethodPatch, keyURL, loopbackPeer, "", `{"restore_request":false}`},
	} {
		w := callRestoreRequest(srv, tc.method, tc.path, tc.peer, tc.key, tc.body)
		if w.Code != http.StatusConflict {
			t.Errorf("request during maintenance: %d %s", w.Code, w.Body)
		}
	}
}

func TestRestoreRequestHistoryRetainsStateWhenTwoHandlesRaceExpiryAndWithdrawal(t *testing.T) {
	d := testDB(t)
	now := time.Date(2026, 11, 11, 6, 0, 0, 1, time.UTC)
	var flushes atomic.Int64
	cfg := ServerConfig{RestoreRequestClock: func() time.Time { return now }, RestoreRequestFlush: func() error {
		flushes.Add(1)
		return nil
	}}
	srv := NewServer(d, cfg)
	key := issueAssistantKey(t, srv, "Assistant")
	grantRestoreRequests(t, srv, key.ID, true)
	job, point, _, _ := requestFolderFixture(t, d)
	request := submitFolderRequest(t, srv, key.Key, job, point, "")
	other, err := db.Open(d.Path())
	other = requireFixture(t, other, err)
	defer other.Close()
	otherServer := NewServer(other, cfg)
	now = request.ExpiresAt

	path := restoreRequestsPath + "/" + request.ID
	var calls sync.WaitGroup
	calls.Go(func() {
		w := callRestoreRequest(srv, http.MethodPost, path+"/withdraw", remotePeer, key.Key, "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"expired"`) {
			t.Errorf("withdraw at expiry: %d %s", w.Code, w.Body)
		}
	})
	calls.Go(func() {
		if err := otherServer.ReconcileRestoreRequests(t.Context()); err != nil {
			t.Errorf("reconcile at expiry: %v", err)
		}
	})
	calls.Wait()
	if got := flushes.Load(); got != 2 {
		t.Fatalf("submission and expiry should produce two durable transitions, got %d", got)
	}
}

func TestRestoreRequestReviewsClassicReplayAndReportsMissingOriginal(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Replay assistant")
	grantRestoreRequests(t, srv, key.ID, true)
	job, base, root, _ := requestFolderFixture(t, d)
	incremental := evidencePoint(t, d, job, base, "incremental", "private-source/incremental", `{"item_sizes":{"archive":20}}`)
	dir := filepath.Join(root, "private-source", "incremental", "archive")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.tar"), []byte("fixture archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := submitFolderRequest(t, srv, key.Key, job, incremental, "")
	if !result.Eligible || len(result.Chain.Steps) != 2 ||
		!reflect.DeepEqual(result.Effects, []string{"overwrite_matching_entries", "keep_unrelated_entries", "prune_replayed_entries"}) {
		t.Fatalf("review of chain replay: %+v", result)
	}

	withdraw := func(id string) {
		t.Helper()
		w := callRestoreRequest(srv, http.MethodPost, restoreRequestsPath+"/"+id+"/withdraw", remotePeer, key.Key, "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"withdrawn"`) {
			t.Fatalf("withdraw previous review: %d %s", w.Code, w.Body)
		}
	}
	withdraw(result.ID)
	items, err := d.GetJobItems(job)
	items = requireFixture(t, items, err)
	if err := d.UpdateJobItemSettings(items[0].ID, `{"path":"/etc"}`); err != nil {
		t.Fatal(err)
	}
	result = submitFolderRequest(t, srv, key.Key, job, incremental, "")
	if result.Eligible || !reflect.DeepEqual(result.Reasons, []string{"original_target_disallowed"}) {
		t.Errorf("review of disallowed original: %+v", result)
	}
	withdraw(result.ID)
	if err := d.UpdateJobItemSettings(items[0].ID, "{}"); err != nil {
		t.Fatal(err)
	}
	result = submitFolderRequest(t, srv, key.Key, job, incremental, "")
	if result.Eligible || !reflect.DeepEqual(result.Reasons, []string{"original_target_unknown"}) {
		t.Errorf("review of missing original: %+v", result)
	}
}

func TestRestoreRequestDatabaseWriteFailureDoesNotPublishTransition(t *testing.T) {
	d := testDB(t)
	now := time.Date(2026, 11, 11, 6, 0, 0, 1, time.UTC)
	flushes := 0
	failFlash := false
	srv := NewServer(d, ServerConfig{RestoreRequestClock: func() time.Time { return now }, RestoreRequestFlush: func() error {
		flushes++
		if failFlash {
			return errors.New("flash fixture failure")
		}
		return nil
	}})
	key := issueAssistantKey(t, srv, "Assistant")
	grantRestoreRequests(t, srv, key.ID, true)
	job, point, _, _ := requestFolderFixture(t, d)

	// SQLite triggers stand in for a failed durable write without changing
	// the service or relying on filesystem timing.
	exec := func(sql string) {
		t.Helper()
		if _, err := d.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TRIGGER refuse_insert BEFORE INSERT ON restore_requests BEGIN SELECT RAISE(ABORT, 'private fault'); END`)
	body := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"archive","write_mode":"merge"}`, job, point)
	assertUnavailable := func(w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "private fault") {
			t.Fatalf("failed database write disclosed a result: %d %s", w.Code, w.Body)
		}
	}
	assertUnavailable(callRestoreRequest(srv, http.MethodPost, restoreRequestsPath, remotePeer, key.Key, body))
	if flushes != 0 {
		t.Fatalf("failed submission attempted flash persistence: %d", flushes)
	}
	exec("DROP TRIGGER refuse_insert")
	request := submitFolderRequest(t, srv, key.Key, job, point, "")
	exec(`CREATE TRIGGER refuse_transition BEFORE UPDATE ON restore_requests BEGIN SELECT RAISE(ABORT, 'private fault'); END`)
	path := restoreRequestsPath + "/" + request.ID
	assertUnavailable(callRestoreRequest(srv, http.MethodPost, path+"/withdraw", remotePeer, key.Key, ""))
	current := callRestoreRequest(srv, http.MethodGet, path, remotePeer, key.Key, "")
	if current.Code != http.StatusOK || !strings.Contains(current.Body.String(), `"status":"pending"`) {
		t.Fatalf("failed withdrawal changed request state: %d %s", current.Code, current.Body)
	}
	now = request.ExpiresAt
	lock := srv.storageHandler.MaintenanceLock()
	lock.Lock()
	busy := callRestoreRequest(srv, http.MethodGet, path, remotePeer, key.Key, "")
	lock.Unlock()
	if busy.Code != http.StatusConflict {
		t.Fatalf("due request during maintenance: %d %s", busy.Code, busy.Body)
	}
	assertUnavailable(callRestoreRequest(srv, http.MethodGet, path, remotePeer, key.Key, ""))
	assertUnavailable(callRestoreRequest(srv, http.MethodGet, restoreRequestsPath, loopbackPeer, "", ""))
	if flushes != 1 {
		t.Fatalf("failed transitions attempted flash persistence: %d", flushes)
	}

	exec("DROP TRIGGER refuse_transition")
	failFlash = true
	if err := srv.ReconcileRestoreRequests(t.Context()); err == nil || !strings.Contains(err.Error(), "could not be saved to flash") {
		t.Fatalf("reconciliation did not report committed but unflushed expiry: %v", err)
	}
	current = callRestoreRequest(srv, http.MethodGet, path, remotePeer, key.Key, "")
	if current.Code != http.StatusOK || !strings.Contains(current.Body.String(), `"status":"expired"`) || flushes != 2 {
		t.Fatalf("working database lost expiry after flash failure: %d %s, flushes %d", current.Code, current.Body, flushes)
	}
}

func TestAssistantRestoreGrantFailsClosedAfterCommittedTombstone(t *testing.T) {
	d := testDB(t)
	dir := t.TempDir()
	cfg := ServerConfig{CredentialDir: dir}
	srv := NewServer(d, cfg)
	key := issueAssistantKey(t, srv, "Assistant")
	grantRestoreRequests(t, srv, key.ID, false) // Inspection-only is already in force.
	grantRestoreRequests(t, srv, key.ID, true)
	job, point, _, _ := requestFolderFixture(t, d)
	request := submitFolderRequest(t, srv, key.Key, job, point, "")
	requestPath := restoreRequestsPath + "/" + request.ID
	keyPath := fmt.Sprintf("%s/%d", assistantKeysPath, key.ID)

	// A failed tombstone write must not look like a successful downgrade.
	journal := filepath.Join(dir, "assistant-keys.revoked")
	if err := os.Mkdir(journal, 0o700); err != nil {
		t.Fatal(err)
	}
	removeGrant := func(s *Server) *httptest.ResponseRecorder {
		return callRestoreRequest(s, http.MethodPatch, keyPath, loopbackPeer, "", `{"restore_request":false}`)
	}
	if w := removeGrant(srv); w.Code != http.StatusInternalServerError {
		t.Fatalf("could not persist tombstone, downgrade status = %d", w.Code)
	}
	if w := callRestoreRequest(srv, http.MethodGet, requestPath, remotePeer, key.Key, ""); w.Code != http.StatusOK {
		t.Fatalf("failed downgrade incorrectly removed current grant: %d %s", w.Code, w.Body)
	}
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}

	// With the journal back, fail the subsequent SQLite cleanup. The
	// successful tombstone must remain authoritative across restart.
	if _, err := d.Exec(`CREATE TRIGGER refuse_grant_update BEFORE UPDATE ON assistant_keys BEGIN SELECT RAISE(ABORT, 'private fault'); END`); err != nil {
		t.Fatal(err)
	}
	if w := removeGrant(srv); w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "private fault") {
		t.Fatalf("failed SQLite grant cleanup: %d %s", w.Code, w.Body)
	}
	restarted := NewServer(d, cfg)
	for _, server := range []*Server{srv, restarted} {
		if w := callRestoreRequest(server, http.MethodGet, requestPath, remotePeer, key.Key, ""); w.Code != http.StatusForbidden {
			t.Fatalf("committed tombstone did not deny ownership access: %d %s", w.Code, w.Body)
		}
		if w := callRestoreRequest(server, http.MethodGet, "/api/v1/recovery/catalog", remotePeer, key.Key, ""); w.Code != http.StatusOK {
			t.Fatalf("permission tombstone revoked inspection: %d %s", w.Code, w.Body)
		}
	}
}

func TestRestoreRequestReportsMissingStorageAndDoesNotAuthorizeMalformedIDs(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Assistant")
	grantRestoreRequests(t, srv, key.ID, true)
	job, point, _, _ := requestFolderFixture(t, d)
	config, err := d.GetJob(job)
	config = requireFixture(t, config, err)
	if err := d.DeleteStorageDestination(config.StorageDestID); err != nil {
		t.Fatal(err)
	}
	operation := submitFolderRequest(t, srv, key.Key, job, point, "")
	if operation.Eligible || !strings.Contains(strings.Join(operation.Reasons, ","), "required_data_unusable") {
		t.Fatalf("missing destination was not reported: %+v", operation)
	}
	if w := callRestoreRequest(srv, http.MethodGet, restoreRequestsPath+"/bad-id", remotePeer, key.Key, ""); w.Code != http.StatusNotFound {
		t.Errorf("follow malformed request ID: %d %s", w.Code, w.Body)
	}
	if w := callRestoreRequest(srv, http.MethodPost, restoreRequestsPath+"/bad-id/withdraw", remotePeer, key.Key, ""); w.Code != http.StatusNotFound {
		t.Errorf("withdraw malformed request ID: %d %s", w.Code, w.Body)
	}
}

func TestRestoreRequestCorruptStoredReviewFailsClosedAcrossTransports(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Assistant")
	grantRestoreRequests(t, srv, key.ID, true)
	job, point, _, _ := requestFolderFixture(t, d)
	saved := submitFolderRequest(t, srv, key.Key, job, point, "")
	if _, err := d.Exec("UPDATE restore_requests SET operation = ? WHERE id = ?", `{"fixture-private-operation"`, saved.ID); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{restoreRequestsPath + "/" + saved.ID, restoreRequestsPath} {
		w := callRestoreRequest(srv, http.MethodGet, url, loopbackPeer, "", "")
		if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "fixture-private-operation") {
			t.Errorf("corrupt REST result: %d %s", w.Code, w.Body)
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = remotePeer
		srv.router.ServeHTTP(w, r)
	}))
	defer ts.Close()
	defer srv.mcp.Close()
	client := assistantMCPClient{t: t, baseURL: ts.URL + "/api/v1/mcp", key: key.Key}
	client.initialize()
	status, body := client.post(fmt.Sprintf(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"get_restore_request","arguments":{"id":%q}}}`, saved.ID))
	if status != http.StatusOK || !strings.Contains(body, `"isError":true`) || strings.Contains(body, "fixture-private-operation") {
		t.Errorf("corrupt MCP result: %d %s", status, body)
	}
}

func TestAssistantMCPRefreshesScopeWhenPersistentPermissionChanges(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Assistant")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = remotePeer
		srv.router.ServeHTTP(w, r)
	}))
	defer ts.Close()
	defer srv.mcp.Close()
	client := assistantMCPClient{t: t, baseURL: ts.URL + "/api/v1/mcp", key: key.Key}
	client.initialize()

	// This server persists the grant without access to the transport
	// registry that owns the existing inspection session.
	writer := NewServer(d, ServerConfig{})
	grantRestoreRequests(t, writer, key.ID, true)
	list := `{"jsonrpc":"2.0","id":10,"method":"tools/list"}`
	if status, _ := client.post(list); status != http.StatusNotFound {
		t.Fatalf("stale inspection session after grant = %d, want 404", status)
	}
	client.session = ""
	client.initialize()
	if status, body := client.post(list); status != http.StatusOK || !strings.Contains(body, `"name":"submit_restore_request"`) {
		t.Fatalf("new session did not reflect persisted grant: %d %s", status, body)
	}
}

func TestRestoreRequestReusesPendingSubmissionAcrossDatabaseHandles(t *testing.T) {
	d := testDB(t)
	var flushes atomic.Int64
	config := ServerConfig{RestoreRequestFlush: func() error {
		flushes.Add(1)
		return nil
	}}
	srv := NewServer(d, config)
	key := issueAssistantKey(t, srv, "Assistant")
	grantRestoreRequests(t, srv, key.ID, true)
	job, point, _, _ := requestFolderFixture(t, d)
	other, err := db.Open(d.Path())
	other = requireFixture(t, other, err)
	defer other.Close()
	otherServer := NewServer(other, config)
	payload := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"archive","write_mode":"merge"}`, job, point)
	results := make(chan *httptest.ResponseRecorder, 2)
	var calls sync.WaitGroup
	for _, server := range []*Server{srv, otherServer} {
		calls.Go(func() {
			results <- callRestoreRequest(server, http.MethodPost, restoreRequestsPath, remotePeer, key.Key, payload)
		})
	}
	calls.Wait()
	close(results)
	observed := map[int]restorerequest.View{}
	for w := range results {
		var result restorerequest.View
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.ID == "" {
			t.Fatalf("concurrent submission: %d %s", w.Code, w.Body)
		}
		observed[w.Code] = result
	}
	first := observed[http.StatusCreated]
	reused := observed[http.StatusOK]
	if first.ID == "" || reused.ID != first.ID || !reused.ExpiresAt.Equal(first.ExpiresAt) || flushes.Load() != 1 {
		t.Fatalf("duplicate changed the request or flash writes: results %+v, flushes %d", observed, flushes.Load())
	}
	if err := d.Reopen(); err != nil {
		t.Fatal(err)
	}
	w := callRestoreRequest(srv, http.MethodPost, restoreRequestsPath, remotePeer, key.Key, payload)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), first.ID) || flushes.Load() != 1 {
		t.Fatalf("repeat after reopen: %d %s, flushes %d", w.Code, w.Body, flushes.Load())
	}
	w = callRestoreRequest(srv, http.MethodGet, restoreRequestsPath, loopbackPeer, "", "")
	var pending restorerequest.Page
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &pending) != nil || len(pending.Requests) != 1 {
		t.Fatalf("duplicate grew the pending list: %d %s", w.Code, w.Body)
	}
}

func TestRestoreRequestLimitsBoundDurableAcceptanceAcrossRESTAndMCP(t *testing.T) {
	d := testDB(t)
	start := time.Date(2026, 11, 11, 6, 0, 0, 1, time.UTC)
	var clock, flushes atomic.Int64
	clock.Store(start.UnixNano())
	config := ServerConfig{RestoreRequestClock: func() time.Time { return time.Unix(0, clock.Load()) }, RestoreRequestFlush: func() error {
		flushes.Add(1)
		return nil
	}}
	srv := NewServer(d, config)
	key := issueAssistantKey(t, srv, "Assistant")
	grantRestoreRequests(t, srv, key.ID, true)
	job, point, root, _ := requestFolderFixture(t, d)
	target := func(index int) string { return filepath.Join(root, fmt.Sprintf("target-%d", index)) }
	payload := func(index int) string {
		return fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"archive","write_mode":"merge","destination":%q}`, job, point, target(index))
	}
	post := func(server *Server, index int) *httptest.ResponseRecorder {
		return callRestoreRequest(server, http.MethodPost, restoreRequestsPath, remotePeer, key.Key, payload(index))
	}
	var accepted []restorerequest.View
	for index := range 5 {
		accepted = append(accepted, submitFolderRequest(t, srv, key.Key, job, point, fmt.Sprintf(`,"destination":%q`, target(index))))
	}
	if w := post(srv, 5); w.Code != http.StatusTooManyRequests || flushes.Load() != 5 {
		t.Fatalf("pending cap: %d %s, flushes %d", w.Code, w.Body, flushes.Load())
	}
	if w := post(srv, 0); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), accepted[0].ID) || flushes.Load() != 5 {
		t.Fatalf("pending retry at cap: %d %s, flushes %d", w.Code, w.Body, flushes.Load())
	}
	withdraw := func(id string) {
		t.Helper()
		w := callRestoreRequest(srv, http.MethodPost, restoreRequestsPath+"/"+id+"/withdraw", remotePeer, key.Key, "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"withdrawn"`) {
			t.Fatalf("withdraw request %s: %d %s", id, w.Code, w.Body)
		}
	}
	withdraw(accepted[0].ID)
	submitFolderRequest(t, srv, key.Key, job, point, fmt.Sprintf(`,"destination":%q`, target(5)))
	withdraw(accepted[1].ID)

	if err := d.Reopen(); err != nil {
		t.Fatal(err)
	}
	restarted := NewServer(d, config)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = remotePeer
		restarted.router.ServeHTTP(w, r)
	}))
	defer ts.Close()
	defer restarted.mcp.Close()
	client := assistantMCPClient{t: t, baseURL: ts.URL + "/api/v1/mcp", key: key.Key}
	client.initialize()
	status, body := client.post(fmt.Sprintf(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"submit_restore_request","arguments":%s}}`, payload(6)))
	if status != http.StatusOK || !strings.Contains(body, `"isError":true`) || !strings.Contains(body, "limit reached") || flushes.Load() != 8 {
		t.Fatalf("rolling cap after reconnect and reopen: %d %s, flushes %d", status, body, flushes.Load())
	}
	clock.Store(start.Add(time.Hour).UnixNano())
	if w := post(restarted, 6); w.Code != http.StatusCreated || flushes.Load() != 9 {
		t.Fatalf("rolling allowance did not renew: %d %s, flushes %d", w.Code, w.Body, flushes.Load())
	}
}

func TestRestoreRequestRetriesReuseTheOriginalReviewAfterEvidenceChanges(t *testing.T) {
	d := testDB(t)
	flushes := 0
	srv := NewServer(d, ServerConfig{RestoreRequestFlush: func() error {
		flushes++
		return nil
	}})
	key := issueAssistantKey(t, srv, "Assistant")
	grantRestoreRequests(t, srv, key.ID, true)
	job, point, _, target := requestFolderFixture(t, d)
	created := submitFolderRequest(t, srv, key.Key, job, point, fmt.Sprintf(`,"destination":%q`, target+"/"))
	readReview := func() restorerequest.View {
		t.Helper()
		w := callRestoreRequest(srv, http.MethodGet, restoreRequestsPath+"/"+created.ID, loopbackPeer, "", "")
		var review restorerequest.View
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &review) != nil || review.ReviewDigest == "" {
			t.Fatalf("operator review: %d %s", w.Code, w.Body)
		}
		return review
	}
	original := readReview()

	verification, err := d.CreateVerifyRun(point, "quick")
	verification = requireFixture(t, verification, err)
	if err := d.FinishVerifyRun(verification, "passed", ""); err != nil {
		t.Fatal(err)
	}
	freshEvidence := callRestoreRequest(srv, http.MethodGet, fmt.Sprintf("/api/v1/recovery/restore-points/%d/evidence%s", point, evidenceQuery), remotePeer, key.Key, "")
	if freshEvidence.Code != http.StatusOK || !strings.Contains(freshEvidence.Body.String(), `"status":"passed"`) {
		t.Fatalf("updated verification evidence: %d %s", freshEvidence.Code, freshEvidence.Body)
	}

	body := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_name":"archive","item_type":"folder","write_mode":"merge","destination":%q}`, job, point, target)
	w := callRestoreRequest(srv, http.MethodPost, restoreRequestsPath, remotePeer, key.Key, body)
	var reused restorerequest.View
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &reused) != nil || !reused.Reused ||
		reused.ID != created.ID || !reused.ExpiresAt.Equal(created.ExpiresAt) || flushes != 1 {
		t.Fatalf("retry changed request identity, expiry or persistence: %d %s, flushes %d", w.Code, w.Body, flushes)
	}
	if after := readReview(); after.ReviewDigest != original.ReviewDigest || after.Verification.Status != original.Verification.Status {
		t.Fatalf("retry changed the review the operator would approve: before %+v, after %+v", original, after)
	}
}

func TestRestoreRequestWithdrawalRetryKeepsWarningAndBoundsFlashWrites(t *testing.T) {
	d := testDB(t)
	now := time.Date(2026, 11, 11, 6, 0, 0, 1, time.UTC)
	flushes := 0
	flashUnavailable := false
	cfg := ServerConfig{RestoreRequestClock: func() time.Time { return now }, RestoreRequestFlush: func() error {
		flushes++
		if flashUnavailable {
			return errors.New("fixture private flash error")
		}
		return nil
	}}
	srv := NewServer(d, cfg)
	key := issueAssistantKey(t, srv, "Assistant")
	grantRestoreRequests(t, srv, key.ID, true)
	job, point, _, _ := requestFolderFixture(t, d)
	request := submitFolderRequest(t, srv, key.Key, job, point, "")
	path := restoreRequestsPath + "/" + request.ID
	check := func(method, suffix string, warned bool, wantFlushes int) {
		t.Helper()
		w := callRestoreRequest(srv, method, path+suffix, remotePeer, key.Key, "")
		var result restorerequest.View
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil ||
			result.Status != "withdrawn" || (result.PersistenceWarning != "") != warned ||
			strings.Contains(w.Body.String(), "fixture private") || flushes != wantFlushes {
			t.Fatalf("withdrawal persistence: %d %s, flushes %d, want %d", w.Code, w.Body, flushes, wantFlushes)
		}
	}
	flashUnavailable = true
	check(http.MethodPost, "/withdraw", true, 2)
	check(http.MethodPost, "/withdraw", true, 2)
	check(http.MethodGet, "", true, 2)

	if err := d.Reopen(); err != nil {
		t.Fatal(err)
	}
	srv = NewServer(d, cfg)
	check(http.MethodPost, "/withdraw", true, 2)
	now = now.Add(time.Minute)
	check(http.MethodPost, "/withdraw", true, 3)
	check(http.MethodPost, "/withdraw", true, 3)

	flashUnavailable = false
	now = now.Add(time.Minute)
	check(http.MethodPost, "/withdraw", false, 4)
	check(http.MethodGet, "", false, 4)
	check(http.MethodPost, "/withdraw", false, 4)
}
