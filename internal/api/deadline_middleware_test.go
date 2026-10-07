package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/api/handlers"
)

// deadlineRecorder is a ResponseWriter that records the deadlines an
// http.ResponseController sets on it.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	write time.Time
}

// SetWriteDeadline records t so tests can assert what reached the writer.
func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error { d.write = t; return nil }

// The chi wrapper picks a different concrete type depending on which optional
// interfaces the underlying writer implements; each must still unwrap.
type deadlineHijacker struct{ *deadlineRecorder }

// Hijack makes chi wrap this writer in its hijack-capable variant.
func (deadlineHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) { return nil, nil, nil }

type deadlinePusher struct{ *deadlineRecorder }

// Push makes chi wrap this writer in its HTTP/2 variant.
func (deadlinePusher) Push(string, *http.PushOptions) error { return nil }

// TestExtendWriteDeadlineReachesConnThroughRequestLogger checks the deadline
// unwraps through every chi wrapper variant QuietRequestLogger can install.
func TestExtendWriteDeadlineReachesConnThroughRequestLogger(t *testing.T) {
	const extend = 2 * time.Minute

	cases := []struct {
		name       string
		protoMajor int
		wrap       func(*deadlineRecorder) http.ResponseWriter
	}{
		{"flush", 1, func(d *deadlineRecorder) http.ResponseWriter { return d }},
		{"flush+hijack", 1, func(d *deadlineRecorder) http.ResponseWriter { return deadlineHijacker{d} }},
		{"http2 flush+push", 2, func(d *deadlineRecorder) http.ResponseWriter { return deadlinePusher{d} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			ran := false
			h := QuietRequestLogger(ExtendWriteDeadline(extend)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				ran = true
				w.WriteHeader(http.StatusOK)
			})))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/1/restore-points/2/contents", nil)
			req.ProtoMajor = tc.protoMajor
			before := time.Now()
			h.ServeHTTP(tc.wrap(rec), req)

			if !ran {
				t.Fatal("handler did not run")
			}
			if rec.write.Before(before.Add(extend)) || rec.write.After(time.Now().Add(extend)) {
				t.Errorf("write deadline = %v, want ≈ now+%v", rec.write, extend)
			}
		})
	}
}

// TestExtendWriteDeadlineServesWhenUnsupported checks a writer without
// deadline support is still served normally.
func TestExtendWriteDeadlineServesWhenUnsupported(t *testing.T) {
	rec := httptest.NewRecorder()
	h := ExtendWriteDeadline(time.Minute)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("got %d %q, want 200 \"ok\"", rec.Code, rec.Body.String())
	}
}

// TestExtendWriteDeadlineOutlivesServerTimeouts proves on a real connection that
// the extended route answers after the server-wide timeouts have passed (and
// that ReadTimeout does not cancel its request context), while a route without
// the middleware still fails.
func TestExtendWriteDeadlineOutlivesServerTimeouts(t *testing.T) {
	const base = 50 * time.Millisecond
	slow := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(4 * base):
			_, _ = io.WriteString(w, "done")
		case <-r.Context().Done():
			_, _ = io.WriteString(w, "cancelled")
		}
	}

	mux := http.NewServeMux()
	mux.Handle("/extended", ExtendWriteDeadline(5*time.Second)(http.HandlerFunc(slow)))
	mux.HandleFunc("/control", slow)
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.WriteTimeout = base
	srv.Config.ReadTimeout = base
	srv.Start()
	defer srv.Close()

	client := srv.Client()
	client.Transport.(*http.Transport).DisableKeepAlives = true

	resp, err := client.Get(srv.URL + "/extended")
	if err != nil {
		t.Fatalf("extended route: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(body) != "done" {
		t.Fatalf("extended route body = %q, err = %v; want \"done\"", body, err)
	}

	resp, err = client.Get(srv.URL + "/control")
	if err == nil {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if string(body) == "done" {
			t.Fatal("control route succeeded past WriteTimeout; test is not exercising the deadline")
		}
	}
}

// TestContentsHandlerTimeoutAnswersBeforeWriteDeadline mirrors the contents
// route's composition with short durations: a handler that overruns its
// budget must still produce a JSON error the SPA can surface, rather than a
// dropped connection once the write deadline passes.
func TestContentsHandlerTimeoutAnswersBeforeWriteDeadline(t *testing.T) {
	if handlers.RestorePointContentsHandlerTimeout >= handlers.RestorePointContentsWriteTimeout {
		t.Fatalf("handler timeout %v must be below write timeout %v",
			handlers.RestorePointContentsHandlerTimeout, handlers.RestorePointContentsWriteTimeout)
	}

	const base = 50 * time.Millisecond
	release := make(chan struct{})
	defer close(release)
	overrun := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release })

	srv := httptest.NewUnstartedServer(ExtendWriteDeadline(6 * base)(
		http.TimeoutHandler(overrun, 3*base, handlers.RestorePointContentsTimeoutBody)))
	srv.Config.WriteTimeout = base
	srv.Start()
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var body struct{ Error string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("timeout body is not JSON: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable || body.Error == "" {
		t.Fatalf("got %d %+v, want 503 with an error message", resp.StatusCode, body)
	}
}

// failingDeadlineWriter reports an unexpected (non-ErrNotSupported) error
// from SetWriteDeadline, e.g. a connection that is already closed.
type failingDeadlineWriter struct{ *httptest.ResponseRecorder }

// SetWriteDeadline always fails with a non-ErrNotSupported error.
func (failingDeadlineWriter) SetWriteDeadline(time.Time) error { return errors.New("conn closed") }

// TestExtendWriteDeadlineLogsUnexpectedErrorAndServes checks an unexpected
// deadline error is logged by path only and the request is still served.
func TestExtendWriteDeadlineLogsUnexpectedErrorAndServes(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	rec := failingDeadlineWriter{httptest.NewRecorder()}
	h := ExtendWriteDeadline(time.Minute)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/1/restore-points/2/contents?item=x", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("got %d %q, want 200 \"ok\"", rec.Code, rec.Body.String())
	}
	got := buf.String()
	if !strings.Contains(got, "extend write deadline for /api/v1/jobs/1/restore-points/2/contents: conn closed") {
		t.Fatalf("log = %q, want the path and error", got)
	}
	if strings.Contains(got, "item=x") {
		t.Fatalf("log leaked the query string: %q", got)
	}
}
