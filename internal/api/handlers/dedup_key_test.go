package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// TestGetServerKeyDownload checks vault.key is served as the raw 32-byte
// file, never cached, and refused when the server has no key.
func TestGetServerKeyDownload(t *testing.T) {
	h := newTestSettingsHandler(t)
	w := httptest.NewRecorder()
	h.GetServerKey(w, httptest.NewRequest(http.MethodGet, "/api/v1/settings/server-key", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), h.serverKey) || w.Body.Len() != 32 {
		t.Fatalf("body is %d bytes, want the 32-byte server key", w.Body.Len())
	}
	for header, want := range map[string]string{
		"Content-Type":        "application/octet-stream",
		"Content-Disposition": `attachment; filename="vault.key"`,
		"Cache-Control":       "no-store",
		"Content-Length":      "32",
	} {
		if got := w.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	h.serverKey = nil
	w = httptest.NewRecorder()
	h.GetServerKey(w, httptest.NewRequest(http.MethodGet, "/api/v1/settings/server-key", nil))
	if w.Code != http.StatusServiceUnavailable || w.Body.Len() == 32 {
		t.Fatalf("no key: status %d", w.Code)
	}
}

// foreignDedupDest creates a dedup destination initialised by another server
// (key 0x11), optionally with a passphrase escrow.
func foreignDedupDest(t *testing.T, d *db.DB, escrowPass string) db.StorageDestination {
	t.Helper()
	dest := seedDedupDest(t, d)
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.CloseAdapter(adapter)
	repo, err := dedup.InitRepo(d, adapter, dest.ID, bytes.Repeat([]byte{0x11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if escrowPass != "" {
		if _, err := repo.EnsurePassphraseEscrow(escrowPass); err != nil {
			t.Fatal(err)
		}
	}
	return dest
}

func postRewrap(t *testing.T, h *StorageHandler, body string) map[int64]runner.DedupKeyResult {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage/dedup-keys/rewrap", strings.NewReader(body))
	if body == "" {
		req.ContentLength = 0
	}
	w := httptest.NewRecorder()
	h.RewrapDedupKeys(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Destinations []runner.DedupKeyResult `json:"destinations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	out := map[int64]runner.DedupKeyResult{}
	for _, r := range resp.Destinations {
		out[r.StorageID] = r
	}
	return out
}

// TestRewrapDedupKeysEndpoint walks the Recovery wizard's call: a status
// check without a passphrase, a wrong passphrase, then the real rewrap.
func TestRewrapDedupKeysEndpoint(t *testing.T) {
	h, ownID := newDedupStorageHandler(t, true)
	escrowed := foreignDedupDest(t, h.db, "pw")
	bare := foreignDedupDest(t, h.db, "")

	got := postRewrap(t, h, "")
	if got[ownID].Status != "ok" || got[escrowed.ID].Status != "locked_needs_passphrase" || got[bare.ID].Status != "locked_needs_passphrase" {
		t.Fatalf("status check: %+v", got)
	}
	got = postRewrap(t, h, `{"passphrase":"nope"}`)
	if got[escrowed.ID].Status != "locked_wrong_passphrase" || got[bare.ID].Status != "locked_no_escrow" {
		t.Fatalf("wrong passphrase: %+v", got)
	}
	got = postRewrap(t, h, `{"passphrase":"pw"}`)
	if got[escrowed.ID].Status != "rewrapped" || got[ownID].Status != "ok" {
		t.Fatalf("rewrap: %+v", got)
	}
	if got = postRewrap(t, h, ""); got[escrowed.ID].Status != "ok" {
		t.Fatalf("after rewrap: %+v", got)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage/dedup-keys/rewrap", strings.NewReader("{"))
	w := httptest.NewRecorder()
	h.RewrapDedupKeys(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad body: status %d", w.Code)
	}
}

// TestRestorePointContentsDedupKeyMismatch checks browsing a destination
// sealed with another server's key returns 424 with a stable code and the
// recovery guidance, not a generic 500.
func TestRestorePointContentsDedupKeyMismatch(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	dest := foreignDedupDest(t, d, "")
	var id dedup.ID
	jobID, rpID := seedDedupRestorePoint(t, d, dest, "plex", "container", hex.EncodeToString(id[:]))

	w := browseContents(t, h, jobID, rpID, "plex")
	if w.Code != http.StatusFailedDependency {
		t.Fatalf("status = %d, want 424; body %s", w.Code, w.Body.String())
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["code"] != dedupKeyMismatchCode || !strings.Contains(body["error"], "vault.key") {
		t.Fatalf("body = %v", body)
	}
}

// TestRewrapDedupKeysWithoutRunner checks a handler without a runner refuses
// cleanly.
func TestRewrapDedupKeysWithoutRunner(t *testing.T) {
	h := &StorageHandler{}
	w := httptest.NewRecorder()
	h.RewrapDedupKeys(w, httptest.NewRequest(http.MethodPost, "/api/v1/storage/dedup-keys/rewrap", strings.NewReader(`{}`)))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", w.Code)
	}
}
