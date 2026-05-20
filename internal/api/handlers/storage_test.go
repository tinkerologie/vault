package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	return NewStorageHandler(d, r), destID
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
