package storage

import (
	"errors"
	"testing"

	"github.com/tinkerologie/vault/internal/credentials"
)

func TestNewAdapterRefusesLockedCredentials(t *testing.T) {
	t.Parallel()
	_, err := NewAdapter("smb", `{"host":"nas","share":"b","password":"vault-sealed:v1:abc"}`)
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
