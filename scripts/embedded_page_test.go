package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Plugin side of the embedded Backups view (#526). The browser behaviour lives
// in tests/embedded.spec.ts; here is what the shipped page and PHP contribute.

func TestBackupsPageRequestsEmbeddedPresentation(t *testing.T) {
	page := readRepoFile(t, "plugin/pages/Backups.page")
	for _, want := range []string{
		"/plugins/vault/include/app.php?embed=1#/",
		`/plugins/vault/include/embed-host.js?v=`,
		"VaultEmbedHost.attach(document.getElementById('vault-backups-frame'))",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Backups.page is missing %q", want)
		}
	}
	// The host script owns sizing and the CSRF reload, and only runs if the
	// frame exists.
	for _, gone := range []string{"function size()", "visibilitychange", "innerHeight"} {
		if strings.Contains(page, gone) {
			t.Errorf("Backups.page still contains %q", gone)
		}
	}
	if frame, attach := strings.Index(page, "<iframe"), strings.Index(page, "VaultEmbedHost.attach"); frame > attach {
		t.Error("the host script attaches before the frame exists")
	}
	if notRunning, attach := strings.Index(page, "<?php else: ?>"), strings.Index(page, "VaultEmbedHost.attach"); notRunning > attach {
		t.Error("the host script must only load when the daemon is running")
	}
}

// The new-tab launcher must stay standalone.
func TestVaultPageDoesNotRequestEmbeddedPresentation(t *testing.T) {
	if strings.Contains(readRepoFile(t, "plugin/pages/Vault.page"), "embed=1") {
		t.Error("Vault.page must not request embedded presentation")
	}
}

func TestAppPhpInjectsPresentationFromTheQuery(t *testing.T) {
	app := readRepoFile(t, "plugin/pages/include/app.php")
	if !regexp.MustCompile(`vault_embed_runtime_config\(\$_GET\)`).MatchString(app) {
		t.Error("app.php must build the presentation hints from the query")
	}
}

func TestDeployUploadsEmbedHost(t *testing.T) {
	if !strings.Contains(readRepoFile(t, "ansible/roles/deploy/tasks/main.yml"), "plugin/pages/include/embed-host.js") {
		t.Error("the deploy role must upload embed-host.js")
	}
}

// runPHP calls a function from the real api.php with a JSON-decoded argument list.
func runPHP(t *testing.T, call string) string {
	t.Helper()
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php not installed")
	}
	api, err := filepath.Abs(filepath.Join("..", "plugin", "pages", "include", "api.php"))
	if err != nil {
		t.Fatal(err)
	}
	code := fmt.Sprintf("require %q; echo json_encode(%s);", api, call)
	out, err := exec.Command(php, "-d", "display_errors=stderr", "-r", code).Output()
	if err != nil {
		t.Fatalf("php: %v", err)
	}
	return string(out)
}

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dynamix.cfg")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectHostTheme(t *testing.T) {
	cases := []struct{ name, cfg, want string }{
		{"black", "[display]\ntheme=\"black\"\n", `"dark"`},
		{"gray", "[display]\ntheme=\"gray\"\n", `"dark"`},
		{"white", "[display]\ntheme=\"white\"\n", `"light"`},
		{"azure", "[display]\ntheme=\"azure\"\n", `"light"`},
		{"custom theme", "[display]\ntheme=\"neon\"\n", "null"},
		{"no theme key", "[display]\ndate=\"%c\"\n", "null"},
		{"theme in another section", "[notify]\ntheme=\"black\"\n", "null"},
		{"empty file", "", "null"},
		{"malformed file", "[display\ntheme=(\n", "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runPHP(t, fmt.Sprintf("vault_detect_host_theme(%q)", writeCfg(t, tc.cfg)))
			if got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}

	t.Run("missing file", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "none.cfg")
		if got := runPHP(t, fmt.Sprintf("vault_detect_host_theme(%q)", missing)); got != "null" {
			t.Errorf("got %s, want null", got)
		}
	})
}

func TestEmbedRuntimeConfig(t *testing.T) {
	cfg := writeCfg(t, "[display]\ntheme=\"black\"\n")
	cases := []struct {
		name, query, presentation string
		hostTheme                 any
	}{
		{"framed", `['embed' => '1']`, "embedded", "dark"},
		{"new tab", `[]`, "standalone", nil},
		{"another value", `['embed' => '0']`, "standalone", nil},
		{"an array", `['embed' => ['1']]`, "standalone", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got struct {
				Presentation string `json:"presentation"`
				HostTheme    any    `json:"hostTheme"`
			}
			out := runPHP(t, fmt.Sprintf("vault_embed_runtime_config(%s, %q)", tc.query, cfg))
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if got.Presentation != tc.presentation || got.HostTheme != tc.hostTheme {
				t.Errorf("got %+v, want %s/%v", got, tc.presentation, tc.hostTheme)
			}
		})
	}
}

// The proxy destination comes from vault.cfg, so anything but a local daemon
// falls back to the default instead of being requested (#567).
func TestProxyTargetStaysOnLocalDaemon(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "vault.cfg")
	target := func(content string) string {
		t.Helper()
		if err := os.WriteFile(cfg, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return runPHP(t, fmt.Sprintf("(function() { $GLOBALS['VAULT_CFG'] = %q; return vault_target_url('/x'); })()", cfg))
	}
	for content, want := range map[string]string{
		"PORT=\"24085\"\nBIND_ADDRESS=\"127.0.0.1\"\n": "http://127.0.0.1:24085/x",
		"PORT=\"8080\"\nBIND_ADDRESS=\"0.0.0.0\"\n":    "http://127.0.0.1:8080/x",
		"PORT=\"8080\"\nBIND_ADDRESS=\"::1\"\n":        "http://[::1]:8080/x",
		"BIND_ADDRESS=\"169.254.169.254\"\n":           "http://127.0.0.1:24085/x",
		"BIND_ADDRESS=\"evil.example\"\n":              "http://127.0.0.1:24085/x",
		"BIND_ADDRESS=\"127.0.0.1/admin?x=\"\n":        "http://127.0.0.1:24085/x",
		"PORT=\"80/x\"\n":                              "http://127.0.0.1:24085/x",
		"PORT=\"80\"\n":                                "http://127.0.0.1:24085/x",
		"PORT=\"70000\"\n":                             "http://127.0.0.1:24085/x",
		"PORT=\"8080@evil.example\"\n":                 "http://127.0.0.1:24085/x",
	} {
		var got string
		if err := json.Unmarshal([]byte(target(content)), &got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%q -> %s, want %s", content, got, want)
		}
	}
}

// A LAN-IP bind must keep working: the proxy has to reach the daemon there.
func TestProxyAcceptsAddressesAssignedToThisHost(t *testing.T) {
	for call, want := range map[string]string{
		`vault_is_allowed_bind_address('192.168.1.20', ['10.0.0.1', '192.168.1.20'])`: "true",
		`vault_is_allowed_bind_address('2001:db8:0::1', ['2001:db8::1'])`:             "true",
		`vault_is_allowed_bind_address('[2001:db8::1]', ['2001:db8::1'])`:             "true",
		`vault_is_allowed_bind_address('192.168.1.21', ['192.168.1.20'])`:             "false",
		`vault_is_allowed_bind_address('nas.local', ['192.168.1.20'])`:                "false",
		`vault_is_allowed_bind_address('127.0.0.1', [])`:                              "true",
		`vault_is_allowed_bind_address('::', [])`:                                     "true",
	} {
		if got := strings.TrimSpace(runPHP(t, call)); got != want {
			t.Errorf("%s = %s, want %s", call, got, want)
		}
	}
	// The real interface list works in this PHP and yields a usable bind.
	got := strings.TrimSpace(runPHP(t, `(function() { $a = vault_interface_addresses(); return $a === [] || vault_is_allowed_bind_address($a[0]); })()`))
	if got != "true" {
		t.Errorf("vault_interface_addresses round trip = %s", got)
	}
}
