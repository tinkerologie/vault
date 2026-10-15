package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// faultyDAV is an httptest WebDAV server backed by golang.org/x/net/webdav
// whose responses can be overridden per request by a swappable fault hook.
type faultyDAV struct {
	root   string
	server *httptest.Server
	mu     sync.Mutex
	fault  func(w http.ResponseWriter, r *http.Request) bool
}

func newFaultyDAV(t *testing.T) *faultyDAV {
	t.Helper()
	f := &faultyDAV{root: t.TempDir()}
	h := testWebDAVHandler(f.root)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		fault := f.fault
		f.mu.Unlock()
		if fault != nil && fault(w, r) {
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *faultyDAV) set(fn func(w http.ResponseWriter, r *http.Request) bool) {
	f.mu.Lock()
	f.fault = fn
	f.mu.Unlock()
}

// failWhen returns a fault hook answering status for requests matching
// method (empty = any) whose path satisfies match.
func failWhen(method string, match func(string) bool, status int) func(http.ResponseWriter, *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if (method == "" || r.Method == method) && match(r.URL.Path) {
			http.Error(w, http.StatusText(status), status)
			return true
		}
		return false
	}
}

func suffix(s string) func(string) bool {
	return func(p string) bool { return strings.HasSuffix(p, s) }
}

func (f *faultyDAV) adapter(t *testing.T, cfg WebDAVConfig) *WebDAVAdapter {
	t.Helper()
	cfg.URL = f.server.URL
	a, err := NewWebDAVAdapter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// chunkedPayload is just over one 1 MiB chunk so ChunkSizeMB:1 writes two
// chunks plus a manifest.
var chunkedPayload = bytes.Repeat([]byte("0123456789abcdef"), 70000)

// errAfterReader yields n bytes of data and then fails.
type errAfterReader struct {
	n   int
	err error
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, r.err
	}
	k := min(len(p), r.n)
	for i := range p[:k] {
		p[i] = 'z'
	}
	r.n -= k
	return k, nil
}

func fastWebDAVRetries(t *testing.T) {
	t.Helper()
	old := webDAVChunkRetryBackoffs
	webDAVChunkRetryBackoffs = []time.Duration{0}
	t.Cleanup(func() { webDAVChunkRetryBackoffs = old })
}

func TestWebDAV458_PlainObjectOperations(t *testing.T) {
	t.Parallel()
	f := newFaultyDAV(t)
	a := f.adapter(t, WebDAVConfig{Username: "u", Password: "p", TimeoutSeconds: 30, BasePath: "base"})

	if err := a.WriteFrom("dir/plain.txt", func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("hello plain")), nil
	}); err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}
	rc, err := a.Read("dir/plain.txt")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "hello plain" {
		t.Fatalf("Read = %q", got)
	}
	st, err := a.Stat("dir/plain.txt")
	if err != nil || st.Size != 11 || st.IsDir {
		t.Fatalf("Stat = %+v, %v", st, err)
	}
	rr, err := a.ReadRange("dir/plain.txt", 6, 5)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	got, _ = io.ReadAll(rr)
	_ = rr.Close()
	if string(got) != "plain" {
		t.Fatalf("ReadRange = %q", got)
	}

	bad := "../../escape"
	if _, err := a.Read(bad); err == nil {
		t.Error("Read traversal: want error")
	}
	if _, err := a.ReadRange(bad, 0, 1); err == nil {
		t.Error("ReadRange traversal: want error")
	}
}

func TestWebDAV458_ManifestFailuresSurface(t *testing.T) {
	t.Parallel()
	f := newFaultyDAV(t)
	a := f.adapter(t, WebDAVConfig{})
	f.set(failWhen(http.MethodGet, suffix(webDAVManifestSuffix), http.StatusInternalServerError))

	if _, err := a.Read("x.bin"); err == nil || !strings.Contains(err.Error(), "read chunk manifest") {
		t.Errorf("Read: %v", err)
	}
	if _, err := a.Stat("x.bin"); err == nil || !strings.Contains(err.Error(), "read chunk manifest") {
		t.Errorf("Stat: %v", err)
	}
	if _, err := a.ReadRange("x.bin", 0, 1); err == nil || !strings.Contains(err.Error(), "read chunk manifest") {
		t.Errorf("ReadRange: %v", err)
	}
	if err := a.Write("x.bin", strings.NewReader("small")); err == nil {
		t.Error("Write (buffered single): want manifest error")
	}
	if err := a.Delete("x.bin"); err == nil {
		t.Error("Delete: want sidecar error")
	}
	neg := f.adapter(t, WebDAVConfig{ChunkSizeMB: -1})
	if err := neg.Write("x.bin", strings.NewReader("small")); err == nil {
		t.Error("Write (temp single): want manifest error")
	}
}

func TestWebDAV458_CorruptAndUnsupportedManifests(t *testing.T) {
	t.Parallel()
	f := newFaultyDAV(t)
	a := f.adapter(t, WebDAVConfig{})
	write := func(logical, body string) {
		t.Helper()
		full := "/" + logical
		p := filepath.Join(f.root, filepath.FromSlash(strings.TrimPrefix(webDAVManifestPath(full), "/")))
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("garbage.bin", "{not json")
	write("future.bin", `{"version":99,"path":"future.bin"}`)

	if _, err := a.Stat("garbage.bin"); err == nil || !strings.Contains(err.Error(), "decode chunk manifest") {
		t.Errorf("Stat garbage manifest: %v", err)
	}
	if _, err := a.Stat("future.bin"); err == nil || !strings.Contains(err.Error(), "unsupported chunk manifest version") {
		t.Errorf("Stat future manifest: %v", err)
	}
	// List decodes every manifest it sees; the corrupt one fails the listing.
	if _, err := a.List(""); err == nil || !strings.Contains(err.Error(), "decode chunk manifest") {
		t.Errorf("List with corrupt manifest: %v", err)
	}
}

func TestWebDAV458_ListErrors(t *testing.T) {
	t.Parallel()
	f := newFaultyDAV(t)
	a := f.adapter(t, WebDAVConfig{ChunkSizeMB: 1})
	if err := a.Write("big.bin", bytes.NewReader(chunkedPayload)); err != nil {
		t.Fatal(err)
	}
	f.set(failWhen(http.MethodGet, suffix(webDAVManifestSuffix), http.StatusInternalServerError))
	if _, err := a.List(""); err == nil || !strings.Contains(err.Error(), "read chunk manifest") {
		t.Errorf("List manifest read failure: %v", err)
	}
	f.set(failWhen("PROPFIND", func(string) bool { return true }, http.StatusInternalServerError))
	if _, err := a.List(""); err == nil || IsNotExist(err) {
		t.Errorf("List PROPFIND 500: want non-not-exist error, got %v", err)
	}
}

func TestWebDAV458_DeleteFailures(t *testing.T) {
	t.Parallel()
	f := newFaultyDAV(t)
	a := f.adapter(t, WebDAVConfig{ChunkSizeMB: 1})
	if err := a.Write("plain.txt", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	f.set(failWhen(http.MethodDelete, suffix("plain.txt"), http.StatusInternalServerError))
	if err := a.Delete("plain.txt"); err == nil || !strings.Contains(err.Error(), "webdav: delete") {
		t.Errorf("Delete 500: %v", err)
	}

	// The fault above only matches plain.txt; this write must succeed.
	if err := a.Write("big.bin", bytes.NewReader(chunkedPayload)); err != nil {
		t.Fatalf("chunked write: %v", err)
	}
	cases := map[string]func(string) bool{
		"chunk":     suffix(".dat"),
		"manifest":  suffix(webDAVManifestSuffix),
		"chunk dir": suffix(".chunks"),
	}
	for name, match := range cases {
		// Re-create the chunked object so every case starts from a full set
		// of sidecars regardless of map iteration order.
		f.set(nil)
		if err := a.Write("big.bin", bytes.NewReader(chunkedPayload)); err != nil {
			t.Fatalf("chunked write: %v", err)
		}
		f.set(failWhen(http.MethodDelete, match, http.StatusInternalServerError))
		if err := a.Delete("big.bin"); err == nil {
			t.Errorf("Delete with %s DELETE failing: want error", name)
		}
	}
}

func TestWebDAV458_WriteSidecarCleanupFailures(t *testing.T) {
	t.Parallel()
	f := newFaultyDAV(t)
	f.set(failWhen(http.MethodDelete, suffix(".chunks"), http.StatusInternalServerError))
	for _, cfg := range []WebDAVConfig{{}, {ChunkSizeMB: -1}, {ChunkSizeMB: 1}} {
		a := f.adapter(t, cfg)
		var payload io.Reader = strings.NewReader("small")
		if cfg.ChunkSizeMB == 1 {
			payload = bytes.NewReader(chunkedPayload)
		}
		if err := a.Write("w.bin", payload); err == nil || !strings.Contains(err.Error(), "delete chunk dir") {
			t.Errorf("Write %+v: want chunk dir delete error, got %v", cfg, err)
		}
	}
}

func TestWebDAV458_ChunkedWriteRemoveExistingFails(t *testing.T) {
	t.Parallel()
	f := newFaultyDAV(t)
	a := f.adapter(t, WebDAVConfig{ChunkSizeMB: 1})
	f.set(func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/big.bin") {
			http.Error(w, "nope", http.StatusForbidden)
			return true
		}
		return false
	})
	if err := a.Write("big.bin", bytes.NewReader(chunkedPayload)); err == nil || !strings.Contains(err.Error(), "remove existing") {
		t.Errorf("Write: %v", err)
	}
}

func TestWebDAV458_ChunkedWriteReaderFailsMidStream(t *testing.T) {
	t.Parallel()
	f := newFaultyDAV(t)
	a := f.adapter(t, WebDAVConfig{ChunkSizeMB: 1})
	boom := errors.New("source broke")
	// Exactly one full chunk, then the source fails on the second chunk read.
	err := a.Write("mid.bin", &errAfterReader{n: 1024*1024 + 10, err: boom})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "read chunk") {
		t.Fatalf("Write: %v", err)
	}
	if n := countFilesWithSuffix(t, f.root, ".dat"); n != 0 {
		t.Errorf("chunks left behind after failed write: %d", n)
	}

	// A failure before the first chunk is buffered surfaces from Write itself.
	if err := a.Write("early.bin", &errAfterReader{n: 10, err: boom}); !errors.Is(err, boom) {
		t.Errorf("Write early failure: %v", err)
	}
	// Chunking disabled: the spool to the temp file fails.
	neg := f.adapter(t, WebDAVConfig{ChunkSizeMB: -1})
	if err := neg.Write("spool.bin", &errAfterReader{n: 10, err: boom}); !errors.Is(err, boom) || !strings.Contains(err.Error(), "spool") {
		t.Errorf("Write temp spool failure: %v", err)
	}
}

func TestWebDAV458_ChunkedWriteManifestPutFailsAndCleanupLogs(t *testing.T) {
	fastWebDAVRetries(t)
	f := newFaultyDAV(t)
	a := f.adapter(t, WebDAVConfig{ChunkSizeMB: 1})
	var manifestDeletes atomic.Int32
	f.set(func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, webDAVManifestSuffix):
			// The first (pre-write) manifest DELETE passes; the cleanup one fails.
			if manifestDeletes.Add(1) > 1 {
				http.Error(w, "busy", http.StatusInternalServerError)
				return true
			}
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, webDAVManifestSuffix):
			http.Error(w, "forbidden", http.StatusForbidden)
			return true
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, webDAVSidecarPrefix):
			// Pre-write sidecar cleanup sees 404s (nothing exists yet); the
			// post-failure cleanup DELETEs fail and are only logged.
			if _, err := os.Stat(filepath.Join(f.root, filepath.FromSlash(r.URL.Path))); err == nil {
				http.Error(w, "busy", http.StatusInternalServerError)
				return true
			}
		}
		return false
	})
	if err := a.Write("m.bin", bytes.NewReader(chunkedPayload)); err == nil {
		t.Fatal("Write: want manifest PUT failure")
	}
}

func TestWebDAV458_MkdirRetryAndFatal(t *testing.T) {
	fastWebDAVRetries(t)
	f := newFaultyDAV(t)
	a := f.adapter(t, WebDAVConfig{})
	var mu sync.Mutex
	mkcols := 0
	f.set(func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == "MKCOL" {
			mu.Lock()
			mkcols++
			n := mkcols
			mu.Unlock()
			if n == 1 {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return true
			}
		}
		return false
	})
	if err := a.Write("a/b/c.txt", strings.NewReader("x")); err != nil {
		t.Fatalf("Write after one retriable MKCOL failure: %v", err)
	}

	f.set(failWhen("MKCOL", func(string) bool { return true }, http.StatusServiceUnavailable))
	if err := a.Write("d/e.txt", strings.NewReader("x")); err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("Write with MKCOL always 503: %v", err)
	}

	// A failing MKCOL on a directory that verifiably exists is tolerated.
	if err := os.MkdirAll(filepath.Join(f.root, "exists"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.set(failWhen("MKCOL", func(string) bool { return true }, http.StatusConflict))
	if err := mkdirAllWithRetry(a.client(), "/exists"); err != nil {
		t.Errorf("mkdirAllWithRetry on existing dir: %v", err)
	}
}

func TestWebDAV458_TempFileCreateFails(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))
	f := newFaultyDAV(t)
	a := f.adapter(t, WebDAVConfig{ChunkSizeMB: -1})
	if err := a.Write("t.bin", strings.NewReader("x")); err == nil || !strings.Contains(err.Error(), "create upload temp file") {
		t.Errorf("Write with unusable TMPDIR: %v", err)
	}
}

func TestWebDAV458_TestConnectionMkdirFails(t *testing.T) {
	t.Parallel()
	f := newFaultyDAV(t)
	f.set(failWhen("MKCOL", func(string) bool { return true }, http.StatusForbidden))
	a := f.adapter(t, WebDAVConfig{BasePath: "missing"})
	if err := a.TestConnection(); err == nil || !strings.Contains(err.Error(), "connection test failed") {
		t.Errorf("TestConnection: %v", err)
	}
}

func TestWebDAV458_RawRangeStatuses(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	status := http.StatusOK
	body := "0123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		s := status
		mu.Unlock()
		if s == -1 { // truncated body: promise more than is sent
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "abc")
			return
		}
		w.WriteHeader(s)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	a, err := NewWebDAVAdapter(WebDAVConfig{URL: srv.URL, Username: "u", Password: "p", TimeoutSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	setStatus := func(s int) { mu.Lock(); status = s; mu.Unlock() }

	setStatus(http.StatusOK) // server ignores Range; offset past EOF
	if _, err := a.ReadRange("f", 50, 2); err == nil || !strings.Contains(err.Error(), "past EOF") {
		t.Errorf("200 past EOF: %v", err)
	}
	setStatus(-1)
	if _, err := a.ReadRange("f", 50, 2); err == nil || !strings.Contains(err.Error(), "skip to offset") {
		t.Errorf("200 truncated: %v", err)
	}
	setStatus(http.StatusRequestedRangeNotSatisfiable)
	if _, err := a.ReadRange("f", 1, 2); err == nil || !strings.Contains(err.Error(), "not satisfiable") {
		t.Errorf("416: %v", err)
	}
	setStatus(http.StatusBadGateway)
	if _, err := a.ReadRange("f", 1, 2); err == nil || !strings.Contains(err.Error(), "unexpected status 502") {
		t.Errorf("502: %v", err)
	}

	bad := &WebDAVAdapter{config: WebDAVConfig{URL: "http://[::1"}}
	if _, err := bad.rawRangeRequest("/f", 0, 1); err == nil || !strings.Contains(err.Error(), "build range request") {
		t.Errorf("bad URL: %v", err)
	}
	srv.Close()
	if _, err := a.ReadRange("f", 1, 2); err == nil || !strings.Contains(err.Error(), "range get") {
		t.Errorf("closed server: %v", err)
	}
}

func TestWebDAV458_ChunkRangeReaderEdges(t *testing.T) {
	t.Parallel()
	f := newFaultyDAV(t)
	a := f.adapter(t, WebDAVConfig{ChunkSizeMB: 1})
	if err := a.Write("big.bin", bytes.NewReader(chunkedPayload)); err != nil {
		t.Fatal(err)
	}

	// Close mid-stream releases the open chunk body.
	rr, err := a.ReadRange("big.bin", 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := rr.Read(buf); err != nil {
		t.Fatal(err)
	}
	if err := rr.Close(); err != nil {
		t.Errorf("Close mid-stream: %v", err)
	}
	if err := rr.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	// A chunk GET failing surfaces from Read.
	f.set(failWhen(http.MethodGet, suffix(".dat"), http.StatusBadGateway))
	rr, err = a.ReadRange("big.bin", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rr); err == nil {
		t.Error("chunk GET 502: want read error")
	}
	_ = rr.Close()
	f.set(nil)

	// A manifest claiming more data than its chunks hold is truncation.
	short := &webDAVChunkRangeReader{w: a, remaining: 5}
	if _, err := short.Read(buf); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated manifest: %v", err)
	}

	// Range spanning the chunk boundary crosses into the second chunk.
	rr, err = a.ReadRange("big.bin", 1024*1024-2, 4)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rr)
	_ = rr.Close()
	if err != nil || !bytes.Equal(got, chunkedPayload[1024*1024-2:1024*1024+2]) {
		t.Errorf("boundary range = %q, %v", got, err)
	}
}

func TestWebDAV458_ChunkReaderFailures(t *testing.T) {
	t.Parallel()
	f := newFaultyDAV(t)
	a := f.adapter(t, WebDAVConfig{ChunkSizeMB: 1})
	if err := a.Write("big.bin", bytes.NewReader(chunkedPayload)); err != nil {
		t.Fatal(err)
	}

	f.set(failWhen(http.MethodGet, suffix(".dat"), http.StatusBadGateway))
	rc, err := a.Read("big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rc); err == nil || !strings.Contains(err.Error(), "read chunk") {
		t.Errorf("chunk GET failure: %v", err)
	}
	_ = rc.Close()

	// Truncated chunk body: the transport reports an unexpected EOF.
	f.set(func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, ".dat") {
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "short")
			return true
		}
		return false
	})
	rc, err = a.Read("big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rc); err == nil {
		t.Error("truncated chunk body: want error")
	}
	_ = rc.Close()
	f.set(nil)

	// A chunk shorter than its manifest entry is a size mismatch.
	chunk := firstWebDAVChunkPath(t, f.root)
	if err := os.WriteFile(chunk, []byte("tiny"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, err = a.Read("big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rc); err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Errorf("short chunk: %v", err)
	}
	_ = rc.Close()
}

func TestWebDAV458_GetCapacityPaths(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	status := http.StatusNotImplemented
	var gotAuth bool
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		s := status
		_, _, gotAuth = r.BasicAuth()
		gotPath = r.URL.Path
		mu.Unlock()
		if s == -1 {
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, "<d:multistatus")
			return
		}
		w.WriteHeader(s)
	}))
	t.Cleanup(srv.Close)
	a, err := NewWebDAVAdapter(WebDAVConfig{URL: srv.URL, Username: "u", Password: "p", BasePath: "sub/dir"})
	if err != nil {
		t.Fatal(err)
	}
	setStatus := func(s int) { mu.Lock(); status = s; mu.Unlock() }

	c, err := a.GetCapacity(context.Background())
	if err != nil || c.TotalBytes != 0 || c.Source != "webdav-quota" {
		t.Errorf("501: %+v, %v", c, err)
	}
	mu.Lock()
	if !gotAuth || gotPath != "/sub/dir/" {
		t.Errorf("auth=%v path=%q", gotAuth, gotPath)
	}
	mu.Unlock()

	setStatus(http.StatusInternalServerError)
	if _, err := a.GetCapacity(context.Background()); err == nil || !strings.Contains(err.Error(), "propfind status 500") {
		t.Errorf("500: %v", err)
	}
	setStatus(-1)
	if _, err := a.GetCapacity(context.Background()); err == nil || !strings.Contains(err.Error(), "read propfind body") {
		t.Errorf("truncated body: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.GetCapacity(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled ctx: %v", err)
	}

	bad := &WebDAVAdapter{config: WebDAVConfig{URL: "http://[::1"}}
	if _, err := bad.GetCapacity(context.Background()); err == nil || !strings.Contains(err.Error(), "build propfind") {
		t.Errorf("bad URL: %v", err)
	}
	srv.Close()
	if _, err := a.GetCapacity(context.Background()); err == nil || !strings.Contains(err.Error(), "webdav: propfind") {
		t.Errorf("closed server: %v", err)
	}
}

// rtFunc adapts a function to http.RoundTripper.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWebDAV458_MkcolTransportEdges(t *testing.T) {
	t.Parallel()
	forbidden := func() *http.Response {
		return &http.Response{StatusCode: http.StatusForbidden, Status: "403 Forbidden", Body: io.NopCloser(strings.NewReader(""))}
	}

	// Digest-authenticated MKCOL: no probe, original 403 preserved.
	probes := 0
	tr := &mkcolExistsTransport{base: rtFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "PROPFIND" {
			probes++
		}
		return forbidden(), nil
	})}
	req, _ := http.NewRequest("MKCOL", "http://example.invalid/dir", nil)
	req.Header.Set("Authorization", `Digest username="u"`)
	resp, err := tr.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusForbidden || probes != 0 {
		t.Errorf("digest: status=%v err=%v probes=%d", resp.StatusCode, err, probes)
	}

	// Probe transport failure: original 403 preserved.
	tr = &mkcolExistsTransport{base: rtFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "PROPFIND" {
			return nil, errors.New("probe failed")
		}
		return forbidden(), nil
	})}
	req, _ = http.NewRequest("MKCOL", "http://example.invalid/dir", nil)
	resp, err = tr.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("probe error: status=%v err=%v", resp.StatusCode, err)
	}
}

func TestWebDAV458_ClientOptions(t *testing.T) {
	t.Parallel()
	a := &WebDAVAdapter{config: WebDAVConfig{URL: "https://example.invalid", InsecureSkipVerify: true, TimeoutSeconds: 7}}
	if tr := a.transport(); tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify not applied to transport")
	}
	if c := a.client(); c == nil {
		t.Error("client() returned nil")
	}
}

func TestWebDAV458_HelperEdges(t *testing.T) {
	t.Parallel()
	if got := httpErrorCode(&os.PathError{Op: "x", Path: "/", Err: errors.New("Locked out")}); got != 0 {
		t.Errorf("non-numeric status prefix = %d, want 0", got)
	}
	if _, ok := extractWebDAVPropInt([]byte("<d:quota-used-bytes>99999999999999999999999</d:quota-used-bytes>"), "quota-used-bytes"); ok {
		t.Error("overflowing integer must not parse")
	}
	if _, _, err := readWebDAVChunk(&errAfterReader{n: 0, err: errors.New("x")}, 10); err == nil {
		t.Error("readWebDAVChunk: want error")
	}
}
