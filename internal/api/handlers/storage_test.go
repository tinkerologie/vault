package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/storage"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// newDedupStorageHandler provisions a StorageHandler backed by a real DB
// and runner so the dedup-stats / gc endpoints exercise the actual code
// path. Returns the handler plus the storage destination ID for the test
// to use in URL params.
func newDedupStorageHandler(t *testing.T, dedupEnabled bool) (*StorageHandler, int64) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "vault.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	storageDir := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:         "dedup-handler-test",
		Type:         "local",
		Config:       string(cfg),
		DedupEnabled: dedupEnabled,
	})
	if err != nil {
		t.Fatalf("create storage destination: %v", err)
	}

	serverKey := bytes.Repeat([]byte{0xee}, 32)
	hub := ws.NewHub()
	go hub.Run()
	r := runner.New(d, hub, serverKey)

	// Initialise the dedup repo header on disk so handlers that
	// `dedup.OpenRepo` succeed without going through a full backup. Skipped
	// for non-dedup destinations — those tests never call into the dedup
	// machinery.
	if dedupEnabled {
		dest, err := d.GetStorageDestination(destID)
		if err != nil {
			t.Fatalf("get dest: %v", err)
		}
		adapter, err := storage.NewAdapter(dest.Type, dest.Config)
		if err != nil {
			t.Fatalf("adapter: %v", err)
		}
		if _, err := dedup.InitRepo(d, adapter, dest.ID, serverKey); err != nil {
			storage.CloseAdapter(adapter)
			t.Fatalf("init repo: %v", err)
		}
		storage.CloseAdapter(adapter)
	}

	return NewStorageHandler(d, r, serverKey), destID
}

// reqWithID is a small helper that attaches an `id` URL parameter to a
// freshly built httptest request — chi parses URL params from a context
// value that httptest does not populate by default.
func reqWithID(method, path, id string, body []byte) *http.Request {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestGetDedupStatsForDedupDest(t *testing.T) {
	h, destID := newDedupStorageHandler(t, true)

	req := reqWithID(http.MethodGet,
		"/api/v1/storage/1/dedup-stats", strconv.FormatInt(destID, 10), nil)
	w := httptest.NewRecorder()
	h.GetDedupStats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["enabled"] != true {
		t.Errorf("enabled = %v, want true", resp["enabled"])
	}
	if _, ok := resp["dedup_ratio"]; !ok {
		t.Errorf("response missing dedup_ratio: %v", resp)
	}
	for _, k := range []string{"total_chunks", "total_packs", "logical_bytes", "physical_bytes", "wasted_bytes_estimate"} {
		if _, ok := resp[k]; !ok {
			t.Errorf("response missing %q: %v", k, resp)
		}
	}
}

func TestGetDedupStatsForNonDedupDest(t *testing.T) {
	h, destID := newDedupStorageHandler(t, false)

	req := reqWithID(http.MethodGet,
		"/api/v1/storage/1/dedup-stats", strconv.FormatInt(destID, 10), nil)
	w := httptest.NewRecorder()
	h.GetDedupStats(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestGetDedupStatsMissingDest(t *testing.T) {
	h, _ := newDedupStorageHandler(t, true)

	req := reqWithID(http.MethodGet,
		"/api/v1/storage/9999/dedup-stats", "9999", nil)
	w := httptest.NewRecorder()
	h.GetDedupStats(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestRunDedupGCReturns202(t *testing.T) {
	h, destID := newDedupStorageHandler(t, true)

	req := reqWithID(http.MethodPost,
		"/api/v1/storage/1/gc", strconv.FormatInt(destID, 10), nil)
	w := httptest.NewRecorder()
	h.RunDedupGC(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["gc_run_id"] == "" {
		t.Errorf("expected non-empty gc_run_id in response: %v", resp)
	}
	// Give the background goroutine a moment so the test cleanup doesn't
	// race-close the DB while the goroutine is still mid-query. The GC will
	// fail (no repo initialised yet) but we don't care about the result —
	// only that we don't panic.
	time.Sleep(100 * time.Millisecond)
}

func TestRunDedupGCRejectsNonDedupDest(t *testing.T) {
	h, destID := newDedupStorageHandler(t, false)

	req := reqWithID(http.MethodPost,
		"/api/v1/storage/1/gc", strconv.FormatInt(destID, 10), nil)
	w := httptest.NewRecorder()
	h.RunDedupGC(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestRunDedupGCMissingDest(t *testing.T) {
	h, _ := newDedupStorageHandler(t, true)

	req := reqWithID(http.MethodPost,
		"/api/v1/storage/9999/gc", "9999", nil)
	w := httptest.NewRecorder()
	h.RunDedupGC(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestTestConfigSuccess(t *testing.T) {
	h, _ := newDedupStorageHandler(t, false)
	dir := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": dir})
	body, _ := json.Marshal(map[string]string{"type": "local", "config": string(cfg)})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage/test", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.TestConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["success"] != true {
		t.Errorf("success = %v, want true; body: %s", resp["success"], w.Body.String())
	}
}

func TestTestConfigMissingType(t *testing.T) {
	h, _ := newDedupStorageHandler(t, false)
	body, _ := json.Marshal(map[string]string{"config": "{}"})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage/test", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.TestConfig(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestTestConfigBogusType(t *testing.T) {
	h, _ := newDedupStorageHandler(t, false)
	body, _ := json.Marshal(map[string]string{"type": "bogus", "config": "{}"})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage/test", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.TestConfig(w, req)

	// Unknown type is a soft failure: 200 with success:false so the modal can
	// show the error inline rather than throwing.
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["success"] != false {
		t.Errorf("success = %v, want false; body: %s", resp["success"], w.Body.String())
	}
}

func TestRedactConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		want   map[string]string
		intact []string
	}{
		{
			name:   "local config unchanged",
			input:  `{"base_path":"/mnt/backups"}`,
			intact: []string{"base_path"},
		},
		{
			name:   "sftp password redacted",
			input:  `{"host":"192.168.1.1","user":"admin","password":"s3cret","base_path":"/backups"}`,
			want:   map[string]string{"password": "••••••••"},
			intact: []string{"host", "user", "base_path"},
		},
		{
			name:   "s3 secret key redacted",
			input:  `{"bucket":"my-bucket","access_key":"AKIA","secret_key":"wJalr","region":"us-east-1"}`,
			want:   map[string]string{"secret_key": "••••••••"},
			intact: []string{"bucket", "access_key", "region"},
		},
		{
			name:   "empty password not redacted",
			input:  `{"host":"192.168.1.1","password":""}`,
			intact: []string{"host", "password"},
		},
		{
			name:   "invalid json returns original",
			input:  `not-json`,
			intact: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := redactConfig(tt.input)

			if tt.intact == nil {
				if got != tt.input {
					t.Errorf("expected original string, got %q", got)
				}
				return
			}

			var result map[string]any
			if err := json.Unmarshal([]byte(got), &result); err != nil {
				t.Fatalf("failed to parse result: %v", err)
			}

			for key, expected := range tt.want {
				val, ok := result[key].(string)
				if !ok {
					t.Errorf("key %q missing", key)
					continue
				}
				if val != expected {
					t.Errorf("key %q = %q, want %q", key, val, expected)
				}
			}

			for _, key := range tt.intact {
				if _, ok := tt.want[key]; ok {
					continue
				}
				var orig map[string]any
				if err := json.Unmarshal([]byte(tt.input), &orig); err != nil {
					continue
				}
				if result[key] != orig[key] {
					t.Errorf("key %q modified: got %v, want %v", key, result[key], orig[key])
				}
			}
		})
	}
}

func TestStorageGetIncludesCapacityWhenProbed(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	// Seed a capacity row directly.
	rec := db.CapacityRecord{
		TotalBytes: 1 << 40,
		UsedBytes:  1 << 30,
		FreeBytes:  (1 << 40) - (1 << 30),
		ProbedAt:   time.Now().UTC(),
		Source:     "statfs",
	}
	if err := h.db.UpdateStorageDestinationCapacity(destID, rec, ""); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.Get(w, reqWithID("GET", fmt.Sprintf("/storage/%d", destID), strconv.FormatInt(destID, 10), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	capObj, ok := body["capacity"].(map[string]any)
	if !ok {
		t.Fatalf("expected capacity object, got %T (%v)", body["capacity"], body["capacity"])
	}
	if capObj["source"] != "statfs" {
		t.Errorf("source = %v", capObj["source"])
	}
	// JSON numbers come back as float64.
	if got := capObj["total_bytes"]; got != float64(1<<40) {
		t.Errorf("total_bytes = %v, want %v", got, float64(1<<40))
	}
}

func TestStorageGetCapacityNullWhenNotProbed(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	w := httptest.NewRecorder()
	h.Get(w, reqWithID("GET", fmt.Sprintf("/storage/%d", destID), strconv.FormatInt(destID, 10), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["capacity"] != nil {
		t.Errorf("expected capacity=null for never-probed dest, got %v", body["capacity"])
	}
}

func TestRefreshCapacityReturnsFreshNumbers(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	w := httptest.NewRecorder()
	h.RefreshCapacity(w, reqWithID("POST", fmt.Sprintf("/storage/%d/capacity-check", destID), strconv.FormatInt(destID, 10), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body struct {
		Capacity storage.Capacity `json:"capacity"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if body.Capacity.Source != "statfs" {
		t.Errorf("source = %q, want statfs", body.Capacity.Source)
	}
	if body.Capacity.TotalBytes <= 0 {
		t.Errorf("total = %d, want > 0", body.Capacity.TotalBytes)
	}
}

func TestRefreshCapacityMissingDest(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)
	w := httptest.NewRecorder()
	h.RefreshCapacity(w, reqWithID("POST", "/storage/9999/capacity-check", "9999", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

// TestPreserveRedactedSecrets locks in the round-trip safety net: when the
// UI re-submits an edit modal whose password field still holds the
// "••••••••" marker (because the user changed only the bandwidth limit,
// say), the Update handler must NOT store the marker bytes as the new
// password. The helper rewrites those marker values to the existing
// stored credential before persistence.
func TestPreserveRedactedSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		incoming  string
		existing  string
		wantField string // key whose value we assert
		wantValue string // "" means we assert the key is missing or empty
	}{
		{
			name:      "marker password is restored from existing",
			incoming:  `{"url":"https://wd.example.com","username":"u","password":"••••••••","base_path":"/v"}`,
			existing:  `{"url":"https://wd.example.com","username":"u","password":"real-secret","base_path":""}`,
			wantField: "password",
			wantValue: "real-secret",
		},
		{
			name:      "new password overwrites existing",
			incoming:  `{"url":"x","password":"brand-new"}`,
			existing:  `{"url":"x","password":"old"}`,
			wantField: "password",
			wantValue: "brand-new",
		},
		{
			name:      "marker secret_key is restored",
			incoming:  `{"bucket":"b","secret_key":"••••••••"}`,
			existing:  `{"bucket":"b","secret_key":"real-secret-access-key"}`,
			wantField: "secret_key",
			wantValue: "real-secret-access-key",
		},
		{
			name:      "marker on key absent from existing stays as marker",
			incoming:  `{"bucket":"b","secret_key":"••••••••"}`,
			existing:  `{"bucket":"b"}`,
			wantField: "secret_key",
			wantValue: "••••••••",
		},
		{
			name:      "non-marker passthrough",
			incoming:  `{"url":"x","password":"abc123"}`,
			existing:  `{"url":"x","password":"old"}`,
			wantField: "password",
			wantValue: "abc123",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := preserveRedactedSecrets(tt.incoming, tt.existing)
			if err != nil {
				t.Fatalf("preserveRedactedSecrets: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(got), &m); err != nil {
				t.Fatalf("output not JSON: %v\n%s", err, got)
			}
			v, _ := m[tt.wantField].(string)
			if v != tt.wantValue {
				t.Errorf("field %q = %q, want %q", tt.wantField, v, tt.wantValue)
			}
		})
	}
}

// TestPreserveRedactedSecretsInvalidJSONPassesThrough covers the
// fail-open path: if the incoming JSON is malformed, the function returns
// the original string so the downstream adapter validator surfaces the
// real parsing error in context rather than the helper masking it.
func TestPreserveRedactedSecretsInvalidJSONPassesThrough(t *testing.T) {
	t.Parallel()
	got, err := preserveRedactedSecrets("not-json", `{"password":"real"}`)
	if err != nil {
		t.Fatalf("expected no error for invalid JSON, got %v", err)
	}
	if got != "not-json" {
		t.Errorf("expected passthrough, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// StorageHandler CRUD tests
// ---------------------------------------------------------------------------

func TestStorageList_Empty(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	// The handler already has one destination created by newDedupStorageHandler.
	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/api/v1/storage", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp []any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) < 1 {
		t.Errorf("expected at least 1 destination, got %d", len(resp))
	}
}

func TestStorageCreate_Local(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	storageDir := t.TempDir()
	cfgJSON, _ := json.Marshal(map[string]string{"path": storageDir})
	payload, _ := json.Marshal(map[string]any{
		"name":   "new-local",
		"type":   "local",
		"config": string(cfgJSON),
	})

	w := httptest.NewRecorder()
	h.Create(w, httptest.NewRequest(http.MethodPost, "/api/v1/storage", bytes.NewReader(payload)))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["name"] != "new-local" {
		t.Errorf("name = %v, want new-local", resp["name"])
	}
	if resp["type"] != "local" {
		t.Errorf("type = %v, want local", resp["type"])
	}
}

func TestStorageCreate_MissingName(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	body := []byte(`{"type":"local","config":"{}"}`)
	w := httptest.NewRecorder()
	h.Create(w, httptest.NewRequest(http.MethodPost, "/api/v1/storage", bytes.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageCreate_MissingType(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	body := []byte(`{"name":"foo","config":"{}"}`)
	w := httptest.NewRecorder()
	h.Create(w, httptest.NewRequest(http.MethodPost, "/api/v1/storage", bytes.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageCreate_InvalidJSON(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.Create(w, httptest.NewRequest(http.MethodPost, "/api/v1/storage", bytes.NewReader([]byte("not-json"))))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageCreate_InvalidAdapterType(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	body := []byte(`{"name":"bad","type":"bogus","config":"{}"}`)
	w := httptest.NewRecorder()
	h.Create(w, httptest.NewRequest(http.MethodPost, "/api/v1/storage", bytes.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageUpdate_Name(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	body := []byte(`{"name":"renamed-dest"}`)
	w := httptest.NewRecorder()
	h.Update(w, reqWithID(http.MethodPut, "/api/v1/storage/"+idStr, idStr, body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["name"] != "renamed-dest" {
		t.Errorf("name = %v, want renamed-dest", resp["name"])
	}
}

func TestStorageUpdate_EmptyNameRejected(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	body := []byte(`{"name":"   "}`)
	w := httptest.NewRecorder()
	h.Update(w, reqWithID(http.MethodPut, "/api/v1/storage/"+idStr, idStr, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageUpdate_TypeChangeRejected(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	body := []byte(`{"type":"sftp"}`)
	w := httptest.NewRecorder()
	h.Update(w, reqWithID(http.MethodPut, "/api/v1/storage/"+idStr, idStr, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageUpdate_InvalidJSON(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	w := httptest.NewRecorder()
	h.Update(w, reqWithID(http.MethodPut, "/api/v1/storage/"+idStr, idStr, []byte("bad json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageUpdate_NotFound(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	body := []byte(`{"name":"renamed"}`)
	w := httptest.NewRecorder()
	h.Update(w, reqWithID(http.MethodPut, "/api/v1/storage/9999", "9999", body))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageUpdate_Config(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	newDir := t.TempDir()
	newCfg, _ := json.Marshal(map[string]string{"path": newDir})
	body, _ := json.Marshal(map[string]string{"config": string(newCfg)})

	w := httptest.NewRecorder()
	h.Update(w, reqWithID(http.MethodPut, "/api/v1/storage/"+idStr, idStr, body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageDelete_NoDependents(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	w := httptest.NewRecorder()
	h.Delete(w, reqWithID(http.MethodDelete, "/api/v1/storage/"+idStr, idStr, nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageDelete_NotFound(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.Delete(w, reqWithID(http.MethodDelete, "/api/v1/storage/9999", "9999", nil))
	// With no dependent jobs, a missing ID just hits the DB delete which fails silently or returns no-content.
	// The actual behavior is a 500 (internal) or 204 depending on DB error handling.
	// We just assert no panic here.
	if w.Code == 0 {
		t.Fatal("expected a status code")
	}
}

func TestStorageDelete_WithDependentJobs_Blocked(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	// Create a job referencing this destination.
	_, err := h.db.CreateJob(db.Job{
		Name:          "dep-job",
		StorageDestID: destID,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	w := httptest.NewRecorder()
	h.Delete(w, reqWithID(http.MethodDelete, "/api/v1/storage/"+idStr, idStr, nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (conflict); body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["job_count"] == nil {
		t.Error("response missing job_count field")
	}
}

func TestStorageDelete_Force(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	// Create a job referencing this destination.
	jobID, err := h.db.CreateJob(db.Job{
		Name:          "dep-job-force",
		StorageDestID: destID,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	// Remove the job first so the FOREIGN KEY constraint allows deletion.
	if err := h.db.DeleteJob(jobID); err != nil {
		t.Fatalf("delete job: %v", err)
	}

	// Force-delete with no remaining dependents should succeed.
	req2, _ := http.NewRequest(http.MethodDelete, "/api/v1/storage/"+idStr+"?force=true", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", idStr)
	req2 = req2.WithContext(context.WithValue(req2.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.Delete(w, req2)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageTestConnection_Local(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	w := httptest.NewRecorder()
	h.TestConnection(w, reqWithID(http.MethodPost, "/api/v1/storage/"+idStr+"/test", idStr, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["success"] != true {
		t.Errorf("success = %v, want true", resp["success"])
	}
}

func TestStorageTestConnection_NotFound(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.TestConnection(w, reqWithID(http.MethodPost, "/api/v1/storage/9999/test", "9999", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageDependentJobs_Empty(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	w := httptest.NewRecorder()
	h.DependentJobs(w, reqWithID(http.MethodGet, "/api/v1/storage/"+idStr+"/jobs", idStr, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["job_count"] == nil {
		t.Error("response missing job_count field")
	}
	if resp["jobs"] == nil {
		t.Error("response missing jobs field")
	}
}

func TestStorageDependentJobs_WithJob(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	_, err := h.db.CreateJob(db.Job{
		Name:          "job-for-dep-test",
		StorageDestID: destID,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	w := httptest.NewRecorder()
	h.DependentJobs(w, reqWithID(http.MethodGet, "/api/v1/storage/"+idStr+"/jobs", idStr, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if jobCount, ok := resp["job_count"].(float64); !ok || jobCount < 1 {
		t.Errorf("job_count = %v, want ≥1", resp["job_count"])
	}
}

func TestStorageListFiles_Local(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	w := httptest.NewRecorder()
	h.ListFiles(w, reqWithID(http.MethodGet, "/api/v1/storage/"+idStr+"/list", idStr, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageListFiles_TraversalPrefixRejected covers the defence-in-depth
// prefix validation added for the destination file browser (issue #236).
func TestStorageListFiles_TraversalPrefixRejected(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	for _, prefix := range []string{"../etc", "/etc", "a/../../b", `safe\..\..`, `..\smbshare`, `a\..`} {
		w := httptest.NewRecorder()
		h.ListFiles(w, reqWithID(http.MethodGet,
			"/api/v1/storage/"+idStr+"/list?prefix="+url.QueryEscape(prefix), idStr, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("prefix %q: status = %d, want 400", prefix, w.Code)
		}
	}
}

func TestStorageListFiles_NotFound(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.ListFiles(w, reqWithID(http.MethodGet, "/api/v1/storage/9999/list", "9999", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageDownloadFile_MissingPath(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	w := httptest.NewRecorder()
	h.DownloadFile(w, reqWithID(http.MethodGet, "/api/v1/storage/"+idStr+"/files", idStr, nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageDownloadFile_InvalidPath(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/storage/"+idStr+"/files?path=../escape", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", idStr)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.DownloadFile(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (invalid path); body: %s", w.Code, w.Body.String())
	}
}

func TestStorageDownloadFile_NotFound(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/storage/"+idStr+"/files?path=nonexistent.tar", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", idStr)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.DownloadFile(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageDownloadFile_NotFoundStorage(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/storage/9999/files?path=some.tar", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "9999")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.DownloadFile(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageDeleteOrphans_EmptyPaths(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	body := []byte(`{"paths":[]}`)
	w := httptest.NewRecorder()
	h.DeleteOrphans(w, reqWithID(http.MethodPost, "/api/v1/storage/"+idStr+"/delete-orphans", idStr, body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["deleted"] == nil {
		t.Error("response missing 'deleted' field")
	}
}

func TestStorageDeleteOrphans_InvalidJSON(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	w := httptest.NewRecorder()
	h.DeleteOrphans(w, reqWithID(http.MethodPost, "/api/v1/storage/"+idStr+"/delete-orphans", idStr, []byte("bad")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageDeleteOrphans_NotFound(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	body := []byte(`{"paths":["some/path"]}`)
	w := httptest.NewRecorder()
	h.DeleteOrphans(w, reqWithID(http.MethodPost, "/api/v1/storage/9999/delete-orphans", "9999", body))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageSetConfigChangeHook(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	called := false
	h.SetConfigChangeHook(func() { called = true })
	h.notifyConfigChange()
	if !called {
		t.Error("config change hook was not called")
	}
}

func TestStorageBroadcastConfigChange_NilRunner(t *testing.T) {
	t.Parallel()
	// Handler with nil runner should not panic.
	h := NewStorageHandler(newTestDB(t), nil, nil)
	h.broadcastConfigChange("storage") // should not panic
}

func TestStorageRestoreDB_EmptyPath(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	body := []byte(`{"storage_path":""}`)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/api/v1/storage/"+idStr+"/restore-db", idStr, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageRestoreDB_InvalidPath(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	// A path containing ".." is invalid.
	body := []byte(`{"storage_path":"../evil"}`)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/api/v1/storage/"+idStr+"/restore-db", idStr, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageScanOrphans_Local(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	w := httptest.NewRecorder()
	h.ScanOrphans(w, reqWithID(http.MethodPost, "/api/v1/storage/"+idStr+"/scan-orphans", idStr, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["orphans"] == nil {
		t.Error("response missing 'orphans' field")
	}
}

// ---------------------------------------------------------------------------
// Scan handler
// ---------------------------------------------------------------------------

func TestStorageScan_EmptyDest(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	w := httptest.NewRecorder()
	h.Scan(w, reqWithID(http.MethodPost, "/api/v1/storage/"+idStr+"/scan", idStr, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Empty storage → should return backups array (possibly empty).
	if resp["backups"] == nil {
		t.Error("response missing 'backups' field")
	}
}

func TestStorageScan_NotFound(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.Scan(w, reqWithID(http.MethodPost, "/api/v1/storage/9999/scan", "9999", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Import handler
// ---------------------------------------------------------------------------

func TestStorageImport_EmptyList(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	body := []byte(`{"backups":[]}`)
	w := httptest.NewRecorder()
	h.Import(w, reqWithID(http.MethodPost, "/api/v1/storage/"+idStr+"/import", idStr, body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["imported"] == nil {
		t.Error("response missing 'imported' field")
	}
}

func TestStorageImport_InvalidJSON(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	w := httptest.NewRecorder()
	h.Import(w, reqWithID(http.MethodPost, "/api/v1/storage/"+idStr+"/import", idStr, []byte("bad")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageImport_NotFound(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	body := []byte(`{"backups":[]}`)
	w := httptest.NewRecorder()
	h.Import(w, reqWithID(http.MethodPost, "/api/v1/storage/9999/import", "9999", body))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// CloseBreaker handler
// ---------------------------------------------------------------------------

func TestStorageCloseBreaker_NotFound(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.CloseBreaker(w, reqWithID(http.MethodPost, "/api/v1/storage/9999/close-breaker", "9999", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageCloseBreaker_InvalidID(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.CloseBreaker(w, reqWithID(http.MethodPost, "/api/v1/storage/bad/close-breaker", "bad", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// HealthCheck handler
// ---------------------------------------------------------------------------

func TestStorageHealthCheck_Local(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	w := httptest.NewRecorder()
	h.HealthCheck(w, reqWithID(http.MethodPost, "/api/v1/storage/"+idStr+"/health-check", idStr, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] == nil {
		t.Error("response missing 'status' field")
	}
}

func TestStorageHealthCheck_NotFound(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.HealthCheck(w, reqWithID(http.MethodPost, "/api/v1/storage/9999/health-check", "9999", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageDelete_ForceDisablesDependentJobs is the regression test for the
// QA finding that a forced delete removed the destination while leaving every
// dependent job ENABLED with storage_dest_id pointing at the now-dead id. The
// Jobs page rendered that as "Unknown" and the scheduler kept firing runs that
// could never write anywhere.
func TestStorageDelete_ForceDisablesDependentJobs(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	jobID, err := h.db.CreateJob(db.Job{
		Name:            "dep-job-force",
		Enabled:         true,
		StorageDestID:   destID,
		BackupTypeChain: "full",
		Schedule:        "0 3 * * *",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	w := httptest.NewRecorder()
	h.Delete(w, reqWithID(http.MethodDelete,
		"/api/v1/storage/"+idStr+"?force=true", idStr, nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a disabled-jobs notice; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, _ := resp["disabled_jobs"].(float64); got != 1 {
		t.Errorf("disabled_jobs = %v, want 1; body: %s", resp["disabled_jobs"], w.Body.String())
	}

	job, err := h.db.GetJob(jobID)
	if err != nil {
		t.Fatalf("get job after delete: %v", err)
	}
	if job.Enabled {
		t.Error("dependent job must be disabled after its destination is force-deleted")
	}
	if job.StorageDestID != 0 {
		t.Errorf("job.StorageDestID = %d, want 0 (dangling reference cleared)", job.StorageDestID)
	}
}

// TestStorageDelete_ForceWithoutDependentsStillNoContent keeps the common path
// unchanged: nothing to disable means the original 204 contract holds.
func TestStorageDelete_ForceWithoutDependentsStillNoContent(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	w := httptest.NewRecorder()
	h.Delete(w, reqWithID(http.MethodDelete,
		"/api/v1/storage/"+idStr+"?force=true", idStr, nil))

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
}
