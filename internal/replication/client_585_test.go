package replication

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A Vault peer's short JSON error reaches the user, so a missing API key says
// how to fix it (#578). Nothing else in the body is returned (#583).
func TestPeerStatusErrorMessage585(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", 300)
	for _, tc := range []struct {
		name, ctype, body, want, notWant string
	}{
		{"vault error", "application/json", `{"error":"no API key configured: generate one in the Unraid web UI"}`,
			"status 401: no API key configured: generate one in the Unraid web UI", ""},
		{"charset param", "application/json; charset=utf-8", `{"error":"bad key"}`, "status 401: bad key", ""},
		{"control characters", "application/json", "{\"error\":\"line1\\nline2\\u001b[31m red\\u202e \\t end\"}",
			"status 401: line1 line2[31m red end", "\x1b"},
		{"other fields dropped", "application/json", `{"error":"denied","secret":"s3cr3t"}`, "status 401: denied", "s3cr3t"},
		{"no error field", "application/json", `{"message":"s3cr3t"}`, "status 401", "s3cr3t"},
		{"not JSON", "text/html", `<h1>s3cr3t</h1>`, "status 401", "s3cr3t"},
		{"JSON type, bad body", "application/json", `s3cr3t`, "status 401", "s3cr3t"},
		{"non-string error", "application/json", `{"error":{"s":"s3cr3t"}}`, "status 401", "s3cr3t"},
		{"blank error", "application/json", `{"error":" \n "}`, "status 401", ""},
		{"too large", "application/json", `{"error":"x","pad":"` + strings.Repeat("a", maxPeerErrorBody) + `"}`, "status 401", "aaaa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.ctype)
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			c, err := NewClient(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.TestConnection()
			if err == nil || !strings.HasSuffix(err.Error(), tc.want) {
				t.Fatalf("err = %v, want suffix %q", err, tc.want)
			}
			if tc.notWant != "" && strings.Contains(err.Error(), tc.notWant) {
				t.Errorf("err %q contains %q", err, tc.notWant)
			}
		})
	}

	got := sanitizePeerMessage(long)
	if utf8.RuneCountInString(got) != maxPeerErrorRunes+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("long message not truncated: %d runes", utf8.RuneCountInString(got))
	}
}

func TestDownloadFileStatusMessage585(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"file not found"}`)
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL)
	if _, err := c.DownloadFile(1, "a/b"); err == nil || !strings.Contains(err.Error(), "status 404: file not found") {
		t.Fatalf("err = %v", err)
	}
}

// A download that keeps receiving data is not cut off by the client's total
// timeout; one that stalls is abandoned after the idle timeout.
func TestDownloadFileIdleTimeout585(t *testing.T) {
	prev := downloadIdleTimeout
	downloadIdleTimeout = 150 * time.Millisecond
	t.Cleanup(func() { downloadIdleTimeout = prev })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		switch r.URL.Query().Get("path") {
		case "slow":
			// 8 chunks 50ms apart: longer than the client timeout set below,
			// but never idle for long.
			for range 8 {
				_, _ = io.WriteString(w, "x")
				fl.Flush()
				time.Sleep(50 * time.Millisecond)
			}
		case "stall":
			_, _ = io.WriteString(w, "x")
			fl.Flush()
			<-r.Context().Done()
		case "no-headers":
			<-r.Context().Done()
		}
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.SetTimeout(200 * time.Millisecond)

	rc, err := c.DownloadFile(1, "slow")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(got) != "xxxxxxxx" {
		t.Fatalf("slow download = %q, %v; want it to finish", got, err)
	}

	rc, err = c.DownloadFile(1, "stall")
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(rc)
	_ = rc.Close()
	if err == nil || !strings.Contains(err.Error(), "peer sent no data") {
		t.Fatalf("stalled download err = %v, want idle timeout", err)
	}

	if _, err := c.DownloadFile(1, "no-headers"); err == nil || !strings.Contains(err.Error(), "peer sent no data") {
		t.Fatalf("no-response err = %v, want idle timeout", err)
	}

	// Other requests keep the total timeout.
	if _, err := c.ListStorageFiles(1, "no-headers"); err == nil {
		t.Fatal("listing should still time out")
	}
}
