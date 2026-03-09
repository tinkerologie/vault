package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ruaandeysel/vault/internal/db"
)

func testDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestHealthEndpoint(t *testing.T) {
	database := testDB(t)
	srv := NewServer(database, ServerConfig{Addr: ":0"})

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("status = %q, want %q", resp["status"], "ok")
	}
}

// TestEncryptionPassphraseEndpointRemoved verifies that the passphrase
// read-back endpoint no longer exists (should return 404/405, not 200).
func TestEncryptionPassphraseEndpointRemoved(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	srv := NewServer(database, ServerConfig{Addr: ":0"})

	req := httptest.NewRequest("GET", "/api/v1/settings/encryption/passphrase", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Error("GET /api/v1/settings/encryption/passphrase should not return 200 — endpoint was removed")
	}
}

func TestGenerateAPIKeyRequiresBrowserBoundary(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	srv := NewServer(database, ServerConfig{Addr: ":0"})

	t.Run("blocks external bootstrap without same-origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
		}
	})

	t.Run("allows same-origin bootstrap once", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
		}

		var resp map[string]string
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}

		if !strings.HasPrefix(resp["api_key"], "vault_") {
			t.Fatalf("api_key = %q, want vault_ prefix", resp["api_key"])
		}
	})

	t.Run("returns conflict after bootstrap", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusConflict, w.Body.String())
		}
	})
}

func TestListenAddrs(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want []string
	}{
		{name: "loopback only", addr: "127.0.0.1:24085", want: []string{"127.0.0.1:24085"}},
		{name: "wildcard only", addr: ":24085", want: []string{":24085"}},
		{name: "all interfaces only", addr: "0.0.0.0:24085", want: []string{"0.0.0.0:24085"}},
		{name: "specific lan ip adds loopback", addr: "192.168.20.21:24085", want: []string{"127.0.0.1:24085", "192.168.20.21:24085"}},
		{name: "hostname adds loopback", addr: "vault.local:24085", want: []string{"127.0.0.1:24085", "vault.local:24085"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := NewServer(testDB(t), ServerConfig{Addr: tt.addr})
			got := srv.listenAddrs()
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("listenAddrs() = %v, want %v", got, tt.want)
			}
		})
	}
}
