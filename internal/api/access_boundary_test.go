package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
)

const (
	remotePeer   = "192.0.2.50:40000"
	loopbackPeer = "127.0.0.1:40000"
	daemonHost   = "127.0.0.1:24085"
	lanHost      = "192.0.2.10:24085"
	attacker     = "https://attacker.example"
)

func boundaryRequest(method, path, peer, host string, headers map[string]string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = peer
	req.Host = host
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

// asLocal makes a request look like the plugin proxy's: from the daemon's own
// host, addressed by IP. httptest's default peer and Host are remote.
func asLocal(req *http.Request) *http.Request {
	req.RemoteAddr, req.Host = loopbackPeer, daemonHost
	return req
}

func serve(srv *Server, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	return w
}

// setTestAPIKey stores a key the way GenerateAPIKey does and returns it.
func setTestAPIKey(t *testing.T, database *db.DB) string {
	t.Helper()
	key := "vault_" + "boundary_" + "fixture"
	hash, err := crypto.HashPassphrase(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetSetting("api_key_hash", hash); err != nil {
		t.Fatal(err)
	}
	return key
}

// Issue #538: a remote caller is refused when no key has been configured.
func TestRemoteDeniedWithNoKeyConfigured(t *testing.T) {
	srv := NewServer(testDB(t), ServerConfig{Addr: ":0"})
	for _, path := range []string{"/api/v1/jobs", "/api/v1/settings", "/api/v1/mcp", "/api/v1/ws", "/api/v1/no-such-route"} {
		if w := serve(srv, boundaryRequest(http.MethodGet, path, remotePeer, lanHost, nil)); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s from a remote peer = %d, want 401", path, w.Code)
		}
	}
}

// The Unraid PHP proxy and the CLI send no Origin and reach the daemon by IP.
func TestLocalNonBrowserClientsStayExempt(t *testing.T) {
	database := testDB(t)
	setTestAPIKey(t, database)
	srv := NewServer(database, ServerConfig{Addr: ":0"})
	for _, tc := range []struct{ peer, host string }{
		{loopbackPeer, daemonHost},
		{"[::1]:40000", "[::1]:24085"},
		{loopbackPeer, "localhost:24085"},
	} {
		if w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/jobs", tc.peer, tc.host, nil)); w.Code != http.StatusOK {
			t.Errorf("GET /jobs peer=%s host=%s = %d, want 200", tc.peer, tc.host, w.Code)
		}
	}
}

// A browser on the daemon's own origin keeps working over loopback, for the
// fetch metadata a real same-origin request carries on each method.
func TestLocalSameOriginBrowserStaysExempt(t *testing.T) {
	srv := NewServer(testDB(t), ServerConfig{Addr: ":0"})
	get := boundaryRequest(http.MethodGet, "/api/v1/jobs", loopbackPeer, "localhost:24085",
		map[string]string{"Sec-Fetch-Site": "same-origin"})
	if w := serve(srv, get); w.Code != http.StatusOK {
		t.Errorf("same-origin GET = %d, want 200", w.Code)
	}
	put := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`{"log_level":"info"}`))
	put.RemoteAddr, put.Host = loopbackPeer, "localhost:24085"
	put.Header.Set("Origin", "http://localhost:24085")
	put.Header.Set("Sec-Fetch-Site", "same-origin")
	put.Header.Set("Content-Type", "application/json")
	if w := serve(srv, put); w.Code != http.StatusOK {
		t.Errorf("same-origin PUT = %d, want 200: %s", w.Code, w.Body)
	}
}

// Issues #544 and #552: a page on another origin gets nothing from the
// loopback exemption, on any method, with or without an Origin header.
func TestCrossOriginLoopbackRequestsRefused(t *testing.T) {
	srv := NewServer(testDB(t), ServerConfig{Addr: ":0"})
	cases := []struct {
		name         string
		method, path string
		headers      map[string]string
	}{
		{"generate key", http.MethodPost, "/api/v1/settings/api-key/generate", map[string]string{"Origin": attacker}},
		{"rotate key", http.MethodPost, "/api/v1/settings/api-key/rotate", map[string]string{"Origin": attacker}},
		{"revoke key", http.MethodDelete, "/api/v1/settings/api-key", map[string]string{"Origin": attacker}},
		{"run job", http.MethodPost, "/api/v1/jobs/1/run", map[string]string{"Origin": attacker}},
		{"sync replication", http.MethodPost, "/api/v1/replication/1/sync", map[string]string{"Origin": attacker}},
		{"path remap", http.MethodPost, "/api/v1/recovery/path-remap", map[string]string{"Origin": attacker}},
		{"sandboxed frame", http.MethodPost, "/api/v1/jobs/1/run", map[string]string{"Origin": "null"}},
		{"unparseable origin", http.MethodPost, "/api/v1/jobs/1/run", map[string]string{"Origin": "http://%zz"}},
		{"other local port", http.MethodPost, "/api/v1/jobs/1/run", map[string]string{"Origin": "http://localhost:3000"}},
		{"formerly trusted myunraid", http.MethodGet, "/api/v1/settings/api-key/key", map[string]string{"Origin": "https://evil.myunraid.net"}},
		{"no-cors read", http.MethodGet, "/api/v1/settings/server-key", map[string]string{"Sec-Fetch-Site": "cross-site"}},
		{"same-site read", http.MethodGet, "/api/v1/jobs", map[string]string{"Sec-Fetch-Site": "same-site"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := serve(srv, boundaryRequest(tc.method, tc.path, loopbackPeer, daemonHost, tc.headers))
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s = %d, want 403", tc.method, tc.path, w.Code)
			}
		})
	}
}

// An origin is scheme, host and port. Fetch metadata isn't sent to plain HTTP
// hosts, so the scheme has to be compared directly: an http page on the daemon's
// name must not pass as the daemon's https origin, or the reverse. Non-browser
// clients such as the plugin proxy send no Origin and never reach this.
func TestCrossOriginComparesScheme(t *testing.T) {
	serverTLS := &tls.ConnectionState{}
	for _, tc := range []struct {
		name   string
		origin string
		tls    *tls.ConnectionState
		want   bool
	}{
		{"http page, http daemon", "http://" + daemonHost, nil, false},
		{"https page, https daemon", "https://" + daemonHost, serverTLS, false},
		{"http page, https daemon", "http://" + daemonHost, serverTLS, true},
		{"https page, http daemon", "https://" + daemonHost, nil, true},
		{"scheme case is ignored", "HTTPS://" + daemonHost, serverTLS, false},
		{"other scheme", "ftp://" + daemonHost, nil, true},
		{"no origin, proxy or CLI", "", nil, false},
		{"no origin over TLS", "", serverTLS, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/1/run", nil)
			req.Host, req.TLS = daemonHost, tc.tls
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if got := isCrossOrigin(req); got != tc.want {
				t.Errorf("isCrossOrigin = %v, want %v", got, tc.want)
			}
		})
	}

	srv := NewServer(testDB(t), ServerConfig{Addr: ":0"})
	w := serve(srv, boundaryRequest(http.MethodPost, "/api/v1/settings/api-key/generate", loopbackPeer, daemonHost,
		map[string]string{"Origin": "https://" + daemonHost}))
	if w.Code != http.StatusForbidden {
		t.Errorf("https page against http daemon = %d, want 403", w.Code)
	}
}

// DNS rebinding makes the browser treat the daemon as same-origin, so Origin
// matches Host and fetch metadata is absent. The Host name itself gives it away.
func TestDNSRebindingDoesNotEarnLocalTrust(t *testing.T) {
	srv := NewServer(testDB(t), ServerConfig{Addr: ":0"})
	const host = "rebind.attacker.example:24085"
	for _, tc := range []struct {
		method, path string
		headers      map[string]string
	}{
		{http.MethodGet, "/api/v1/settings/api-key/key", nil},
		{http.MethodPost, "/api/v1/settings/api-key/generate", map[string]string{"Origin": "http://" + host}},
	} {
		if w := serve(srv, boundaryRequest(tc.method, tc.path, loopbackPeer, host, tc.headers)); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}

func TestCORSAllowlist(t *testing.T) {
	const trusted = "https://ha.example:8123"
	preflight := func(srv *Server, origin string) *httptest.ResponseRecorder {
		return serve(srv, boundaryRequest(http.MethodOptions, "/api/v1/jobs", loopbackPeer, daemonHost, map[string]string{
			"Origin":                                 origin,
			"Access-Control-Request-Method":          "POST",
			"Access-Control-Request-Private-Network": "true",
		}))
	}

	t.Run("empty allowlist grants nothing", func(t *testing.T) {
		srv := NewServer(testDB(t), ServerConfig{Addr: ":0"})
		for _, origin := range []string{attacker, "https://tower.myunraid.net", "http://localhost:3000", "http://127.0.0.1:9"} {
			h := preflight(srv, origin).Header()
			if got := h.Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("origin %s: Allow-Origin = %q, want none", origin, got)
			}
			if got := h.Get("Access-Control-Allow-Private-Network"); got != "" {
				t.Errorf("origin %s: Allow-Private-Network = %q, want none", origin, got)
			}
			if got := h.Get("Access-Control-Allow-Credentials"); got != "" {
				t.Errorf("origin %s: Allow-Credentials = %q, want none", origin, got)
			}
		}
	})

	database := testDB(t)
	key := setTestAPIKey(t, database)
	srv := NewServer(database, ServerConfig{Addr: ":0", AllowedOrigins: []string{trusted, "https://*.bad.example", "not an origin"}})

	t.Run("listed origin gets exact grant", func(t *testing.T) {
		h := preflight(srv, trusted).Header()
		if got := h.Get("Access-Control-Allow-Origin"); got != trusted {
			t.Errorf("Allow-Origin = %q, want %q", got, trusted)
		}
		if got := h.Get("Access-Control-Allow-Private-Network"); got != "true" {
			t.Errorf("Allow-Private-Network = %q, want true", got)
		}
	})
	t.Run("unlisted and wildcard origins get nothing", func(t *testing.T) {
		for _, origin := range []string{attacker, "https://x.bad.example", "http://ha.example:8123"} {
			h := preflight(srv, origin).Header()
			if h.Get("Access-Control-Allow-Origin") != "" || h.Get("Access-Control-Allow-Private-Network") != "" {
				t.Errorf("origin %s was granted: %v", origin, h)
			}
		}
	})
	t.Run("listed origin still needs the key on loopback", func(t *testing.T) {
		o := map[string]string{"Origin": trusted}
		if w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/jobs", loopbackPeer, daemonHost, o)); w.Code != http.StatusUnauthorized {
			t.Errorf("no key = %d, want 401", w.Code)
		}
		o["X-API-Key"] = key
		w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/jobs", loopbackPeer, daemonHost, o))
		if w.Code != http.StatusOK {
			t.Errorf("with key = %d, want 200", w.Code)
		}
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != trusted {
			t.Errorf("Allow-Origin = %q, want %q", got, trusted)
		}
	})
}

func TestParseAllowedOrigins(t *testing.T) {
	valid, invalid := parseAllowedOrigins([]string{
		"https://Ha.Example:8123", " http://192.0.2.1 ", "", "null", "*", "https://*.example",
		"https://a.example/path", "ftp://a.example", "https://user@a.example", "a.example", "https://a.example?x=1",
	})
	want := []string{"https://ha.example:8123", "http://192.0.2.1"}
	if len(valid) != len(want) || valid[0] != want[0] || valid[1] != want[1] {
		t.Errorf("valid = %v, want %v", valid, want)
	}
	if len(invalid) != 8 {
		t.Errorf("invalid = %v, want 8 entries", invalid)
	}
}

// A rejected entry can be a URL with a login in it, so its value is never logged.
func TestInvalidOriginWarningOmitsValue(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })

	NewServer(testDB(t), ServerConfig{Addr: ":0", AllowedOrigins: []string{"https://user:hunter2@a.example"}})

	got := buf.String()
	if strings.Contains(got, "hunter2") {
		t.Errorf("warning leaked the entry: %s", got)
	}
	if !strings.Contains(got, "ignoring 1 allowed origin") {
		t.Errorf("no warning about the rejected entry: %s", got)
	}
}

// Secret-reveal and key-management routes answer only the daemon's own host,
// so a stolen key can't be used to read or replace the rest.
func TestLocalOnlyRoutes(t *testing.T) {
	database := testDB(t)
	key := setTestAPIKey(t, database)
	srv := NewServer(database, ServerConfig{Addr: ":0", ServerKey: make([]byte, crypto.ServerKeySize)})
	for route := range localOnlyRoutes {
		method, path, _ := strings.Cut(route, " ")
		path = "/api/v1" + path
		w := serve(srv, boundaryRequest(method, path, remotePeer, lanHost, map[string]string{"X-API-Key": key}))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s with a valid key from a remote peer = %d, want 403", route, w.Code)
		}
	}
	w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/settings/server-key", loopbackPeer, daemonHost, nil))
	if w.Code != http.StatusOK {
		t.Errorf("local server-key download = %d, want 200", w.Code)
	}
}

// Headless replicas have no other way to get a key, so key management stays
// open to the local host even though replicas are otherwise read-only.
func TestReplicaKeyProvisioningIsLocalOnly(t *testing.T) {
	database := testDB(t)
	srv := NewServer(database, ServerConfig{Addr: ":0", ReadOnly: true, ServerKey: make([]byte, crypto.ServerKeySize)})
	gen := "/api/v1/settings/api-key/generate"
	if w := serve(srv, boundaryRequest(http.MethodPost, gen, remotePeer, lanHost, nil)); w.Code != http.StatusUnauthorized {
		t.Errorf("remote generate = %d, want 401", w.Code)
	}
	w := serve(srv, boundaryRequest(http.MethodPost, gen, loopbackPeer, daemonHost, nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("local generate on replica = %d, want 201: %s", w.Code, w.Body)
	}
	var body struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.APIKey == "" {
		t.Fatalf("no key in response: %v", err)
	}
	if w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/jobs", remotePeer, lanHost, map[string]string{"X-API-Key": body.APIKey})); w.Code != http.StatusOK {
		t.Errorf("remote read with the new key = %d, want 200", w.Code)
	}
	if w := serve(srv, boundaryRequest(http.MethodPut, "/api/v1/settings", loopbackPeer, daemonHost, nil)); w.Code != http.StatusForbidden {
		t.Errorf("other settings writes on a replica = %d, want 403", w.Code)
	}
}

// The Settings UI flow: every browser path that reaches the daemon as the host
// can run the whole key lifecycle, first-time bootstrap included. The plugin
// proxy (and so its embedded frame) arrives as PHP cURL with no browser headers;
// an SSH tunnel arrives as a same-origin page on whatever local port was chosen.
func TestSettingsUIKeyLifecycle(t *testing.T) {
	// A real address of this machine, since NewServer reads its own interfaces.
	// Falls back to loopback on a host with no other address, as a CI sandbox may.
	nicIP := "127.0.0.1"
	for ip := range readLocalInterfaceIPs() {
		if parsed := net.ParseIP(ip); parsed.To4() != nil && !parsed.IsLoopback() {
			nicIP = ip
			break
		}
	}
	clients := map[string]struct {
		peer, host string
		headers    map[string]string
	}{
		"plugin proxy over loopback": {loopbackPeer, daemonHost, map[string]string{"X-Vault-Proxy": "unraid-plugin-proxy"}},
		// BIND_ADDRESS set to the LAN IP: cURL targets that IP, and Linux sources
		// the connection from it, so peer and Host are both the NIC address.
		"plugin proxy over NIC bind": {nicIP + ":40000", nicIP + ":24085", map[string]string{"X-Vault-Proxy": "unraid-plugin-proxy"}},
		// A tunnel to a LAN-IP bind: sshd connects from the NIC address, and the
		// browser still addresses the tunnel's local end.
		"same-origin through a tunnel to a NIC bind": {nicIP + ":40000", "127.0.0.1:8080", map[string]string{
			"Origin": "http://127.0.0.1:8080", "Sec-Fetch-Site": "same-origin",
		}},
		"same-origin through a tunnel": {loopbackPeer, "127.0.0.1:8080", map[string]string{
			"Origin": "http://127.0.0.1:8080", "Sec-Fetch-Site": "same-origin",
		}},
	}
	for name, c := range clients {
		t.Run(name, func(t *testing.T) {
			database := testDB(t)
			srv := NewServer(database, ServerConfig{Addr: ":0", ServerKey: make([]byte, crypto.ServerKeySize)})
			do := func(method, path string) *httptest.ResponseRecorder {
				return serve(srv, boundaryRequest(method, "/api/v1/settings/api-key"+path, c.peer, c.host, c.headers))
			}
			keyOf := func(w *httptest.ResponseRecorder) string {
				t.Helper()
				var body struct {
					APIKey string `json:"api_key"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.APIKey == "" {
					t.Fatalf("no key in %d response: %s", w.Code, w.Body)
				}
				return body.APIKey
			}

			first := do(http.MethodPost, "/generate")
			if first.Code != http.StatusCreated {
				t.Fatalf("bootstrap generate = %d: %s", first.Code, first.Body)
			}
			key := keyOf(first)
			reveal := do(http.MethodGet, "/key")
			if reveal.Code != http.StatusOK || keyOf(reveal) != key {
				t.Fatalf("reveal = %d, want the generated key", reveal.Code)
			}
			rotated := do(http.MethodPost, "/rotate")
			if rotated.Code != http.StatusCreated || keyOf(rotated) == key {
				t.Fatalf("rotate = %d, want a new key", rotated.Code)
			}
			if w := do(http.MethodDelete, ""); w.Code != http.StatusOK {
				t.Fatalf("revoke = %d", w.Code)
			}
			if w := do(http.MethodGet, ""); !strings.Contains(w.Body.String(), `"enabled":false`) {
				t.Errorf("status after revoke: %s", w.Body)
			}
		})
	}
}

// A remote caller can't bootstrap a key, and the refusal for a key holder says
// where to go instead of leaving the UI with a bare error.
func TestRemoteKeyManagementRefusals(t *testing.T) {
	database := testDB(t)
	srv := NewServer(database, ServerConfig{Addr: ":0", ServerKey: make([]byte, crypto.ServerKeySize)})
	gen := "/api/v1/settings/api-key/generate"
	if w := serve(srv, boundaryRequest(http.MethodPost, gen, remotePeer, lanHost, nil)); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous remote bootstrap = %d, want 401", w.Code)
	}
	if hash, _ := database.GetSetting("api_key_hash", ""); hash != "" {
		t.Error("anonymous remote caller created a key")
	}

	key := setTestAPIKey(t, database)
	w := serve(srv, boundaryRequest(http.MethodPost, gen, remotePeer, lanHost, map[string]string{"X-API-Key": key}))
	if w.Code != http.StatusForbidden {
		t.Fatalf("key holder generate = %d, want 403", w.Code)
	}
	for _, want := range []string{"Unraid web UI", "SSH tunnel", "CLI"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("refusal %s doesn't mention %q", w.Body, want)
		}
	}
}

// Issue #552: a cross-site page can't change the key through the loopback
// exemption, whether or not one exists. Checks the stored state, not just status.
func TestCrossSiteCannotChangeKey(t *testing.T) {
	for _, withKey := range []bool{false, true} {
		database := testDB(t)
		if withKey {
			setTestAPIKey(t, database)
		}
		before, _ := database.GetSetting("api_key_hash", "")
		srv := NewServer(database, ServerConfig{Addr: ":0", ServerKey: make([]byte, crypto.ServerKeySize)})
		for _, route := range []struct{ method, path string }{
			{http.MethodPost, "/api/v1/settings/api-key/generate"},
			{http.MethodPost, "/api/v1/settings/api-key/rotate"},
			{http.MethodDelete, "/api/v1/settings/api-key"},
		} {
			for _, headers := range []map[string]string{
				{"Origin": attacker},
				{"Origin": attacker, "Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": "cross-site"},
				{"Sec-Fetch-Site": "cross-site"},
			} {
				if w := serve(srv, boundaryRequest(route.method, route.path, loopbackPeer, daemonHost, headers)); w.Code != http.StatusForbidden {
					t.Errorf("%s %s %v = %d, want 403", route.method, route.path, headers, w.Code)
				}
			}
		}
		if after, _ := database.GetSetting("api_key_hash", ""); after != before {
			t.Errorf("withKey=%v: the stored key changed", withKey)
		}
	}
}

// Issue #570: /health is behind the boundary like everything else. ha-vault's
// config flow depends on this: it probes /health with no key and only asks for
// one on a 401, so a 200 here would create an entry with no key.
func TestHealthDisclosure(t *testing.T) {
	database := testDB(t)
	key := setTestAPIKey(t, database)
	srv := NewServer(database, ServerConfig{Addr: ":0", Version: "1.2.3"})
	srv.SetStartupDiagnostics(&StartupDiagnostics{
		HybridMode:      true,
		DetectedPool:    "/mnt/secretpool",
		RestorationInfo: &db.RestorationInfo{Source: "primary", Path: "/mnt/secretpool/.vault/vault.db"},
	})

	anon := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/health", remotePeer, lanHost, nil))
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous health = %d, want 401", anon.Code)
	}
	if strings.Contains(anon.Body.String(), "secretpool") {
		t.Errorf("anonymous health leaks a path: %s", anon.Body)
	}

	for name, req := range map[string]*http.Request{
		"local":    boundaryRequest(http.MethodGet, "/api/v1/health", loopbackPeer, daemonHost, nil),
		"key auth": boundaryRequest(http.MethodGet, "/api/v1/health", remotePeer, lanHost, map[string]string{"X-API-Key": key}),
	} {
		w := serve(srv, req)
		if !strings.Contains(w.Body.String(), `"hybrid_mode":true`) || !strings.Contains(w.Body.String(), `"version":"1.2.3"`) {
			t.Errorf("%s health missing detail: %s", name, w.Body)
		}
	}

	if w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/health", remotePeer, lanHost, map[string]string{"X-API-Key": "wrong"})); w.Code != http.StatusUnauthorized {
		t.Errorf("health with wrong key = %d, want 401", w.Code)
	}
}

// With no key configured, a remote caller is told where to create one.
func TestNoKeyRefusalNamesUnraidUI(t *testing.T) {
	srv := NewServer(testDB(t), ServerConfig{Addr: ":0"})
	w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/jobs", remotePeer, lanHost, nil))
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Unraid web UI") {
		t.Errorf("refusal = %d %s, want 401 naming the Unraid web UI", w.Code, w.Body)
	}
}

// The two clients that need a key, in the exact form their code sends it. Paths
// come from internal/replication/client.go and ha-vault's api/client.py, whose
// requests also carry Authorization and no Origin or Sec-Fetch headers.
func TestKeyClientsAreAccepted(t *testing.T) {
	database := testDB(t)
	key := setTestAPIKey(t, database)
	srv := NewServer(database, ServerConfig{Addr: ":0", Version: "1.2.3"})
	ha := map[string]string{"X-API-Key": key, "Authorization": "Bearer " + key}
	replication := map[string]string{"X-API-Key": key}
	for _, tc := range []struct {
		name    string
		path    string
		headers map[string]string
	}{
		{"ha health", "/api/v1/health", ha},
		{"ha summary", "/api/v1/health/summary", ha},
		{"ha jobs", "/api/v1/jobs", ha},
		{"ha settings", "/api/v1/settings", ha},
		{"replication health", "/api/v1/health", replication},
		{"replication jobs", "/api/v1/jobs", replication},
		{"replication source list", "/api/v1/replication", ha},
	} {
		if w := serve(srv, boundaryRequest(http.MethodGet, tc.path, remotePeer, lanHost, tc.headers)); w.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200: %s", tc.name, w.Code, w.Body)
		}
	}
	// There is no /replication/sources route: it reaches /replication/{id} and
	// fails ID parsing, which is past the boundary, not a refusal.
	if w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/replication/sources", remotePeer, lanHost, ha)); w.Code != http.StatusBadRequest {
		t.Errorf("/replication/sources = %d, want 400", w.Code)
	}
	// A steady poll from one address never reaches the attempt limiter.
	for range apiKeyAttemptsPerMinute * 2 {
		if w := serve(srv, boundaryRequest(http.MethodGet, "/api/v1/health", remotePeer, lanHost, ha)); w.Code != http.StatusOK {
			t.Fatalf("polling with the key = %d, want 200", w.Code)
		}
	}
}

// Real connection: Home Assistant's WebSocket sends the key as a header and no Origin.
func TestHomeAssistantWebSocketAccepted(t *testing.T) {
	database := testDB(t)
	key := setTestAPIKey(t, database)
	srv := NewServer(database, ServerConfig{Addr: ":0"})
	go srv.Hub().Run()
	t.Cleanup(srv.Hub().Stop)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr, r.Host = remotePeer, lanHost
		srv.router.ServeHTTP(w, r)
	}))
	defer ts.Close()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws"

	header := http.Header{"X-Api-Key": {key}}
	conn, _, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("dial with key: %v", err)
	}
	defer conn.CloseNow()

	if _, resp, err := websocket.Dial(context.Background(), url, nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("dial with no key: resp=%v err=%v, want 401", resp, err)
	}
}

// Issue #564: the expensive encryption mutation is rate limited too.
func TestSetEncryptionIsRateLimited(t *testing.T) {
	srv := NewServer(testDB(t), ServerConfig{Addr: ":0"})
	var last int
	for range setEncryptionRatePerMinute + 1 {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/encryption", strings.NewReader(`{`))
		req.RemoteAddr, req.Host = loopbackPeer, daemonHost
		last = serve(srv, req).Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("request past the limit = %d, want 429", last)
	}
}

// localOnlyRoutes names the routes wrapped in LocalOnly, as "METHOD path" under
// /api/v1. TestLocalOnlyRoutes checks each answers remote key holders 403.
var localOnlyRoutes = map[string]bool{
	"GET /settings/encryption/passphrase":  true,
	"GET /settings/server-key":             true,
	"GET /settings/api-key/key":            true,
	"POST /settings/api-key/generate":      true,
	"POST /settings/api-key/rotate":        true,
	"DELETE /settings/api-key":             true,
	"POST /settings/assistant-keys":        true,
	"DELETE /settings/assistant-keys/{id}": true,
}

// publicRoutes is every route reachable with no credential. Adding to this
// list is a security decision: it needs a reason in review.
var publicRoutes = map[string]bool{
	"GET /*": true, // embedded SPA assets, no data
}

var secretLookingRoute = regexp.MustCompile(`(?i)(passphrase|server-key|api-key|assistant-keys|secret|token|credential)`)

// TestRouteInventoryDeniesByDefault walks the real router so a new route is
// covered with no change here. Any route not in publicRoutes must refuse a
// remote caller and a cross-origin page, in daemon and replica mode, with and
// without a key configured. Routes that look like secrets must be declared
// local-only.
func TestRouteInventoryDeniesByDefault(t *testing.T) {
	param := regexp.MustCompile(`\{[^}]+\}`)
	for _, readOnly := range []bool{false, true} {
		for _, withKey := range []bool{false, true} {
			database := testDB(t)
			if withKey {
				setTestAPIKey(t, database)
			}
			srv := NewServer(database, ServerConfig{Addr: ":0", ReadOnly: readOnly})
			var assistantKey string
			if !readOnly {
				assistantKey = issueAssistantKey(t, srv, "Route inventory").Key
			}
			seen := 0
			err := chi.Walk(srv.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
				seen++
				id := method + " " + route
				if publicRoutes[id] {
					return nil
				}
				if !strings.HasPrefix(route, "/api/v1/") {
					t.Errorf("%s is outside /api/v1 and not declared public", id)
					return nil
				}
				path := strings.TrimSuffix(param.ReplaceAllString(route, "1"), "*")
				if w := serve(srv, boundaryRequest(method, path, remotePeer, lanHost, nil)); w.Code != http.StatusUnauthorized {
					t.Errorf("%s from a remote peer with no key = %d, want 401 (readOnly=%v withKey=%v)", id, w.Code, readOnly, withKey)
				}
				if w := serve(srv, boundaryRequest(method, path, loopbackPeer, daemonHost, map[string]string{"Origin": attacker})); w.Code != http.StatusForbidden {
					t.Errorf("%s from a cross-origin page = %d, want 403 (readOnly=%v withKey=%v)", id, w.Code, readOnly, withKey)
				}
				scopedAllowed := id == "GET /api/v1/recovery/catalog" ||
					id == "GET /api/v1/recovery/restore-points" || id == "GET /api/v1/recovery/restore-points/{rpid}/evidence" ||
					((route == "/api/v1/mcp" || route == "/api/v1/mcp/*") &&
						(method == http.MethodGet || method == http.MethodPost || method == http.MethodDelete))
				if assistantKey != "" && !scopedAllowed {
					for _, peer := range []string{remotePeer, loopbackPeer} {
						w := serve(srv, boundaryRequest(method, path, peer, daemonHost, map[string]string{"X-API-Key": assistantKey}))
						if w.Code != http.StatusForbidden {
							t.Errorf("%s with an Assistant Key from %s = %d, want 403", id, peer, w.Code)
						}
					}
				}
				local := method + " " + strings.TrimSuffix(strings.TrimPrefix(route, "/api/v1"), "/")
				if secretLookingRoute.MatchString(route) && !localOnlyRoutes[local] && !secretRouteNotLocalOnly[local] {
					t.Errorf("%s looks like it handles a secret: add it to localOnlyRoutes or secretRouteNotLocalOnly", id)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if seen < 100 {
				t.Errorf("walked only %d routes; the inventory is not seeing the router", seen)
			}
		}
	}
}

// secretRouteNotLocalOnly lists routes matching secretLookingRoute that don't
// return a secret.
var secretRouteNotLocalOnly = map[string]bool{
	"GET /settings/api-key":        true, // reports only whether a key is configured
	"GET /settings/assistant-keys": true, // lists only metadata
}
