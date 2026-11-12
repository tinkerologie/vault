package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/recovery"
	"github.com/tinkerologie/vault/internal/runner"
)

const evidenceQuery = "?item_type=folder&item_name=archive"

func evidencePoint(t *testing.T, d *db.DB, job, parent int64, kind, path, metadata string) int64 {
	t.Helper()
	run, err := d.CreateJobRun(db.JobRun{JobID: job, Status: "completed", BackupType: kind})
	id, err := d.CreateRestorePoint(db.RestorePoint{
		JobID: job, JobRunID: requireFixture(t, run, err), BackupType: kind,
		ParentRestorePointID: parent, StoragePath: path, Metadata: metadata,
	})
	return requireFixture(t, id, err)
}

func TestRecoveryEvidenceClassicChain(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Recovery evidence")
	root := t.TempDir()
	cfg, err := json.Marshal(map[string]string{"path": root})
	cfg = requireFixture(t, cfg, err)
	dest, err := d.CreateStorageDestination(db.StorageDestination{Name: "Local", Type: "local", Config: string(cfg)})
	job, err := d.CreateJob(db.Job{Name: "History", StorageDestID: requireFixture(t, dest, err)})
	job = requireFixture(t, job, err)
	const membership = `{"item_sizes":{"archive":20},"internal":"fixture-private-raw-metadata"}`
	full := evidencePoint(t, d, job, 0, "full", "fixture-private-job/full", membership)
	incr := evidencePoint(t, d, job, full, "incremental", "fixture-private-job/incremental", membership)
	for _, step := range []string{"full", "incremental"} {
		dir := filepath.Join(root, "fixture-private-job", step, "archive")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "data.tar"), []byte("fixture-private-backup-bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := recoveryTree(t, root)
	for _, peer := range []string{remotePeer, loopbackPeer} {
		route := fmt.Sprintf("/api/v1/recovery/restore-points/%d/evidence%s", incr, evidenceQuery)
		w := serve(srv, boundaryRequest(http.MethodGet, route, peer, daemonHost, map[string]string{"X-API-Key": key.Key}))
		if w.Code != http.StatusOK {
			t.Fatalf("evidence for %s = %d: %s", peer, w.Code, w.Body.String())
		}
		var result struct {
			Membership          string `json:"membership"`
			EligibleForApproval bool   `json:"eligible_for_approval"`
			Chain               struct {
				Status string `json:"status"`
				Steps  []struct {
					RestorePointID int64  `json:"restore_point_id"`
					Artifacts      string `json:"artifacts"`
				} `json:"steps"`
			} `json:"chain"`
			Verification struct {
				Status string `json:"status"`
			} `json:"verification"`
			Warnings []string `json:"warnings"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		steps := result.Chain.Steps
		if result.Membership != "confirmed" || result.Chain.Status != "confirmed" || !result.EligibleForApproval ||
			len(steps) != 2 || steps[0].RestorePointID != full || steps[1].RestorePointID != incr ||
			steps[0].Artifacts != "confirmed" || steps[1].Artifacts != "confirmed" ||
			result.Verification.Status != "none" || len(result.Warnings) == 0 {
			t.Fatalf("complete unverified chain: %s", w.Body.String())
		}
		for _, private := range []string{root, "fixture-private-", "data.tar", key.Key} {
			if strings.Contains(w.Body.String(), private) {
				t.Errorf("evidence exposed %q", private)
			}
		}
	}
	if after := recoveryTree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("evidence changed the local Destination")
	}

}

func TestRecoveryEvidenceAndFindAcrossRESTAndMCP(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Recovery reader")
	job, err := d.CreateJob(db.Job{Name: "Historical Job"})
	job = requireFixture(t, job, err)
	other, err := d.CreateJob(db.Job{Name: "Current Job"})
	other = requireFixture(t, other, err)
	_, err = d.AddJobItem(db.JobItem{JobID: other, ItemName: "archive", ItemType: "folder"})
	if err != nil {
		t.Fatal(err)
	}
	const member = `{"item_sizes":{"archive":20},"item_types":{"archive":"folder"},"verified":true}`
	full := evidencePoint(t, d, job, 0, "full", "job/full", member)
	missing := evidencePoint(t, d, job, 43210, "incremental", "job/incremental", member)
	unknown := evidencePoint(t, d, other, 0, "full", "other/full", "{}")
	absent := evidencePoint(t, d, other, 0, "full", "other/absent", `{"item_sizes":{"other":5}}`)
	foreign := evidencePoint(t, d, job, unknown, "incremental", "job/foreign", member)
	orphan := evidencePoint(t, d, job, 0, "differential", "job/orphan", member)
	cycle := evidencePoint(t, d, job, orphan, "incremental", "job/cycle", member)
	if _, err := d.Exec("UPDATE restore_points SET parent_restore_point_id = ? WHERE id = ?", cycle, orphan); err != nil {
		t.Fatal(err)
	}
	unanchored := evidencePoint(t, d, job, 0, "incremental", "job/unanchored", member)
	pass, err := d.CreateVerifyRun(missing, "quick")
	pass = requireFixture(t, pass, err)
	fail, err := d.CreateVerifyRun(missing, "deep")
	fail = requireFixture(t, fail, err)
	for id, status := range map[int64]string{pass: "passed", fail: "failed"} {
		if err := d.FinishVerifyRun(id, status, "fixture-private-verify-error"); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec("UPDATE verify_runs SET started_at = '2026-10-10 12:00:00', completed_at = '2026-10-10 12:01:00' WHERE id = ?", id); err != nil {
			t.Fatal(err)
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
	get := func(route string, code int) []byte {
		t.Helper()
		w := serve(srv, boundaryRequest(http.MethodGet, route, remotePeer, daemonHost, map[string]string{"X-API-Key": key.Key}))
		if w.Code != code {
			t.Fatalf("GET %s = %d, want %d: %s", route, w.Code, code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "fixture-private-") {
			t.Fatal("REST leaked private fixture data")
		}
		return w.Body.Bytes()
	}
	call := func(name, arguments string) json.RawMessage {
		t.Helper()
		status, response := client.post(fmt.Sprintf(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, name, arguments))
		_, payload, found := strings.Cut(response, "data: ")
		if status != http.StatusOK || !found || strings.Contains(response, "fixture-private-") {
			t.Fatalf("MCP %s = %d, %s", name, status, response)
		}
		var rpc struct {
			Result struct {
				Structured json.RawMessage `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(payload), &rpc); err != nil {
			t.Fatal(err)
		}
		if len(rpc.Result.Structured) == 0 {
			t.Fatalf("MCP structured result unavailable: %s", payload)
		}
		return rpc.Result.Structured
	}
	var first recovery.FindResult
	firstPage := get("/api/v1/recovery/restore-points"+evidenceQuery+"&limit=5", http.StatusOK)
	if err := json.Unmarshal(firstPage, &first); err != nil {
		t.Fatal(err)
	}
	var mcpPage recovery.FindResult
	if err := json.Unmarshal(call("find_restore_points", `{"item_type":"folder","item_name":"archive","limit":5}`), &mcpPage); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, mcpPage) {
		t.Fatalf("REST and MCP discovery differ: %s / %+v", firstPage, mcpPage)
	}
	if len(first.Points) != 5 || first.NextBeforeID != absent || first.Points[4].ID != absent || first.Points[4].Membership != "missing" {
		t.Fatalf("wrong first page: %s", firstPage)
	}
	var second recovery.FindResult
	if err := json.Unmarshal(get(fmt.Sprintf("/api/v1/recovery/restore-points%s&limit=5&before_id=%d", evidenceQuery, first.NextBeforeID), http.StatusOK), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Points) != 3 || second.NextBeforeID != 0 || second.Points[0].ID != unknown || second.Points[0].Membership != "unknown" ||
		second.Points[1].ID != missing || second.Points[2].ID != full || second.Points[2].Membership != "confirmed" {
		t.Fatalf("wrong second page: %+v", second)
	}

	for _, tc := range []struct {
		id                        int64
		membership, chain, reason string
	}{
		{missing, "confirmed", "missing", "missing_parent"},
		{unknown, "unknown", "unknown", "unknown_step_membership"},
		{absent, "missing", "missing", "item_missing_from_required_step"},
		{foreign, "confirmed", "broken", "foreign_parent"},
		{cycle, "confirmed", "broken", "cycle"},
		{unanchored, "confirmed", "missing", "missing_full_backup"},
	} {
		route := fmt.Sprintf("/api/v1/recovery/restore-points/%d/evidence%s", tc.id, evidenceQuery)
		var rest, mcp runner.RecoveryEvidence
		if err := json.Unmarshal(get(route, http.StatusOK), &rest); err != nil {
			t.Fatal(err)
		}
		input := fmt.Sprintf(`{"restore_point_id":%d,"item_type":"folder","item_name":"archive"}`, tc.id)
		if err := json.Unmarshal(call("get_recovery_evidence", input), &mcp); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(rest, mcp) || rest.Point.ID != tc.id || string(rest.Membership) != tc.membership ||
			string(rest.Chain.Status) != tc.chain || rest.Chain.Reason != tc.reason || rest.EligibleForApproval {
			t.Errorf("point %d: REST %+v, MCP %+v", tc.id, rest, mcp)
		}
		if tc.id == missing {
			verify := rest.Verification
			if rest.Chain.MissingRestorePointID != 43210 || verify.Status != "failed" || verify.Mode != "deep" ||
				verify.VerifiedDuringBackup == nil || !*verify.VerifiedDuringBackup || verify.CompletedAt == nil ||
				!verify.CompletedAt.Equal(time.Date(2026, 10, 10, 12, 1, 0, 0, time.UTC)) {
				t.Errorf("did not surface the missing ID and latest Verification: %+v", rest)
			}
		}
	}
	get("/api/v1/recovery/restore-points?item_type=zfs&item_name=archive", http.StatusBadRequest)
	get("/api/v1/recovery/restore-points"+evidenceQuery+"&limit=201", http.StatusBadRequest)
	get("/api/v1/recovery/restore-points/99999/evidence"+evidenceQuery, http.StatusNotFound)
	for _, peer := range []string{remotePeer, loopbackPeer} {
		w := serve(srv, boundaryRequest(http.MethodPost, "/api/v1/recovery/restore-points"+evidenceQuery, peer, daemonHost, map[string]string{"X-API-Key": key.Key}))
		if w.Code != http.StatusForbidden {
			t.Errorf("POST discovery from %s = %d, want 403", peer, w.Code)
		}
	}
}

func TestDeduplicatedEvidenceProbesOnlyRequiredPackMetadata(t *testing.T) {
	d := testDB(t)
	var headerSize, packSize atomic.Int64
	headerSize.Store(24)
	packSize.Store(128)
	var requests atomic.Int64
	objectStore := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodHead || r.Header.Get("Range") != "" || r.URL.RawQuery != "" {
			t.Errorf("unexpected storage probe %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/fixture/_vault/repo.json":
			w.Header().Set("Content-Length", strconv.FormatInt(headerSize.Load(), 10))
		case "/fixture/_vault/packs/ab/abcdef":
			size := packSize.Load()
			if size == -2 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if size < 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		default:
			t.Errorf("unexpected metadata path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer objectStore.Close()
	config, err := json.Marshal(map[string]any{
		"bucket": "fixture", "region": "us-east-1", "endpoint": objectStore.URL,
		"access_key": "fixture-private-key", "secret_key": "fixture-private-secret", "force_path_style": true,
	})
	config = requireFixture(t, config, err)
	dest, err := d.CreateStorageDestination(db.StorageDestination{Name: "S3 fixture", Type: "s3", Config: string(config), DedupEnabled: true})
	dest = requireFixture(t, dest, err)
	job, err := d.CreateJob(db.Job{Name: "Dedup Job", StorageDestID: dest})
	job = requireFixture(t, job, err)
	full := evidencePoint(t, d, job, 0, "full", "ignored/full", `{"item_sizes":{"earlier_item":5}}`)
	manifest := strings.Repeat("fa", 32)
	incremental := evidencePoint(t, d, job, full, "incremental", "ignored/selected", fmt.Sprintf(`{"item_manifests":{"archive":%q}}`, manifest))
	if err := d.UpsertDedupPack(db.DedupPack{ID: "abcdef", StorageID: dest, Path: "_vault/packs/ab/abcdef", SizeBytes: 128, ChunkCount: 1}); err != nil {
		t.Fatal(err)
	}
	manifestID, err := hex.DecodeString(manifest)
	if err := d.UpsertDedupChunk(db.DedupChunk{ChunkID: requireFixture(t, manifestID, err), StorageID: dest, PackID: "abcdef", Offset: 24, Length: 40}); err != nil {
		t.Fatal(err)
	}
	run, err := d.CreateVerifyRun(incremental, "deep")
	if err := d.FinishVerifyRun(requireFixture(t, run, err), "failed", "fixture-private-transient-error"); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Dedup reader")
	for _, tc := range []struct {
		name         string
		header, size int64
		changeIndex  string
		artifact     runner.Status
		eligible     bool
		requests     int64
	}{
		{"present", 24, 128, "", "confirmed", true, 2},
		{"truncated", 24, 63, "", "broken", false, 2},
		{"missing", 24, -1, "", "missing", false, 2},
		{"denied", 24, -2, "", "unknown", true, 2},
		{"empty header", 0, 128, "", "broken", false, 1},
		{"unsafe indexed path", 24, 128, "UPDATE dedup_packs SET path = '_vault/other' WHERE id = 'abcdef'", "broken", false, 1},
		{"missing index entry", 24, 128, "DELETE FROM dedup_chunks", "missing", false, 1},
	} {
		headerSize.Store(tc.header)
		packSize.Store(tc.size)
		requests.Store(0)
		if tc.changeIndex != "" {
			if _, err := d.Exec(tc.changeIndex); err != nil {
				t.Fatal(err)
			}
		}
		path := fmt.Sprintf("/api/v1/recovery/restore-points/%d/evidence%s", incremental, evidenceQuery)
		w := serve(srv, boundaryRequest(http.MethodGet, path, remotePeer, daemonHost, map[string]string{"X-API-Key": key.Key}))
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "fixture-private-") || strings.Contains(w.Body.String(), manifest) ||
			strings.Contains(w.Body.String(), "_vault") || strings.Contains(w.Body.String(), "abcdef") {
			t.Fatalf("status = %d, evidence = %s", w.Code, w.Body.String())
		}
		var result runner.RecoveryEvidence
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		steps := result.Chain.Steps
		if result.Chain.Status != "confirmed" || result.ItemTypeEvidence != "unknown" || result.Membership != "confirmed" || result.EligibleForApproval != tc.eligible ||
			len(steps) != 2 || steps[0].DataRequired || steps[0].Artifacts != "" || !steps[1].DataRequired || steps[1].Artifacts != tc.artifact ||
			result.Verification.Status != "failed" || !slices.Contains(result.Warnings, "latest_verification_failed") {
			t.Errorf("%s produced wrong result: %s", tc.name, w.Body.String())
		}
		if got := requests.Load(); got != tc.requests {
			t.Errorf("%s sent %d requests, want %d", tc.name, got, tc.requests)
		}
	}
}

func recoveryTree(t *testing.T, root string) map[string]string {
	t.Helper()
	state := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			state[path] = "directory"
			return nil
		}
		contents, err := os.ReadFile(path)
		state[path] = string(contents)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRecoveryEvidenceBoundsClassicStorageListing(t *testing.T) {
	d := testDB(t)
	var requests atomic.Int64
	objectStore := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/fixture" || q.Get("prefix") != "job/full/archive/" ||
			q.Get("max-keys") != "64" || q.Get("continuation-token") != "" {
			t.Errorf("unexpected storage operation %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
			<IsTruncated>true</IsTruncated><NextContinuationToken>unused</NextContinuationToken>
			<Contents><Key>job/full/archive/data.tar</Key><Size>1024</Size></Contents>
		</ListBucketResult>`))
	}))
	defer objectStore.Close()
	config, err := json.Marshal(map[string]any{
		"bucket": "fixture", "region": "us-east-1", "endpoint": objectStore.URL,
		"access_key": "fixture-private-key", "secret_key": "fixture-private-secret", "force_path_style": true,
	})
	config = requireFixture(t, config, err)
	dest, err := d.CreateStorageDestination(db.StorageDestination{Name: "S3 fixture", Type: "s3", Config: string(config)})
	dest = requireFixture(t, dest, err)
	job, err := d.CreateJob(db.Job{Name: "Job", StorageDestID: dest})
	job = requireFixture(t, job, err)
	point := evidencePoint(t, d, job, 0, "full", "job/full", `{"item_sizes":{"archive":1024}}`)
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Bounded storage reader")
	path := fmt.Sprintf("/api/v1/recovery/restore-points/%d/evidence%s", point, evidenceQuery)
	w := serve(srv, boundaryRequest(http.MethodGet, path, remotePeer, daemonHost, map[string]string{"X-API-Key": key.Key}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var result runner.RecoveryEvidence
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Chain.Steps) != 1 || result.Chain.Steps[0].Artifacts != "unknown" ||
		!slices.Contains(result.Warnings, "artifact_evidence_unavailable") || requests.Load() != 1 {
		t.Fatalf("truncated listing generated %d requests and evidence %s", requests.Load(), w.Body.String())
	}
}

func TestAssistantCatalogAndEvidenceBoundLongChains(t *testing.T) {
	d := testDB(t)
	job, err := d.CreateJob(db.Job{Name: "Long chain"})
	job = requireFixture(t, job, err)
	run, err := d.CreateJobRun(db.JobRun{JobID: job, Status: "completed"})
	run = requireFixture(t, run, err)
	// One fixture transaction populates a history beyond the catalog link cap.
	const pointCount = 4100
	if _, err := d.Exec(`
		WITH RECURSIVE sequence(id) AS (
			SELECT 1 UNION ALL SELECT id + 1 FROM sequence WHERE id < ?
		)
		INSERT INTO restore_points(id, job_run_id, job_id, backup_type, storage_path, metadata, parent_restore_point_id)
		SELECT id, ?, ?, CASE id WHEN 1 THEN 'full' ELSE 'incremental' END,
			'job/run', '{"item_sizes":{"archive":1024}}', id - 1
		FROM sequence`, pointCount, run, job); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Long chain reader")
	headers := map[string]string{"X-API-Key": key.Key}
	w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/recovery/catalog?limit_per_job=1", remotePeer, daemonHost, headers))
	if w.Code != http.StatusOK {
		t.Fatalf("catalog status = %d: %s", w.Code, w.Body.String())
	}
	var catalog recovery.Catalog
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Jobs) != 1 || !catalog.Jobs[0].Truncated || len(catalog.Jobs[0].RestorePoints) != 1 ||
		catalog.Jobs[0].RestorePoints[0].ID != pointCount || catalog.Jobs[0].RestorePoints[0].ChainStatus != "unknown" {
		t.Fatalf("catalog claimed a known chain beyond the limit: %s", w.Body.String())
	}
	route := fmt.Sprintf("/api/v1/recovery/restore-points/%d/evidence%s", pointCount, evidenceQuery)
	w = serve(srv, boundaryRequest(http.MethodGet, route, remotePeer, daemonHost, headers))
	if w.Code != http.StatusOK {
		t.Fatalf("evidence status = %d: %s", w.Code, w.Body.String())
	}
	var result runner.RecoveryEvidence
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Chain.Status != "unknown" || result.Chain.Reason != "depth_limit" || result.EligibleForApproval ||
		len(result.Chain.Steps) > 128 {
		t.Errorf("evidence exceeded the Backup Chain limit: %+v", result.Chain)
	}
}

func TestRecoveryEvidenceKeepsKnownFailureAfterStorageTimeout(t *testing.T) {
	d := testDB(t)
	objectStore := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/fixture" || q.Get("list-type") != "2" {
			t.Errorf("unexpected storage operation %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch q.Get("prefix") {
		case "job/full/archive/":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
				<IsTruncated>false</IsTruncated></ListBucketResult>`))
		case "job/incremental/archive/":
			<-r.Context().Done()
		default:
			t.Errorf("unexpected prefix %s", q.Get("prefix"))
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer objectStore.Close()
	config, err := json.Marshal(map[string]any{
		"bucket": "fixture", "region": "us-east-1", "endpoint": objectStore.URL,
		"access_key": "fixture-private-key", "secret_key": "fixture-private-secret", "force_path_style": true,
	})
	config = requireFixture(t, config, err)
	dest, err := d.CreateStorageDestination(db.StorageDestination{Name: "Slow S3 fixture", Type: "s3", Config: string(config)})
	dest = requireFixture(t, dest, err)
	job, err := d.CreateJob(db.Job{Name: "Job", StorageDestID: dest})
	job = requireFixture(t, job, err)
	const metadata = `{"item_sizes":{"archive":1024}}`
	full := evidencePoint(t, d, job, 0, "full", "job/full", metadata)
	point := evidencePoint(t, d, job, full, "incremental", "job/incremental", metadata)
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Slow storage reader")
	path := fmt.Sprintf("/api/v1/recovery/restore-points/%d/evidence%s", point, evidenceQuery)
	w := serve(srv, boundaryRequest(http.MethodGet, path, remotePeer, daemonHost, map[string]string{"X-API-Key": key.Key}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var result runner.RecoveryEvidence
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Chain.Steps) != 2 || result.Chain.Steps[0].Artifacts != "missing" ||
		result.Chain.Steps[1].Artifacts != "unknown" || result.EligibleForApproval {
		t.Fatalf("a timeout obscured a proven missing artifact: %s", w.Body.String())
	}
}

func TestRecoveryEvidenceResolvesManifestForSelectedItemOnly(t *testing.T) {
	d := testDB(t)
	root := t.TempDir()
	config, err := json.Marshal(map[string]string{"path": root})
	config = requireFixture(t, config, err)
	dest, err := d.CreateStorageDestination(db.StorageDestination{Name: "Mixed local backup", Type: "local", Config: string(config), DedupEnabled: true})
	dest = requireFixture(t, dest, err)
	job, err := d.CreateJob(db.Job{Name: "Folder and VM", StorageDestID: dest})
	job = requireFixture(t, job, err)
	manifest := strings.Repeat("ab", 32)
	rawManifest, err := hex.DecodeString(manifest)
	rawManifest = requireFixture(t, rawManifest, err)
	for relativePath, body := range map[string]string{
		"job/full/guest/domain.xml": "<domain type='kvm'><name>guest</name><devices><disk type='file' device='disk'><source file='/mnt/user/domains/guest.img'/><target dev='vda'/></disk></devices></domain>",
		"job/full/guest/vdisk0.img": strings.Repeat("d", 128),
		"job/full/guest/vdisk2.img": "",
		"job/full/archive/data.tar": strings.Repeat("\x00", 1024),
		"_vault/repo.json":          `{"version":1}`,
		"_vault/packs/ab/abcdef":    strings.Repeat("p", 128),
	} {
		file := filepath.Join(root, relativePath)
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.UpsertDedupPack(db.DedupPack{ID: "abcdef", StorageID: dest, Path: "_vault/packs/ab/abcdef", SizeBytes: 128, ChunkCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDedupChunk(db.DedupChunk{ChunkID: rawManifest, StorageID: dest, PackID: "abcdef", Offset: 24, Length: 40}); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Mixed recovery reader")
	for _, tc := range []struct {
		name, query, metadata string
		artifacts             runner.Status
		eligible              bool
	}{
		{"classic VM beside deduplicated folder", "?item_type=vm&item_name=guest",
			fmt.Sprintf(`{"item_sizes":{"guest":128,"archive":128},"item_manifests":{"archive":%q},"checksums":{"guest":{"domain.xml":"xml","vdisk0.img":"disk"}}}`, manifest), "confirmed", true},
		{"VM without a recorded disk inventory", "?item_type=vm&item_name=guest",
			fmt.Sprintf(`{"item_sizes":{"guest":128,"archive":128},"item_manifests":{"archive":%q}}`, manifest), "unknown", true},
		{"VM with a missing recorded disk", "?item_type=vm&item_name=guest",
			fmt.Sprintf(`{"item_sizes":{"guest":128,"archive":128},"item_manifests":{"archive":%q},"checksums":{"guest":{"domain.xml":"xml","vdisk1.img":"disk"}}}`, manifest), "missing", false},
		{"VM with an empty recorded disk", "?item_type=vm&item_name=guest",
			`{"item_sizes":{"guest":128},"item_manifests":{},"checksums":{"guest":{"domain.xml":"xml","vdisk2.img":"disk"}}}`, "broken", false},
		{"VM checkpoint identifies type", "?item_type=vm&item_name=guest",
			`{"item_sizes":{"guest":128},"item_manifests":{},"vm_checkpoints":{"guest":"checkpoint"}}`, "unknown", true},
		{"VM checkpoint excludes a folder of the same name", evidenceQuery,
			`{"item_sizes":{"archive":128},"vm_checkpoints":{"archive":"checkpoint"}}`, "unknown", false},
		{"imported item type matches", evidenceQuery,
			`{"item_sizes":{"archive":128},"manifest_items":[{"name":"archive","type":"folder"}]}`, "confirmed", true},
		{"imported item type differs", evidenceQuery,
			`{"item_sizes":{"archive":128},"manifest_items":[{"name":"archive","type":"container"}]}`, "unknown", false},
		{"native recorded type differs", evidenceQuery,
			`{"item_sizes":{"archive":128},"item_types":{"archive":"container"}}`, "unknown", false},
		{"deduplicated folder beside classic VM", evidenceQuery,
			fmt.Sprintf(`{"item_sizes":{"guest":128,"archive":128},"item_manifests":{"archive":%q}}`, manifest), "confirmed", true},
		{"legacy row-level manifest", evidenceQuery, `{"item_sizes":{"archive":128}}`, "confirmed", true},
		{"ambiguous row-level manifest", evidenceQuery, `{"item_sizes":{"archive":128,"guest":128}}`, "broken", false},
		{"invalid selected entry", evidenceQuery, `{"item_sizes":{"archive":128},"item_manifests":{"archive":"invalid"}}`, "broken", false},
		{"null selected entry", evidenceQuery, `{"item_sizes":{"archive":128},"item_manifests":{"archive":null}}`, "broken", false},
		{"invalid map", evidenceQuery, `{"item_sizes":{"archive":128},"item_manifests":[]}`, "broken", false},
		{"empty classic placeholder", "?item_type=vm&item_name=guest",
			fmt.Sprintf(`{"item_sizes":{"guest":128,"archive":128},"item_manifests":{"archive":%q,"guest":""},"checksums":{"guest":{"domain.xml":"xml","vdisk0.img":"disk"}}}`, manifest), "confirmed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			point := evidencePoint(t, d, job, 0, "full", "job/full", tc.metadata)
			if err := d.SetRestorePointManifestID(point, rawManifest); err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf("/api/v1/recovery/restore-points/%d/evidence%s", point, tc.query)
			w := serve(srv, boundaryRequest(http.MethodGet, path, remotePeer, daemonHost, map[string]string{"X-API-Key": key.Key}))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			var result runner.RecoveryEvidence
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Chain.Steps) != 1 || result.Chain.Steps[0].Artifacts != tc.artifacts ||
				result.EligibleForApproval != tc.eligible {
				t.Errorf("wrong selected item format: %s", w.Body.String())
			}
			if (tc.name == "legacy row-level manifest" || tc.name == "deduplicated folder beside classic VM") &&
				result.ItemTypeEvidence != "unknown" {
				t.Errorf("selected dedup manifest was treated as a classic archive: %s", w.Body.String())
			}
		})
	}
}

func TestRecoveryEvidenceRedactsCatalogFailures(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Recovery errors")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = remotePeer
		srv.router.ServeHTTP(w, r)
	}))
	defer ts.Close()
	defer srv.mcp.Close()
	client := assistantMCPClient{t: t, baseURL: ts.URL + "/api/v1/mcp", key: key.Key}
	client.initialize()

	const route = "/api/v1/recovery/restore-points"
	get := func(path string, want int, message string) {
		t.Helper()
		w := serve(srv, boundaryRequest(http.MethodGet, path, remotePeer, daemonHost, map[string]string{"X-API-Key": key.Key}))
		if w.Code != want || !strings.Contains(w.Body.String(), message) || strings.Contains(w.Body.String(), "fixture_private") {
			t.Errorf("GET %s = %d: %s", path, w.Code, w.Body.String())
		}
	}
	call := func(tool, arguments, message string) {
		t.Helper()
		status, response := client.post(fmt.Sprintf(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, arguments))
		if status != http.StatusOK || !strings.Contains(response, `"isError":true`) || !strings.Contains(response, message) ||
			strings.Contains(response, "fixture_private") {
			t.Errorf("MCP %s = %d: %s", tool, status, response)
		}
	}
	get(route+evidenceQuery+"&limit=not-a-number", http.StatusBadRequest, "limit must be")
	get(route+evidenceQuery+"&before_id=0", http.StatusBadRequest, "before_id must be")
	get(route+"/bad/evidence"+evidenceQuery, http.StatusBadRequest, "invalid")
	call("get_recovery_evidence", `{"restore_point_id":0,"item_type":"folder","item_name":"archive"}`, "restore_point_id must be positive")
	call("get_recovery_evidence", `{"restore_point_id":999,"item_type":"folder","item_name":"archive"}`, "restore point not found")
	call("find_restore_points", `{"item_type":"folder","item_name":"archive","before_id":-1}`, "before_id must be positive")

	// The authentication tables stay available while both catalog queries fail.
	if _, err := d.Exec("ALTER TABLE restore_points RENAME TO fixture_private_points"); err != nil {
		t.Fatal(err)
	}
	get(route+evidenceQuery, http.StatusInternalServerError, "could not read recovery evidence")
	get(route+"/1/evidence"+evidenceQuery, http.StatusInternalServerError, "could not read recovery evidence")
	call("find_restore_points", `{"item_type":"folder","item_name":"archive"}`, "could not find Restore Points")
	call("get_recovery_evidence", `{"restore_point_id":1,"item_type":"folder","item_name":"archive"}`, "could not read recovery evidence")
}

func TestRecoveryEvidenceFallsBackToNamedObjectMetadata(t *testing.T) {
	d := testDB(t)
	var responseStatus atomic.Int64
	responseStatus.Store(http.StatusMultiStatus)
	var requests atomic.Int64
	objectStore := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "PROPFIND" || r.Header.Get("Depth") != "0" ||
			(r.URL.Path != "/job/full/archive/data.tar" && !strings.HasSuffix(r.URL.Path, ".manifest.json")) {
			t.Errorf("unexpected storage operation %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		status := int(responseStatus.Load())
		w.WriteHeader(status)
		if status == http.StatusMultiStatus {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">
				<d:response><d:href>/job/full/archive/data.tar</d:href><d:propstat>
					<d:prop><d:resourcetype/><d:getcontentlength>1024</d:getcontentlength></d:prop>
					<d:status>HTTP/1.1 200 OK</d:status>
				</d:propstat></d:response></d:multistatus>`))
		}
	}))
	defer objectStore.Close()
	config, err := json.Marshal(map[string]string{"url": objectStore.URL})
	config = requireFixture(t, config, err)
	dest, err := d.CreateStorageDestination(db.StorageDestination{Name: "WebDAV fixture", Type: "webdav", Config: string(config)})
	dest = requireFixture(t, dest, err)
	job, err := d.CreateJob(db.Job{Name: "Job", StorageDestID: dest})
	job = requireFixture(t, job, err)
	point := evidencePoint(t, d, job, 0, "full", "job/full", `{"item_sizes":{"archive":1024},"checksums":{"archive":{"data.tar":"fixture-private-hash","data.tar.index.json":"fixture-private-index-hash"}}}`)
	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Named storage reader")
	for _, tc := range []struct {
		status   int64
		artifact runner.Status
		requests int64
	}{
		{http.StatusMultiStatus, "confirmed", 2},
		{http.StatusNotFound, "missing", 3},
		{http.StatusForbidden, "unknown", 2},
	} {
		responseStatus.Store(tc.status)
		requests.Store(0)
		path := fmt.Sprintf("/api/v1/recovery/restore-points/%d/evidence%s", point, evidenceQuery)
		w := serve(srv, boundaryRequest(http.MethodGet, path, remotePeer, daemonHost, map[string]string{"X-API-Key": key.Key}))
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "fixture-private-hash") || strings.Contains(w.Body.String(), "data.tar") {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		var result runner.RecoveryEvidence
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Chain.Steps) != 1 || result.Chain.Steps[0].Artifacts != tc.artifact || requests.Load() < 1 || requests.Load() > tc.requests {
			t.Errorf("storage status %d made %d probes: %s", tc.status, requests.Load(), w.Body.String())
		}
		if tc.artifact == runner.Confirmed && (!result.EligibleForApproval || !slices.Contains(result.Warnings, "folder_replay_evidence_unavailable")) {
			t.Errorf("named archive metadata should remain eligible with a replay warning: %s", w.Body.String())
		}
	}
}

func TestRecoveryEvidenceUsesSelectedItemsRequiredSteps(t *testing.T) {
	d := testDB(t)
	root := t.TempDir()
	config, err := json.Marshal(map[string]string{"path": root})
	config = requireFixture(t, config, err)
	dest, err := d.CreateStorageDestination(db.StorageDestination{Name: "Local recovery", Type: "local", Config: string(config), DedupEnabled: true})
	dest = requireFixture(t, dest, err)
	job, err := d.CreateJob(db.Job{Name: "Job", StorageDestID: dest})
	job = requireFixture(t, job, err)
	for relativePath, body := range map[string]string{
		"job/new-item/archive/data.tar": strings.Repeat("\x00", 1024),
		"_vault/repo.json":              `{"version":1}`,
		"_vault/packs/ab/abcdef":        strings.Repeat("p", 128),
	} {
		file := filepath.Join(root, relativePath)
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw := strings.Repeat("ab", 32)
	id, err := hex.DecodeString(raw)
	id = requireFixture(t, id, err)
	if err := d.UpsertDedupPack(db.DedupPack{ID: "abcdef", StorageID: dest, Path: "_vault/packs/ab/abcdef", SizeBytes: 128, ChunkCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDedupChunk(db.DedupChunk{ChunkID: id, StorageID: dest, PackID: "abcdef", Offset: 24, Length: 40}); err != nil {
		t.Fatal(err)
	}
	older := evidencePoint(t, d, job, 0, "full", "job/full", `{"item_sizes":{"another_item":12}}`)
	newItem := evidencePoint(t, d, job, older, "incremental", "job/new-item", `{"item_sizes":{"archive":1024}}`)
	metadata := fmt.Sprintf(`{"imported":true,"item_manifests":{"archive":%q}}`, raw)
	imported := evidencePoint(t, d, job, 0, "incremental", "job/imported", metadata)
	brokenImport := evidencePoint(t, d, job, 123456, "differential", "job/broken-import", metadata)

	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Backup Chain reader")
	for _, tc := range []struct {
		pointID  int64
		status   runner.Status
		eligible bool
	}{
		{newItem, "confirmed", true},
		{imported, "confirmed", true},
		{brokenImport, "missing", false},
	} {
		path := fmt.Sprintf("/api/v1/recovery/restore-points/%d/evidence%s", tc.pointID, evidenceQuery)
		w := serve(srv, boundaryRequest(http.MethodGet, path, remotePeer, daemonHost, map[string]string{"X-API-Key": key.Key}))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		var result runner.RecoveryEvidence
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Chain.Status != tc.status || result.EligibleForApproval != tc.eligible {
			t.Errorf("wrong Backup Chain evidence: %s", w.Body.String())
		}
		steps := result.Chain.Steps
		if tc.pointID == newItem && (len(steps) != 2 || steps[0].Membership != "missing" || steps[0].DataRequired ||
			!steps[1].DataRequired || steps[1].Artifacts != "confirmed") {
			t.Errorf("older point was required for a newly added item: %s", w.Body.String())
		}
		if tc.pointID == brokenImport && result.Chain.MissingRestorePointID != 123456 {
			t.Errorf("known missing imported parent was not surfaced: %s", w.Body.String())
		}
	}
}

func TestClassicRecoveryEvidenceDetectsMissingAndUnusableArtifacts(t *testing.T) {
	d := testDB(t)
	root := t.TempDir()
	config, err := json.Marshal(map[string]string{"path": root})
	config = requireFixture(t, config, err)
	dest, err := d.CreateStorageDestination(db.StorageDestination{Name: "Local backup", Type: "local", Config: string(config)})
	dest = requireFixture(t, dest, err)
	job, err := d.CreateJob(db.Job{Name: "Job", StorageDestID: dest})
	job = requireFixture(t, job, err)
	noDest, err := d.CreateJob(db.Job{Name: "Job with deleted Destination"})
	noDest = requireFixture(t, noDest, err)
	cases := []struct {
		run, itemType string
		jobID         int64
		files         map[string]string
		status        runner.Status
		replayWarning bool
		pointID       int64
	}{
		{run: "job/missing", itemType: "folder", jobID: job, status: "missing"},
		{run: "job/sidecars-only", itemType: "folder", jobID: job, files: map[string]string{"listing.json": "{}"}, status: "missing"},
		{run: "job/empty", itemType: "folder", jobID: job, files: map[string]string{"data.tar": ""}, status: "broken"},
		{run: "job/collision", itemType: "folder", jobID: job, files: map[string]string{"data.tar": "backup", "data.tar.gz": "duplicate"}, status: "broken"},
		{run: "job/optional-sidecar-missing", itemType: "folder", jobID: job, files: map[string]string{"data.tar": "backup"}, status: "confirmed", replayWarning: true},
		{run: "job/container-no-image", itemType: "container", jobID: job, files: map[string]string{"config.json": "{}"}, status: "missing"},
		{run: "job/deleted-destination", itemType: "folder", jobID: noDest, status: "missing"},
	}
	for i := range cases {
		tc := &cases[i]
		dir := filepath.Join(root, tc.run, "archive")
		if tc.files != nil {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			for name, contents := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		metadata := fmt.Sprintf(`{"item_sizes":{"archive":1024},"item_types":{"archive":%q}}`, tc.itemType)
		if tc.replayWarning {
			metadata = `{"item_sizes":{"archive":1024},"checksums":{"archive":{"data.tar":"fixture-archive-hash","data.tar.index.json":"fixture-index-hash"}}}`
		}
		tc.pointID = evidencePoint(t, d, tc.jobID, 0, "full", tc.run, metadata)
	}

	srv := NewServer(d, ServerConfig{})
	key := issueAssistantKey(t, srv, "Classic storage reader")
	headers := map[string]string{"X-API-Key": key.Key}
	for _, tc := range cases {
		path := fmt.Sprintf("/api/v1/recovery/restore-points/%d/evidence?item_type=%s&item_name=archive", tc.pointID, tc.itemType)
		w := serve(srv, boundaryRequest(http.MethodGet, path, remotePeer, daemonHost, headers))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		var result runner.RecoveryEvidence
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Chain.Steps) != 1 || result.Chain.Steps[0].Artifacts != tc.status ||
			result.EligibleForApproval != tc.replayWarning || slices.Contains(result.Warnings, "folder_replay_evidence_unavailable") != tc.replayWarning {
			t.Errorf("expected %s artifact evidence: %s", tc.status, w.Body.String())
		}
	}
}
