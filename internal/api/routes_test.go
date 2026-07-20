package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestDetectUnraidTimeFormat(t *testing.T) {
	t.Parallel()

	// On non-Unraid systems the config file does not exist, so expect "auto".
	got := detectUnraidTimeFormat()
	if got != "auto" {
		t.Errorf("detectUnraidTimeFormat() on dev machine = %q, want %q", got, "auto")
	}
}

func TestParseTimeFormatINI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "24h uppercase H",
			content: "[display]\ntime=\"H:i\"\n",
			want:    "24h",
		},
		{
			name:    "24h uppercase G",
			content: "[display]\ntime=\"G:i\"\n",
			want:    "24h",
		},
		{
			name:    "12h lowercase h",
			content: "[display]\ntime=\"h:i A\"\n",
			want:    "12h",
		},
		{
			name:    "12h lowercase g",
			content: "[display]\ntime=\"g:i A\"\n",
			want:    "12h",
		},
		{
			name:    "no display section",
			content: "[other]\nfoo=\"bar\"\n",
			want:    "auto",
		},
		{
			name:    "display section without time key",
			content: "[display]\ndate=\"Y-m-d\"\n",
			want:    "auto",
		},
		{
			name:    "empty content",
			content: "",
			want:    "auto",
		},
		{
			name:    "24h with surrounding sections",
			content: "[other]\nfoo=1\n[display]\ndate=\"Y-m-d\"\ntime=\"H:i:s\"\n[more]\nbar=2\n",
			want:    "24h",
		},
		{
			name:    "time key outside display section ignored",
			content: "[other]\ntime=\"H:i\"\n[display]\ndate=\"Y-m-d\"\n",
			want:    "auto",
		},
		{
			name:    "notify section fallback 12h",
			content: "[display]\ndate=\"%c\"\n[notify]\ntime=\"h:i A\"\n",
			want:    "12h",
		},
		{
			name:    "notify section fallback 24h",
			content: "[display]\ndate=\"%c\"\n[notify]\ntime=\"H:i\"\n",
			want:    "24h",
		},
		{
			name:    "display time takes priority over notify",
			content: "[display]\ntime=\"H:i\"\n[notify]\ntime=\"h:i A\"\n",
			want:    "24h",
		},
		{
			name:    "real Unraid 7.x config",
			content: "[display]\nwarning=\"70\"\ncritical=\"90\"\ndate=\"%c\"\n[notify]\ndate=\"d-m-Y\"\ntime=\"h:i A\"\n",
			want:    "12h",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseTimeFormatINI(tt.content)
			if got != tt.want {
				t.Errorf("parseTimeFormatINI() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectTimeFormatFromPath(t *testing.T) {
	t.Parallel()

	t.Run("file exists with 24h format", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "dynamix.cfg")
		os.WriteFile(path, []byte("[display]\ntime=\"H:i\"\n"), 0o600)
		got := detectTimeFormatFromPath(path)
		if got != "24h" {
			t.Errorf("detectTimeFormatFromPath() = %q, want %q", got, "24h")
		}
	})

	t.Run("file does not exist", func(t *testing.T) {
		t.Parallel()
		got := detectTimeFormatFromPath("/nonexistent/path/dynamix.cfg")
		if got != "auto" {
			t.Errorf("detectTimeFormatFromPath() = %q, want %q", got, "auto")
		}
	})
}

func TestBuildInjectedIndex(t *testing.T) {
	t.Parallel()

	htmlContent := `<!doctype html><html><head><title>Test</title></head><body></body></html>`
	distFS := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(htmlContent)},
	}

	result := buildInjectedIndex(distFS)
	html := string(result)

	if !strings.Contains(html, "window.__VAULT_RUNTIME_CONFIG__=") {
		t.Error("injected index should contain runtime config script")
	}
	if !strings.Contains(html, `"mode":"direct"`) {
		t.Error("injected config should contain mode:direct")
	}
	if !strings.Contains(html, `"timeFormat":`) {
		t.Error("injected config should contain timeFormat key")
	}
	if !strings.Contains(html, "</head>") {
		t.Error("injected index should preserve </head> tag")
	}
}

func TestBuildInjectedIndexMissingFile(t *testing.T) {
	t.Parallel()

	distFS := fstest.MapFS{}
	result := buildInjectedIndex(distFS)
	if result != nil {
		t.Errorf("expected nil for missing index.html, got %d bytes", len(result))
	}
}

func TestSPACatchAllServesInjectedHTML(t *testing.T) {
	database := testDB(t)
	srv := NewServer(database, ServerConfig{Addr: ":0"})

	req := httptest.NewRequest(http.MethodGet, "/some/spa/route", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "window.__VAULT_RUNTIME_CONFIG__=") {
		t.Error("SPA catch-all should return HTML with injected runtime config")
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

func TestKeyByRemoteAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		remoteAddr string
		want       string
	}{
		{name: "ipv4 with port strips port", remoteAddr: "1.2.3.4:5678", want: "1.2.3.4"},
		{name: "bare ipv4 falls back", remoteAddr: "1.2.3.4", want: "1.2.3.4"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.RemoteAddr = tc.remoteAddr
			got, err := keyByRemoteAddr(r)
			if err != nil {
				t.Fatalf("keyByRemoteAddr() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("keyByRemoteAddr(%q) = %q, want %q", tc.remoteAddr, got, tc.want)
			}
		})
	}

	// The rate-limit invariant: the same client on different source ports must
	// land in one bucket (LimitByIP's whole point).
	a, _ := keyByRemoteAddr(reqWithAddr("9.9.9.9:1000"))
	b, _ := keyByRemoteAddr(reqWithAddr("9.9.9.9:2000"))
	if a != b {
		t.Errorf("same IP different ports keyed differently: %q vs %q", a, b)
	}
}

func reqWithAddr(addr string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = addr
	return r
}

// TestSPACacheHeaders is the regression test for the QA finding that neither
// index.html nor the hashed assets carried any cache directives, so browsers
// applied heuristic caching and kept serving the PREVIOUS bundle after a
// plugin upgrade — users ran stale UI against a new API until a hard refresh.
func TestSPACacheHeaders(t *testing.T) {
	database := testDB(t)
	srv := NewServer(database, ServerConfig{Addr: ":0"})

	t.Run("index.html must not be cached", func(t *testing.T) {
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		cc := w.Header().Get("Cache-Control")
		if !strings.Contains(cc, "no-cache") && !strings.Contains(cc, "no-store") {
			t.Errorf("Cache-Control = %q, want a no-cache/no-store directive", cc)
		}
	})

	t.Run("SPA deep link must not be cached", func(t *testing.T) {
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/some/spa/route", nil))
		cc := w.Header().Get("Cache-Control")
		if !strings.Contains(cc, "no-cache") && !strings.Contains(cc, "no-store") {
			t.Errorf("Cache-Control = %q, want a no-cache/no-store directive", cc)
		}
	})
}
