package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeKeyDaemon stands in for the daemon's key routes; the routes themselves
// are tested in internal/api.
func fakeKeyDaemon(enabled *bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+keyMgmtRoute, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"enabled": *enabled})
	})
	issue := func(w http.ResponseWriter, _ *http.Request) {
		*enabled = true
		_ = json.NewEncoder(w).Encode(map[string]string{"api_key": "vault_fixture"})
	}
	mux.HandleFunc("POST "+keyMgmtRoute+"/generate", issue)
	mux.HandleFunc("POST "+keyMgmtRoute+"/rotate", func(w http.ResponseWriter, r *http.Request) {
		if !*enabled {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "no existing API key to rotate"})
			return
		}
		issue(w, r)
	})
	mux.HandleFunc("GET "+keyMgmtRoute+"/key", issue)
	mux.HandleFunc("DELETE "+keyMgmtRoute, func(w http.ResponseWriter, _ *http.Request) {
		*enabled = false
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func runAPIKeyCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(append([]string{"api-key"}, args...))
	t.Cleanup(func() { rootCmd.SetOut(nil); rootCmd.SetErr(nil); rootCmd.SetArgs(nil) })
	err := rootCmd.Execute()
	return out.String(), err
}

func TestAPIKeyCommands(t *testing.T) {
	enabled := false
	srv := httptest.NewServer(fakeKeyDaemon(&enabled))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	flags := []string{"--addr", addr, "--tls=false"}

	run := func(cmd string) (string, error) { return runAPIKeyCmd(t, append([]string{cmd}, flags...)...) }

	if out, err := run("status"); err != nil || !strings.Contains(out, "not configured") {
		t.Fatalf("status before generate = %q, %v", out, err)
	}
	if _, err := run("rotate"); err == nil || !strings.Contains(err.Error(), "no existing API key") {
		t.Fatalf("rotate with no key: %v, want the daemon's error", err)
	}
	out, err := run("generate")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out != "vault_fixture\n" {
		t.Errorf("generate output = %q, want only the key", out)
	}
	if _, err := run("generate"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second generate: %v, want already-exists error", err)
	}
	if out, err := run("status"); err != nil || !strings.Contains(out, "configured") || strings.Contains(out, "not configured") {
		t.Errorf("status after generate = %q, %v", out, err)
	}
	if out, err := run("show"); err != nil || out != "vault_fixture\n" {
		t.Errorf("show = %q, %v", out, err)
	}
	if out, err := run("rotate"); err != nil || out != "vault_fixture\n" {
		t.Errorf("rotate = %q, %v", out, err)
	}
	if out, err := run("revoke"); err != nil || !strings.Contains(out, "revoked") {
		t.Errorf("revoke = %q, %v", out, err)
	}
	if enabled {
		t.Error("revoke did not reach the daemon")
	}
}

func TestAPIKeyCommandOverTLS(t *testing.T) {
	enabled := false
	srv := httptest.NewTLSServer(fakeKeyDaemon(&enabled))
	defer srv.Close()
	out, err := runAPIKeyCmd(t, "status", "--addr", strings.TrimPrefix(srv.URL, "https://"), "--tls")
	if err != nil || !strings.Contains(out, "not configured") {
		t.Errorf("status over TLS = %q, %v", out, err)
	}
	if err := apiKeyCmd.PersistentFlags().Set("tls", "false"); err != nil {
		t.Fatal(err)
	}
}

func TestAPIKeyCommandFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case keyMgmtRoute + "/key":
			_, _ = w.Write([]byte(`{}`)) // success with no key
		case keyMgmtRoute + "/rotate":
			_, _ = w.Write([]byte(`not json`))
		case keyMgmtRoute:
			if r.Method == http.MethodDelete {
				http.Error(w, "nope", http.StatusTeapot) // non-JSON refusal
				return
			}
			_, _ = w.Write([]byte(`not json`))
		}
	}))
	addr := strings.TrimPrefix(srv.URL, "http://")
	flags := []string{"--addr", addr, "--tls=false"}
	for _, tc := range []struct{ cmd, want string }{
		{"show", "returned no API key"},
		{"rotate", "reading API key response"},
		{"status", "reading API key status"},
		{"generate", "reading API key status"},
		{"revoke", "418"},
	} {
		if _, err := runAPIKeyCmd(t, append([]string{tc.cmd}, flags...)...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want error containing %q", tc.cmd, err, tc.want)
		}
	}
	srv.Close()
	if _, err := runAPIKeyCmd(t, "status", "--addr", addr, "--tls=false"); err == nil || !strings.Contains(err.Error(), "is it running") {
		t.Errorf("stopped daemon: %v", err)
	}
}

func TestResolveLocalDaemonAddr(t *testing.T) {
	original := daemonCfgPath
	t.Cleanup(func() { daemonCfgPath = original })

	daemonCfgPath = filepath.Join(t.TempDir(), "missing.cfg")
	if got, err := resolveLocalDaemonAddr(""); err != nil || got != "127.0.0.1:24085" {
		t.Errorf("default = %q, %v", got, err)
	}

	cfg := filepath.Join(t.TempDir(), "vault.cfg")
	if err := os.WriteFile(cfg, []byte("BIND_ADDRESS='0.0.0.0'\nPORT='9999'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	daemonCfgPath = cfg
	if got, err := resolveLocalDaemonAddr(""); err != nil || got != "127.0.0.1:9999" {
		t.Errorf("wildcard from cfg = %q, %v", got, err)
	}

	if got, err := resolveLocalDaemonAddr("[::1]:24085"); err != nil || got != "[::1]:24085" {
		t.Errorf("ipv6 = %q, %v", got, err)
	}
	for _, bad := range []string{"203.0.113.9:24085", "tower:24085", "no-port"} {
		if _, err := resolveLocalDaemonAddr(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
	if isHostInterfaceIP(net.ParseIP("203.0.113.9")) {
		t.Error("a documentation address is not on this host")
	}
}
