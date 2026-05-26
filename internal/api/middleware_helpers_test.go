package api

import (
	"net/http"
	"testing"
)

// TestRequestPath_AllBranches exercises requestPath helper:
//   - nil URL -> "/"
//   - empty Path -> "/"
//   - normal path -> as-is
func TestRequestPath_AllBranches(t *testing.T) {
	t.Parallel()

	// Real request via http.NewRequest produces non-nil URL.
	r, _ := http.NewRequest(http.MethodGet, "/some/path", nil)
	if got := requestPath(r); got != "/some/path" {
		t.Errorf("normal path = %q, want %q", got, "/some/path")
	}

	// Request with nil URL.
	r2 := &http.Request{Method: http.MethodGet}
	if got := requestPath(r2); got != "/" {
		t.Errorf("nil URL = %q, want %q", got, "/")
	}

	// Request with empty path URL.
	r3, _ := http.NewRequest(http.MethodGet, "/", nil)
	r3.URL.Path = ""
	if got := requestPath(r3); got != "/" {
		t.Errorf("empty path = %q, want %q", got, "/")
	}
}

// TestRemoteAddrHost_AllBranches covers every return path of remoteAddrHost.
func TestRemoteAddrHost_AllBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		in    string
		wantP string
	}{
		{"ipv4 with port", "127.0.0.1:8080", "127.0.0.1"},
		{"ipv6 with port", "[::1]:8080", "::1"},
		{"bare ipv4", "192.168.1.1", "192.168.1.1"},
		{"empty", "", "-"},
		{"only brackets", "[]", "-"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := remoteAddrHost(tt.in)
			if got != tt.wantP {
				t.Errorf("remoteAddrHost(%q) = %q, want %q", tt.in, got, tt.wantP)
			}
		})
	}
}

// TestIsLoopback_AllBranches covers every return path of isLoopback.
func TestIsLoopback_AllBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{"loopback ipv4 with port", "127.0.0.1:8080", true},
		{"loopback ipv4 bare", "127.0.0.1", true},
		{"loopback ipv6 with port", "[::1]:8080", true},
		{"loopback ipv6 bare", "::1", true},
		{"loopback ipv4 range", "127.42.42.42", true},
		{"public ipv4", "8.8.8.8", false},
		{"private ipv4", "192.168.1.1", false},
		{"not an IP", "definitely-not-an-ip", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isLoopback(tt.addr)
			if got != tt.want {
				t.Errorf("isLoopback(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}
