package replication

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/credentials"
	"github.com/tinkerologie/vault/internal/db"
)

func TestClientDoesNotForwardKeyAcrossRedirect(t *testing.T) {
	t.Parallel()
	var leaked string
	evil := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("X-API-Key")
	}))
	defer evil.Close()
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+r.URL.Path, http.StatusFound)
	}))
	defer peer.Close()

	c, err := NewClientWithAPIKey(peer.URL, "peer-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.TestConnection(); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("err = %v", err)
	}
	if leaked != "" {
		t.Fatalf("key reached the redirect target: %q", leaked)
	}
}

func TestClientRedirectRules(t *testing.T) {
	t.Parallel()
	mk := func(u string) *http.Request {
		r, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	via := []*http.Request{mk("https://peer/a")}
	if err := refuseKeyLeakingRedirect(mk("https://peer/b"), via); err != nil {
		t.Errorf("same host: %v", err)
	}
	if err := refuseKeyLeakingRedirect(mk("http://peer/b"), via); err == nil {
		t.Error("https downgrade allowed")
	}
	if err := refuseKeyLeakingRedirect(mk("https://peer/b"), []*http.Request{mk("http://peer/a")}); err != nil {
		t.Errorf("upgrade to https: %v", err)
	}
	if err := refuseKeyLeakingRedirect(mk("https://peer/b"), make([]*http.Request, 10)); err == nil {
		t.Error("redirect limit not applied")
	}
}

func TestPlaintextWarningKeepsHTTPUsable(t *testing.T) {
	t.Parallel()
	if _, err := NormalizeBaseURL("http://192.0.2.5:24085"); err != nil {
		t.Fatalf("LAN HTTP must stay allowed: %v", err)
	}
	if PlaintextWarning("http://192.0.2.5:24085") == "" {
		t.Error("no warning for LAN HTTP")
	}
	if PlaintextWarning("https://vault.example") != "" || PlaintextWarning("http://127.0.0.1:24085") != "" {
		t.Error("warned about a safe URL")
	}
}

func TestSyncFailsClosedOnLockedKey(t *testing.T) {
	t.Parallel()
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	d := openTestDB(t)
	id, err := d.CreateReplicationSource(db.ReplicationSource{Name: "p", Type: "remote_vault", URL: srv.URL, Config: `{"api_key":"vault-sealed:v1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewSyncer(d, nil).SyncSource(id, nil)
	if !errors.Is(err, credentials.ErrLocked) {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Fatal("contacted the peer without its key")
	}
	src, _ := d.GetReplicationSource(id)
	if src.LastSyncStatus != "failed" || !strings.Contains(src.LastSyncError, "vault.key") {
		t.Fatalf("status %q error %q", src.LastSyncStatus, src.LastSyncError)
	}
}
