package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
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

func TestGetEncryptionPassphrase(t *testing.T) {
	t.Parallel()

	// Create a server key for sealing.
	keyPath := filepath.Join(t.TempDir(), "vault.key")
	serverKey, err := crypto.LoadOrCreateServerKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("returns 404 when no passphrase set", func(t *testing.T) {
		t.Parallel()
		database := testDB(t)
		srv := NewServer(database, ServerConfig{Addr: ":0", ServerKey: serverKey})

		req := httptest.NewRequest("GET", "/api/v1/settings/encryption/passphrase", nil)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
		}
	})

	t.Run("returns passphrase when set", func(t *testing.T) {
		t.Parallel()
		database := testDB(t)
		srv := NewServer(database, ServerConfig{Addr: ":0", ServerKey: serverKey})

		// Set a passphrase via the API.
		body, _ := json.Marshal(map[string]string{"passphrase": "my-test-passphrase"})
		setReq := httptest.NewRequest("POST", "/api/v1/settings/encryption", bytes.NewReader(body))
		setReq.Header.Set("Content-Type", "application/json")
		setW := httptest.NewRecorder()
		srv.router.ServeHTTP(setW, setReq)

		if setW.Code != http.StatusOK {
			t.Fatalf("set passphrase status = %d, want %d", setW.Code, http.StatusOK)
		}

		// Retrieve the passphrase.
		getReq := httptest.NewRequest("GET", "/api/v1/settings/encryption/passphrase", nil)
		getW := httptest.NewRecorder()
		srv.router.ServeHTTP(getW, getReq)

		if getW.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", getW.Code, http.StatusOK)
		}

		var resp map[string]string
		json.NewDecoder(getW.Body).Decode(&resp)
		if resp["passphrase"] != "my-test-passphrase" {
			t.Errorf("passphrase = %q, want %q", resp["passphrase"], "my-test-passphrase")
		}
	})
}
