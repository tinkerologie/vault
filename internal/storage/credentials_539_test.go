package storage

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/tinkerologie/vault/internal/credentials"
)

func TestNewAdapterRefusesLockedCredentials(t *testing.T) {
	t.Parallel()
	_, err := NewAdapter("smb", `{"host":"nas","share":"b","password":"vault-sealed:v1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`)
	if !errors.Is(err, credentials.ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
}

func TestWebDAVMovesURLCredentialsIntoFields(t *testing.T) {
	t.Parallel()
	a, err := NewWebDAVAdapter(WebDAVConfig{URL: "https://bob:pw@dav.example/x/"})
	if err != nil {
		t.Fatal(err)
	}
	if a.config.URL != "https://dav.example/x" || a.config.Username != "bob" || a.config.Password != "pw" {
		t.Fatalf("config = %+v", a.config.URL)
	}
	// Explicit fields win.
	a, err = NewWebDAVAdapter(WebDAVConfig{URL: "https://bob:pw@dav.example/", Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if a.config.URL != "https://dav.example" || a.config.Username != "u" || a.config.Password != "p" {
		t.Fatalf("config = %+v", a.config.URL)
	}
}

func TestPlainHTTPWebDAVStaysAllowed(t *testing.T) {
	t.Parallel()
	for range 2 { // the second build covers the warned-once path
		if _, err := NewWebDAVAdapter(WebDAVConfig{URL: "http://nas.lan:8080/"}); err != nil {
			t.Fatalf("LAN HTTP must keep working: %v", err)
		}
	}
}

func TestWebDAVFillsEachMissingCredentialFromURL(t *testing.T) {
	t.Parallel()
	a, err := NewWebDAVAdapter(WebDAVConfig{URL: "https://bob:pw@dav.example/", Username: "amy"})
	if err != nil {
		t.Fatal(err)
	}
	if a.config.Username != "amy" || a.config.Password != "pw" {
		t.Fatalf("username %q, password set %v", a.config.Username, a.config.Password != "")
	}
	a, err = NewWebDAVAdapter(WebDAVConfig{URL: "https://bob:pw@dav.example/", Password: "typed"})
	if err != nil {
		t.Fatal(err)
	}
	if a.config.Username != "bob" || a.config.Password != "typed" {
		t.Fatalf("username %q", a.config.Username)
	}
}

func TestValidateConfigAcceptsLockedSecretsOnly(t *testing.T) {
	t.Parallel()
	locked := `{"host":"nas","share":"b","password":"vault-sealed:v1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	if err := ValidateConfig("smb", locked); err != nil {
		t.Fatalf("locked secret: %v", err)
	}
	sealedURL := `{"url":"vault-sealed:v1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	if err := ValidateConfig("webdav", sealedURL); err != nil {
		t.Fatalf("locked URL: %v", err)
	}
	if err := ValidateConfig("bogus", `{}`); err == nil {
		t.Fatal("unknown type accepted")
	}
}

func redirectRequest(t *testing.T, from, to string) *http.Request {
	t.Helper()
	prev, err := http.NewRequest(http.MethodGet, from, nil)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, to, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("u", "p")
	req.Response = &http.Response{Request: prev}
	return req
}

type recordingTransport struct{ calls int }

func (r *recordingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls++
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

func TestCredentialRedirectGuard(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, from, to string
		allowed        bool
	}{
		{"same host", "https://dav/a", "https://dav/b", true},
		{"upgrade", "http://dav/a", "https://dav/b", true},
		{"downgrade", "https://dav/a", "http://dav/b", false},
		{"other host", "https://dav/a", "https://evil/b", false},
		{"other port", "https://dav/a", "https://dav:8443/b", false},
		{"same port spelled out", "https://dav/a", "https://dav:443/b", true},
		{"upgrade to another port", "http://dav/a", "https://dav:8443/b", false},
	} {
		rec := &recordingTransport{}
		resp, err := credentialRedirectGuard{base: rec}.RoundTrip(redirectRequest(t, tc.from, tc.to))
		if resp != nil {
			_ = resp.Body.Close()
		}
		if tc.allowed != (err == nil) || tc.allowed != (rec.calls == 1) {
			t.Errorf("%s: err %v, calls %d", tc.name, err, rec.calls)
		}
	}
}

// The real range-read path: an https server redirecting to http must not receive
// the login in the clear.
func TestWebDAVRangeReadRefusesDowngradeRedirect(t *testing.T) {
	t.Parallel()
	var sawAuth atomic.Bool
	plain := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth.Store(true)
		}
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusFound)
	}))
	defer secure.Close()
	a, err := NewWebDAVAdapter(WebDAVConfig{URL: secure.URL, Username: "u", Password: "p", InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if body, err := a.ReadRange("file", 0, 10); err == nil {
		_ = body.Close()
		t.Fatal("redirect followed")
	}
	if sawAuth.Load() {
		t.Fatal("login sent over plain HTTP")
	}
}
