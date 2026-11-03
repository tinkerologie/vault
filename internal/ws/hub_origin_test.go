package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// Issue #565: a private-network or .local address says nothing about who runs
// the page, so only same-origin and exactly listed origins may connect.
func TestHandleWSOriginPolicy(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	hub.SetAllowedOrigins([]string{"https://ha.example:8123", "http://[fd00::1]:8080"})
	srv := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer srv.Close()
	self := srv.URL
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	cases := []struct {
		origin string
		ok     bool
	}{
		{"", true}, // the CLI and other non-browser clients
		{self, true},
		{"https://ha.example:8123", true},
		{"http://[fd00::1]:8080", true},
		{"http://ha.example:8123", false},
		{"http://localhost:3000", false},
		{"http://127.0.0.1:3000", false},
		{"http://nas.local:8080", false},
		{"http://192.168.1.50:8080", false},
		{"http://10.0.0.5", false},
		{"http://172.17.0.2:80", false},
		{"https://attacker.example", false},
	}
	for _, tc := range cases {
		t.Run("origin="+tc.origin, func(t *testing.T) {
			header := http.Header{}
			if tc.origin != "" {
				header.Set("Origin", tc.origin)
			}
			conn, resp, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{HTTPHeader: header})
			if conn != nil {
				defer conn.CloseNow()
			}
			if tc.ok && err != nil {
				t.Fatalf("dial: %v", err)
			}
			if !tc.ok && (err == nil || resp == nil || resp.StatusCode != http.StatusForbidden) {
				t.Fatalf("expected 403, got resp=%v err=%v", resp, err)
			}
		})
	}
}
