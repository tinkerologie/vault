package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/recovery"
)

const assistantKeysPath = "/api/v1/settings/assistant-keys"

type issuedAssistantKey struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Key  string `json:"assistant_key"`
}

func issueAssistantKey(t *testing.T, srv *Server, name string) issuedAssistantKey {
	t.Helper()
	req := asLocal(httptest.NewRequest(http.MethodPost, assistantKeysPath, strings.NewReader(`{"name":"`+name+`"}`)))
	w := serve(srv, req)
	if w.Code != http.StatusCreated || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create status = %d, cache = %q", w.Code, w.Header().Get("Cache-Control"))
	}
	var key issuedAssistantKey
	if err := json.Unmarshal(w.Body.Bytes(), &key); err != nil {
		t.Fatal(err)
	}
	if key.ID <= 0 || len(key.Key) != 52 || !strings.HasPrefix(key.Key, "vault_ak_") {
		t.Fatal("create did not return a valid Assistant Key")
	}
	return key
}

func TestIssueAssistantKeyDisclosesTokenOnlyOnce(t *testing.T) {
	srv := NewServer(testDB(t), ServerConfig{})
	key := issueAssistantKey(t, srv, "  Recovery advisor  ")
	if key.Name != "Recovery advisor" {
		t.Fatalf("issued name = %q", key.Name)
	}
	w := serve(srv, asLocal(httptest.NewRequest(http.MethodGet, assistantKeysPath, nil)))
	body := w.Body.String()
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(body, "Recovery advisor") || !strings.Contains(body, "created_at") {
		t.Fatalf("list keys status = %d", w.Code)
	}
	if strings.Contains(body, key.Key) || strings.Contains(body, "assistant_key") || strings.Contains(body, "digest") {
		t.Fatal("key list exposed credential material")
	}
}

func TestAssistantKeyCanOnlyReadRecoveryCatalog(t *testing.T) {
	srv := NewServer(testDB(t), ServerConfig{})
	key := issueAssistantKey(t, srv, "Catalog reader")
	headers := map[string]string{"X-API-Key": key.Key}
	for _, peer := range []string{remotePeer, loopbackPeer, "[::1]:40000"} {
		w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/recovery/catalog", peer, daemonHost, headers))
		if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"jobs":[]}` {
			t.Errorf("empty catalogue for %s: status %d", peer, w.Code)
		}
		for _, route := range []struct {
			method string
			path   string
		}{
			{http.MethodGet, "/api/v1/settings/api-key/key"},
			{http.MethodGet, "/api/v1/storage/1/files"},
			{http.MethodGet, assistantKeysPath},
			{http.MethodGet, "/api/v1/no-such-route"},
			{http.MethodPost, "/api/v1/recovery/catalog"},
			{http.MethodPost, "/api/v1/jobs/1/restore"},
			{http.MethodPost, "/api/v1/recovery/requests"},
		} {
			w := serve(srv, boundaryRequest(route.method, route.path, peer, daemonHost, headers))
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s for %s: status = %d, want 403", route.method, route.path, peer, w.Code)
			}
		}
	}
	for _, key := range []string{"vault_ak_malformed", "vault_ak_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/recovery/catalog", loopbackPeer, daemonHost, map[string]string{"X-API-Key": key}))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("invalid credential: status = %d, want 401", w.Code)
		}
	}
}

func TestRevokedAssistantKeyStaysDeniedAfterDatabaseRollback(t *testing.T) {
	database := testDB(t)
	setTestAPIKey(t, database)
	credentialDir := t.TempDir()
	cfg := ServerConfig{CredentialDir: credentialDir}
	srv := NewServer(database, cfg)
	key := issueAssistantKey(t, srv, "Revoke me")
	other := issueAssistantKey(t, srv, "Keep me")
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := database.BackupToFile(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}

	deletePath := fmt.Sprintf("%s/%d", assistantKeysPath, key.ID)
	revoke := func() *httptest.ResponseRecorder {
		return serve(srv, asLocal(httptest.NewRequest(http.MethodDelete, deletePath, nil)))
	}
	lock := srv.storageHandler.MaintenanceLock()
	lock.Lock()
	busy := revoke()
	lock.Unlock()
	if busy.Code != http.StatusConflict {
		t.Fatalf("revoke during DB maintenance = %d, want 409", busy.Code)
	}
	if w := revoke(); w.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204", w.Code)
	}
	readCatalog := func(srv *Server, key, peer string) int {
		return serve(srv, boundaryRequest(http.MethodGet, "/api/v1/recovery/catalog", peer, daemonHost,
			map[string]string{"X-API-Key": key})).Code
	}
	for _, peer := range []string{remotePeer, loopbackPeer} {
		if got := readCatalog(srv, key.Key, peer); got != http.StatusUnauthorized {
			t.Errorf("revoked key from %s = %d, want 401", peer, got)
		}
	}
	if got := readCatalog(srv, other.Key, remotePeer); got != http.StatusOK {
		t.Errorf("other key = %d, want 200", got)
	}

	rolledBack, err := db.Open(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer rolledBack.Close()
	restarted := NewServer(rolledBack, cfg)
	if got := readCatalog(restarted, key.Key, remotePeer); got != http.StatusUnauthorized {
		t.Errorf("revoked key after rollback = %d, want 401", got)
	}
	if got := readCatalog(restarted, other.Key, remotePeer); got != http.StatusOK {
		t.Errorf("other key after rollback = %d, want 200", got)
	}

	if err := os.WriteFile(filepath.Join(credentialDir, "assistant-keys.revoked"), []byte("corrupt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted = NewServer(rolledBack, cfg)
	if got := readCatalog(restarted, other.Key, loopbackPeer); got != http.StatusUnauthorized {
		t.Errorf("key with corrupt revocation ledger = %d, want 401", got)
	}
	permissionURL := fmt.Sprintf("%s/%d", assistantKeysPath, other.ID)
	if w := callRestoreRequest(restarted, http.MethodPatch, permissionURL, loopbackPeer, "", `{"restore_request":true}`); w.Code != http.StatusInternalServerError {
		t.Fatalf("grant with a corrupt revocation ledger: %d", w.Code)
	}
	if got := readCatalog(restarted, "", loopbackPeer); got != http.StatusOK {
		t.Errorf("operator with corrupt revocation ledger = %d, want 200", got)
	}
}

func TestAssistantMCPExposesOnlyRecoveryInspection(t *testing.T) {
	srv := NewServer(testDB(t), ServerConfig{})
	key := issueAssistantKey(t, srv, "MCP catalog")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = remotePeer
		srv.router.ServeHTTP(w, r)
	}))
	defer ts.Close()
	defer srv.mcp.Close()

	mcp := &assistantMCPClient{t: t, baseURL: ts.URL + "/api/v1/mcp", key: key.Key}
	mcp.initialize()
	status, body := mcp.post(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if status != http.StatusOK || !strings.Contains(body, `"name":"list_recovery_catalog"`) || !strings.Contains(body, `"name":"find_restore_points"`) || !strings.Contains(body, `"name":"get_recovery_evidence"`) || strings.Count(body, `"inputSchema"`) != 3 {
		t.Fatalf("scoped tools/list = %d: %s", status, body)
	}
	status, body = mcp.post(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_recovery_catalog","arguments":{"limit_per_job":1001}}}`)
	if status != http.StatusOK || !strings.Contains(body, `"isError":true`) {
		t.Errorf("MCP invalid catalog limit = %d: %s", status, body)
	}
	for _, tool := range []string{"restore_item", "delete_job", "run_job", "list_storage_files"} {
		status, body = mcp.post(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, tool))
		if status != http.StatusOK || !strings.Contains(body, `"error"`) {
			t.Errorf("scoped tools/call %s = %d: %s", tool, status, body)
		}
	}
}

type assistantMCPClient struct {
	t       *testing.T
	baseURL string
	key     string
	session string
}

func (c *assistantMCPClient) request(method, body string) *http.Response {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 5*time.Second)
	c.t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-API-Key", c.key)
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return res
}

func (c *assistantMCPClient) initialize() {
	c.t.Helper()
	res := c.request(http.MethodPost, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"assistant-test","version":"1"}}}`)
	c.session = res.Header.Get("Mcp-Session-Id")
	_, err := io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || c.session == "" || err != nil {
		c.t.Fatalf("initialize: status %d, session issued %t, read %v", res.StatusCode, c.session != "", err)
	}
	status, _ := c.post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if status != http.StatusAccepted {
		c.t.Fatalf("initialized notification status %d, want 202", status)
	}
}

func (c *assistantMCPClient) post(body string) (int, string) {
	c.t.Helper()
	res := c.request(http.MethodPost, body)
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return res.StatusCode, string(data)
}

func TestAssistantKeyAuthenticationRejectsAmbiguityAndLimitsFailures(t *testing.T) {
	srv := NewServer(testDB(t), ServerConfig{})
	key := issueAssistantKey(t, srv, "Limited key")
	bad := boundaryRequest(http.MethodGet, "/api/v1/jobs", loopbackPeer, daemonHost, nil)
	bad.Header.Add("X-API-Key", "arbitrary-first-value")
	bad.Header.Add("X-API-Key", key.Key)
	if w := serve(srv, bad); w.Code != http.StatusUnauthorized {
		t.Fatalf("ambiguous key headers = %d, want 401", w.Code)
	}

	for attempt := 1; attempt <= 31; attempt++ {
		w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/jobs", remotePeer, daemonHost,
			map[string]string{"X-API-Key": "vault_ak_invalid"}))
		want := http.StatusUnauthorized
		if attempt == 31 {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("failed attempt %d = %d, want %d", attempt, w.Code, want)
		}
	}

	w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/recovery/catalog", remotePeer, daemonHost,
		map[string]string{"X-API-Key": key.Key}))
	if w.Code != http.StatusOK {
		t.Fatalf("valid key after throttle = %d, want 200", w.Code)
	}
}

func TestLegacyAdminKeyWithAssistantPrefixRequiresAuthentication(t *testing.T) {
	database := testDB(t)
	key := "vault_ak_" + strings.Repeat("A", 40)
	hash, err := crypto.HashPassphrase(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetSetting("api_key_hash", hash); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(database, ServerConfig{})
	for _, peer := range []string{remotePeer, loopbackPeer} {
		w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/jobs", peer, daemonHost, map[string]string{"X-API-Key": key}))
		if w.Code != http.StatusOK {
			t.Errorf("verified legacy API Key from %s = %d, want 200", peer, w.Code)
		}
	}
	w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/jobs", loopbackPeer, daemonHost,
		map[string]string{"X-API-Key": "vault_ak_" + strings.Repeat("B", 39) + "A"}))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong legacy-shaped key on loopback = %d, want 401", w.Code)
	}
}

func TestOnlineDatabaseRestoreKeepsCurrentAssistantKeys(t *testing.T) {
	database := testDB(t)
	srv := NewServer(database, ServerConfig{})
	dir := t.TempDir()
	config, err := json.Marshal(map[string]string{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	dest, err := database.CreateStorageDestination(db.StorageDestination{Name: "Restore source", Type: "local", Config: string(config)})
	if err != nil {
		t.Fatal(err)
	}
	old := issueAssistantKey(t, srv, "Snapshot key")
	if err := database.BackupToFile(t.Context(), filepath.Join(dir, "snapshot.db")); err != nil {
		t.Fatal(err)
	}
	current := issueAssistantKey(t, srv, "Live key")
	grantRestoreRequests(t, srv, current.ID, true)
	revoke := func(key issuedAssistantKey) {
		t.Helper()
		url := fmt.Sprintf("%s/%d", assistantKeysPath, key.ID)
		if w := serve(srv, asLocal(httptest.NewRequest(http.MethodDelete, url, nil))); w.Code != http.StatusNoContent {
			t.Fatalf("revoke status = %d, want 204", w.Code)
		}
	}
	restore := func() {
		t.Helper()
		url := fmt.Sprintf("/api/v1/storage/%d/restore-db", dest)
		w := serve(srv, asLocal(httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{"storage_path":"snapshot.db"}`))))
		if w.Code != http.StatusOK {
			t.Fatalf("restore DB status = %d: %s", w.Code, w.Body)
		}
	}
	revoke(old)
	restore()
	w := serve(srv, asLocal(httptest.NewRequest(http.MethodGet, assistantKeysPath, nil)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), current.Name) || !strings.Contains(w.Body.String(), `"restore_request":true`) || strings.Contains(w.Body.String(), old.Name) {
		t.Fatalf("current list after restore status = %d", w.Code)
	}
	for _, check := range []struct {
		key  issuedAssistantKey
		want int
	}{{current, http.StatusOK}, {old, http.StatusUnauthorized}} {
		w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/recovery/catalog", remotePeer, daemonHost,
			map[string]string{"X-API-Key": check.key.Key}))
		if w.Code != check.want {
			t.Errorf("key after restore status = %d, want %d", w.Code, check.want)
		}
	}

	revoke(current)
	restore()
	w = serve(srv, asLocal(httptest.NewRequest(http.MethodGet, assistantKeysPath, nil)))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("list after restoring with zero current keys = %d", w.Code)
	}
	newKey := issueAssistantKey(t, srv, "After restore")
	if newKey.ID <= current.ID {
		t.Fatal("restoring an older database reused an Assistant Key ID")
	}
}

func TestAssistantMCPSessionIsolationAndRevocation(t *testing.T) {
	database := testDB(t)
	adminKey := setTestAPIKey(t, database)
	srv := NewServer(database, ServerConfig{})
	key := issueAssistantKey(t, srv, "First assistant")
	other := issueAssistantKey(t, srv, "Second assistant")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "" {
			r.RemoteAddr = remotePeer
		}
		srv.router.ServeHTTP(w, r)
	}))
	defer ts.Close()
	defer srv.mcp.Close()
	newClient := func(key string) assistantMCPClient {
		client := assistantMCPClient{t: t, baseURL: ts.URL + "/api/v1/mcp", key: key}
		client.initialize()
		return client
	}
	first := newClient(key.Key)
	second := newClient(other.Key)
	admin := newClient(adminKey)
	local := newClient("")
	list := `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`
	for _, operator := range []assistantMCPClient{admin, local} {
		status, body := operator.post(list)
		if status != http.StatusOK || !strings.Contains(body, `"name":"restore_item"`) || !strings.Contains(body, `"name":"list_recovery_catalog"`) {
			t.Errorf("operator tools list status = %d; expected existing tools and the catalog", status)
		}
	}
	for _, cross := range []struct{ key, session string }{
		{other.Key, first.session}, {key.Key, admin.session}, {adminKey, first.session}, {"", first.session},
	} {
		client := assistantMCPClient{t: t, baseURL: first.baseURL, key: cross.key, session: cross.session}
		if status, _ := client.post(list); status != http.StatusNotFound {
			t.Errorf("foreign session = %d, want 404", status)
		}
	}

	stream := first.request(http.MethodGet, "")
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("open event stream = %d, want 200", stream.StatusCode)
	}
	url := fmt.Sprintf("%s/%d", assistantKeysPath, key.ID)
	if w := serve(srv, asLocal(httptest.NewRequest(http.MethodDelete, url, nil))); w.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204", w.Code)
	}
	if _, err := io.Copy(io.Discard, stream.Body); err != nil {
		t.Fatalf("revoked key's stream did not close cleanly: %v", err)
	}
	if status, _ := first.post(list); status != http.StatusUnauthorized {
		t.Errorf("revoked session = %d, want 401", status)
	}
	if status, _ := second.post(list); status != http.StatusOK {
		t.Errorf("other key's existing session = %d, want 200", status)
	}
}

func requireFixture[T any](t *testing.T, v T, err error) T {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAssistantCatalogMatchesRESTAndLeavesBackupStateUnchanged(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Recovery inventory")
	root := t.TempDir()
	storageFile := filepath.Join(root, "backup-content.txt")
	if err := os.WriteFile(storageFile, []byte("fixture backup content"), 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := json.Marshal(map[string]string{"path": root, "password": "fixture-storage-password"})
	destinationConfig := requireFixture(t, config, err)
	id, err := d.CreateStorageDestination(db.StorageDestination{Name: "Local", Type: "local", Config: string(destinationConfig)})
	destID := requireFixture(t, id, err)
	id, err = d.CreateJob(db.Job{Name: "Alpha", Enabled: true, StorageDestID: destID, PreScript: "fixture-private-script"})
	job := requireFixture(t, id, err)
	id, err = d.CreateJob(db.Job{Name: "Beta", StorageDestID: destID})
	other := requireFixture(t, id, err)
	_, err = d.AddJobItem(db.JobItem{JobID: job, ItemType: "folder", ItemName: "Current selection", ItemID: "current", Settings: `{"path":"/fixture-private-path"}`})
	if err != nil {
		t.Fatal(err)
	}

	point := func(job, parent int64, kind, metadata string) int64 {
		t.Helper()
		run, err := d.CreateJobRun(db.JobRun{JobID: job, Status: "success", BackupType: kind})
		rp, err := d.CreateRestorePoint(db.RestorePoint{
			JobID: job, JobRunID: requireFixture(t, run, err), BackupType: kind, ParentRestorePointID: parent,
			SizeBytes: 12, StoragePath: "fixture-private-storage-path", Metadata: metadata,
		})
		return requireFixture(t, rp, err)
	}
	full := point(job, 0, "full", `{"item_sizes":{"Historical folder":12},"internal":"fixture-private-metadata"}`)
	incr := point(job, full, "incremental", `{"item_manifests":{"Historical folder":"fixture-private-manifest","  \t/fixture-legacy-source-path":"another-manifest"}}`)
	unknown := point(other, 0, "full", `{}`)
	_, err = d.CreateVerifyRun(incr, "quick")
	if err != nil {
		t.Fatal(err)
	}

	capture := func() []any {
		t.Helper()
		jobs, err := d.ListJobs()
		result := []any{requireFixture(t, jobs, err)}
		settings, err := d.GetAllSettings()
		result = append(result, requireFixture(t, settings, err))
		destinations, err := d.ListStorageDestinations()
		result = append(result, requireFixture(t, destinations, err))
		for _, id := range []int64{job, other} {
			items, err := d.GetJobItems(id)
			result = append(result, requireFixture(t, items, err))
			runs, err := d.GetJobRuns(id, 20)
			result = append(result, requireFixture(t, runs, err))
			points, err := d.ListRestorePoints(id)
			result = append(result, requireFixture(t, points, err))
			verify, err := d.ListVerifyRunsForJob(id, 20)
			result = append(result, requireFixture(t, verify, err))
		}
		files := make(map[string]string)
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				files[path] = string(data)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return append(result, files)
	}
	before := capture()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = remotePeer
		srv.router.ServeHTTP(w, r)
	}))
	defer ts.Close()
	defer srv.mcp.Close()
	client := assistantMCPClient{t: t, baseURL: ts.URL + "/api/v1/mcp", key: key.Key}
	client.initialize()
	status, sse := client.post(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"list_recovery_catalog","arguments":{"limit_per_job":1}}}`)
	_, payload, found := strings.Cut(sse, "data: ")
	if status != http.StatusOK || !found {
		t.Fatalf("MCP catalog status = %d, result payload present %t", status, found)
	}
	var rpc struct {
		Result struct {
			StructuredContent recovery.Catalog `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(payload), &rpc); err != nil {
		t.Fatal(err)
	}
	restPath := "/api/v1/recovery/catalog?limit_per_job=1"
	w := serve(srv, boundaryRequest(http.MethodGet, restPath, remotePeer, daemonHost, map[string]string{"X-API-Key": key.Key}))
	if w.Code != http.StatusOK {
		t.Fatalf("REST catalog status = %d", w.Code)
	}
	var rest recovery.Catalog
	if err := json.Unmarshal(w.Body.Bytes(), &rest); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rpc.Result.StructuredContent, rest) {
		t.Fatal("REST and MCP returned different catalog projections")
	}
	if len(rest.Jobs) != 2 || !rest.Jobs[0].Truncated || len(rest.Jobs[0].RestorePoints) != 1 || len(rest.Jobs[1].RestorePoints) != 1 {
		t.Fatalf("wrong Job and Restore Point limits: %+v", rest)
	}
	p := rest.Jobs[0].RestorePoints[0]
	if p.ID != incr || p.ParentRestorePointID != full || p.ChainStatus != "healthy" || p.ChainDepth != 2 ||
		p.Membership != "recorded" || !reflect.DeepEqual(p.ItemNames, []string{"Historical folder", "[redacted]"}) {
		t.Fatalf("wrong incremental chain or item membership: %+v", p)
	}
	p = rest.Jobs[1].RestorePoints[0]
	if p.ID != unknown || p.Membership != "unknown" || !reflect.DeepEqual(p.ItemNames, []string{}) {
		t.Fatalf("wrong legacy membership: %+v", p)
	}
	for _, secret := range []string{"fixture-private-", "fixture-storage-password", "fixture-legacy-source-path", "fixture backup content", "Current selection"} {
		if strings.Contains(sse, secret) || strings.Contains(w.Body.String(), secret) {
			t.Errorf("catalog exposed forbidden fixture marker %q", secret)
		}
	}
	if !reflect.DeepEqual(before, capture()) {
		t.Fatal("catalog inspection changed backup state")
	}
}

func TestAssistantKeyManagementFailureBoundaries(t *testing.T) {
	database := testDB(t)
	adminKey := setTestAPIKey(t, database)
	dir := t.TempDir()
	srv := NewServer(database, ServerConfig{CredentialDir: dir})
	key := issueAssistantKey(t, srv, "Recovery advisor")

	for i, tc := range []struct {
		body string
		want int
	}{
		{`{"name":`, http.StatusBadRequest},
		{`{"name":" "}`, http.StatusBadRequest},
		{`{"name":"Recovery advisor"}`, http.StatusConflict},
	} {
		req := asLocal(httptest.NewRequest(http.MethodPost, assistantKeysPath, strings.NewReader(tc.body)))
		req.RemoteAddr = fmt.Sprintf("127.0.0.%d:40000", i+2)
		if w := serve(srv, req); w.Code != tc.want {
			t.Errorf("create fixture %d = %d, want %d", i, w.Code, tc.want)
		}
	}
	lock := srv.storageHandler.MaintenanceLock()
	lock.Lock()
	busy := serve(srv, asLocal(httptest.NewRequest(http.MethodPost, assistantKeysPath, strings.NewReader(`{"name":"Busy"}`))))
	lock.Unlock()
	if busy.Code != http.StatusConflict {
		t.Errorf("create during maintenance = %d, want 409", busy.Code)
	}

	revoke := func(id string) int {
		t.Helper()
		return serve(srv, asLocal(httptest.NewRequest(http.MethodDelete, assistantKeysPath+"/"+id, nil))).Code
	}
	if got := revoke("bad"); got != http.StatusBadRequest {
		t.Errorf("invalid revoke ID = %d, want 400", got)
	}
	if got := revoke("999"); got != http.StatusNotFound {
		t.Errorf("unknown revoke ID = %d, want 404", got)
	}

	adminList := func() *httptest.ResponseRecorder {
		t.Helper()
		return serve(srv, boundaryRequest(http.MethodGet, assistantKeysPath, remotePeer, daemonHost, map[string]string{"X-API-Key": adminKey}))
	}
	keyStatus := func() int {
		t.Helper()
		return serve(srv, boundaryRequest(http.MethodGet, "/api/v1/recovery/catalog", remotePeer, daemonHost, map[string]string{"X-API-Key": key.Key})).Code
	}
	ledger := filepath.Join(dir, "assistant-keys.revoked")
	if err := os.Mkdir(ledger, 0o700); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprint(key.ID)
	if got := revoke(id); got != http.StatusInternalServerError {
		t.Fatalf("revoke with unwritable ledger = %d, want 500", got)
	}
	if w := adminList(); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), key.Name) || strings.Contains(w.Body.String(), key.Key) {
		t.Fatal("an unwritten revocation should leave a redacted record")
	}
	if got := keyStatus(); got != http.StatusOK {
		t.Errorf("key after failed revocation persistence = %d, want 200", got)
	}
	if err := os.Remove(ledger); err != nil {
		t.Fatal(err)
	}

	if _, err := database.Exec(`CREATE TRIGGER refuse_assistant_delete BEFORE DELETE ON assistant_keys
		BEGIN SELECT RAISE(ABORT, 'fixture deletion failure'); END`); err != nil {
		t.Fatal(err)
	}
	if got := revoke(id); got != http.StatusInternalServerError {
		t.Fatalf("revoke with failed row deletion = %d, want 500", got)
	}
	if got := keyStatus(); got != http.StatusUnauthorized {
		t.Errorf("key after durable revocation and failed deletion = %d, want 401", got)
	}
	if _, err := database.Exec(`DROP TRIGGER refuse_assistant_delete`); err != nil {
		t.Fatal(err)
	}
	if got := revoke(id); got != http.StatusNoContent {
		t.Errorf("retry of durable revocation = %d, want 204", got)
	}

	closed := func(method, path string) int {
		t.Helper()
		req := asLocal(httptest.NewRequest(method, path, strings.NewReader(`{"name":"Unavailable"}`)))
		req.RemoteAddr = "127.0.0.9:40000"
		return serve(srv, req).Code
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, assistantKeysPath},
		{http.MethodPost, assistantKeysPath},
		{http.MethodDelete, assistantKeysPath + "/" + id},
	} {
		if got := closed(route.method, route.path); got != http.StatusInternalServerError {
			t.Errorf("%s with closed database = %d, want 500", route.method, got)
		}
	}
}

func TestRecoveryCatalogValidatesFiltersAndFailsWholeRequestOnReadError(t *testing.T) {
	database := testDB(t)
	srv := NewServer(database, ServerConfig{})
	catalog := func(query string) *httptest.ResponseRecorder {
		t.Helper()
		return serve(srv, asLocal(httptest.NewRequest(http.MethodGet, "/api/v1/recovery/catalog"+query, nil)))
	}
	for _, query := range []string{"?job_id=0", "?job_id=bad", "?limit_per_job=0", "?limit_per_job=1001"} {
		if w := catalog(query); w.Code != http.StatusBadRequest {
			t.Errorf("filter %s = %d, want 400", query, w.Code)
		}
	}
	if w := catalog("?job_id=55"); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"jobs":[]}` {
		t.Errorf("unknown Job filter = %d, want 200 and empty catalog", w.Code)
	}
	if _, err := database.CreateJob(db.Job{Name: "Selected Job"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TABLE restore_points`); err != nil {
		t.Fatal(err)
	}
	if w := catalog(""); w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "Selected Job") {
		t.Errorf("failed Restore Point read = %d, want 500 without partial catalog", w.Code)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if w := catalog(""); w.Code != http.StatusInternalServerError {
		t.Errorf("failed Job read = %d, want 500", w.Code)
	}
}
