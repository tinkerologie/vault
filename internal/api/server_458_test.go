package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/web"
)

func TestServerSettersAndHealthDiagnostics_458(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{Addr: ":0", ReadOnly: true, Version: "v-test"})

	if srv.MountManager() == nil {
		t.Error("MountManager() returned nil")
	}
	srv.SetAnomalyEvaluator(nil) // nil evaluator is ignored
	srv.SetPreShutdownHook(func() {})
	srv.SetStartupDiagnostics(&StartupDiagnostics{})

	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["mode"] != "replica" || resp["version"] != "v-test" {
		t.Errorf("health = %v", resp)
	}
	if _, ok := resp["startup"]; !ok {
		t.Errorf("startup diagnostics missing: %v", resp)
	}
}

func TestRespondJSONEncodeError_458(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	w := httptest.NewRecorder()
	respondJSON(w, http.StatusTeapot, make(chan int))
	if w.Code != http.StatusTeapot || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("code=%d ct=%q", w.Code, w.Header().Get("Content-Type"))
	}
	if !strings.Contains(buf.String(), "failed to write JSON response") {
		t.Errorf("encode failure not logged: %q", buf.String())
	}
}

func TestStartWithContextRunsPreShutdownHook_458(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{Addr: "127.0.0.1:0"})
	hookRan := make(chan struct{})
	srv.SetPreShutdownHook(func() { close(hookRan) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.StartWithContext(ctx) }()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("StartWithContext: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("StartWithContext did not return")
	}
	select {
	case <-hookRan:
	case <-time.After(10 * time.Second):
		t.Fatal("pre-shutdown hook never ran")
	}
}

func TestStartWithContextTLSBadCert_458(t *testing.T) {
	d := testDB(t)
	dir := t.TempDir()
	srv := NewServer(d, ServerConfig{Addr: "127.0.0.1:0", TLSCert: dir + "/missing.crt", TLSKey: dir + "/missing.key"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.StartWithContext(ctx); err == nil || errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("expected TLS load error, got %v", err)
	}
}

func TestRouteHooksUseLateBoundReloader_458(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{Addr: ":0"})

	do := func(method, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:5000"
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		return w.Code
	}

	// Without a reloader the hooks are no-ops.
	if c := do(http.MethodPut, "/api/v1/settings", `{"replication_enabled":"true"}`); c != http.StatusOK {
		t.Fatalf("settings PUT = %d", c)
	}
	if c := do(http.MethodPost, "/api/v1/jobs", `{"name":"a","schedule":""}`); c >= 300 {
		t.Fatalf("job create = %d", c)
	}
	if c := do(http.MethodDelete, "/api/v1/replication/999", ""); c >= 300 {
		t.Fatalf("replication delete = %d", c)
	}

	reloads := 0
	srv.SetScheduleReloader(func() error { reloads++; return nil })
	do(http.MethodPut, "/api/v1/settings", `{"replication_enabled":"false"}`)
	do(http.MethodPost, "/api/v1/jobs", `{"name":"b","schedule":""}`)
	do(http.MethodDelete, "/api/v1/replication/999", "")
	if reloads < 3 {
		t.Fatalf("reloads = %d, want >= 3", reloads)
	}

	// A resolver set before routes are (re)built is wired straight in.
	srv.nextRunResolver = func(int64) (string, bool) { return "", false }
	if srv.setupRoutes() == nil {
		t.Fatal("setupRoutes returned nil")
	}
}

func TestSPAStaticFileCacheHeaders_458(t *testing.T) {
	assets, _ := fs.Glob(web.Dist, "dist/assets/*")
	if len(assets) == 0 {
		t.Skip("web/dist has no built assets")
	}
	d := testDB(t)
	srv := NewServer(d, ServerConfig{Addr: ":0"})

	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+strings.TrimPrefix(assets[0], "dist/"), nil))
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("asset Cache-Control = %q", cc)
	}

	others, _ := fs.Glob(web.Dist, "dist/*.*")
	for _, p := range others {
		if strings.HasSuffix(p, "index.html") {
			continue
		}
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+strings.TrimPrefix(p, "dist/"), nil))
		if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s Cache-Control = %q, want no-cache", p, cc)
		}
		break
	}
}

func TestQuietRequestLoggerDefaults_458(t *testing.T) {
	// nil logger and non-positive threshold fall back to defaults; a handler
	// that never writes is treated as 200 and passes through untouched.
	h := newQuietRequestLogger(nil, 0)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestAPIKeyAuthFailsClosedOnDBError_458(t *testing.T) {
	d := testDB(t)
	cache := newLocalIPCache(time.Minute, func() map[string]struct{} { return map[string]struct{}{} })
	h := apiKeyAuth(d, cache)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	req.RemoteAddr = "192.0.2.10:4444"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestIsLocalRequestEdgeCases_458(t *testing.T) {
	cache := newLocalIPCache(time.Minute, func() map[string]struct{} {
		return map[string]struct{}{"192.0.2.5": {}}
	})
	// Address without a port still parses as a host.
	if !isLocalRequest("192.0.2.5", cache) {
		t.Error("portless local address should be local")
	}
	if isLocalRequest("not-an-ip", cache) {
		t.Error("unparseable host must not be local")
	}
	if cache.has(nil) {
		t.Error("nil IP must not be local")
	}
	if !cache.has(net.ParseIP("192.0.2.5")) {
		t.Error("cached address should match")
	}
	if readLocalInterfaceIPs() == nil {
		t.Error("readLocalInterfaceIPs must return a non-nil set")
	}
}

func TestDetectTimeFormatFromUnreadablePath_458(t *testing.T) {
	// A directory exists but cannot be read as a file: logged, "auto".
	if got := detectTimeFormatFromPath(t.TempDir()); got != "auto" {
		t.Fatalf("got %q, want auto", got)
	}
}
