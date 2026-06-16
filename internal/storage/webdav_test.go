package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	xwebdav "golang.org/x/net/webdav"
)

func TestNewWebDAVAdapter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  WebDAVConfig
		wantErr bool
	}{
		{
			name:    "valid https with auth",
			config:  WebDAVConfig{URL: "https://webdav.example.com/", Username: "u", Password: "p", BasePath: "vault-backups"},
			wantErr: false,
		},
		{
			name:    "valid http no auth",
			config:  WebDAVConfig{URL: "http://nas.local:8080/dav"},
			wantErr: false,
		},
		{
			name:    "valid with insecure tls",
			config:  WebDAVConfig{URL: "https://self-signed.local/", InsecureSkipVerify: true},
			wantErr: false,
		},
		{
			name:    "missing url",
			config:  WebDAVConfig{Username: "u", Password: "p"},
			wantErr: true,
		},
		{
			name:    "url without scheme",
			config:  WebDAVConfig{URL: "webdav.example.com"},
			wantErr: true,
		},
		{
			name:    "url with ftp scheme",
			config:  WebDAVConfig{URL: "ftp://example.com/"},
			wantErr: true,
		},
		{
			name:    "chunk size too large",
			config:  WebDAVConfig{URL: "https://webdav.example.com/", ChunkSizeMB: maxWebDAVChunkSizeMB + 1},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewWebDAVAdapter(tt.config)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewWebDAVAdapter() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if a == nil {
					t.Fatal("adapter is nil")
				}
				if strings.HasSuffix(a.config.URL, "/") {
					t.Errorf("trailing slash should be trimmed: %q", a.config.URL)
				}
			}
		})
	}
}

func TestWebDAVChunkSizeDefaults(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		chunkSizeMB int
		wantSize    int64
		wantEnabled bool
	}{
		{"zero defaults to 50 MiB", 0, defaultWebDAVChunkSizeBytes, true},
		{"explicit one MiB", 1, 1024 * 1024, true},
		{"negative disables", -1, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewWebDAVAdapter(WebDAVConfig{URL: "https://x.test/", ChunkSizeMB: tc.chunkSizeMB})
			if err != nil {
				t.Fatal(err)
			}
			gotSize, gotEnabled := a.chunkSize()
			if gotSize != tc.wantSize || gotEnabled != tc.wantEnabled {
				t.Fatalf("chunkSize() = (%d, %t), want (%d, %t)", gotSize, gotEnabled, tc.wantSize, tc.wantEnabled)
			}
		})
	}
}

func TestWebDAVChunkedWriteReadListStatDelete(t *testing.T) {
	a, root, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{ChunkSizeMB: 1})
	defer cleanup()

	data := bytes.Repeat([]byte("vault-webdav-chunked-data"), 100000)
	if err := a.Write("job/item/archive.bin", bytes.NewReader(data)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "job/item/archive.bin")); !os.IsNotExist(err) {
		t.Fatalf("chunked upload should not create logical file directly, stat err = %v", err)
	}

	info, err := a.Stat("job/item/archive.bin")
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Size != int64(len(data)) {
		t.Fatalf("Stat().Size = %d, want %d", info.Size, len(data))
	}

	entries, err := a.List("job/item")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "job/item/archive.bin" || entries[0].Size != int64(len(data)) {
		t.Fatalf("List() = %+v, want one logical chunked file", entries)
	}

	rc, err := a.Read("job/item/archive.bin")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("reassembled data mismatch")
	}

	if err := a.Delete("job/item/archive.bin"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	entries, err = a.List("job/item")
	if err != nil {
		t.Fatalf("List() after delete error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("List() after delete = %+v, want empty", entries)
	}
	// The chunk sidecar directory and manifest must also be gone from disk —
	// List() hides sidecar entries, so a leftover .chunks dir would be an
	// invisible artifact (issue #111).
	_ = filepath.WalkDir(root, func(path string, dEntry fs.DirEntry, _ error) error {
		if dEntry != nil && isWebDAVSidecarName(dEntry.Name()) {
			t.Errorf("sidecar artifact left behind after Delete: %s", path)
		}
		return nil
	})
}

func TestWebDAVSmallWriteUsesLogicalFile(t *testing.T) {
	a, root, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{ChunkSizeMB: 1})
	defer cleanup()

	data := []byte("small file")
	if err := a.Write("small.txt", bytes.NewReader(data)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "small.txt")) // #nosec G304 -- root is t.TempDir owned by this test.
	if err != nil {
		t.Fatalf("read server file: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("small file content mismatch")
	}
	entries, err := a.List("")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "small.txt" {
		t.Fatalf("List() = %+v, want small.txt", entries)
	}
}

func TestWebDAVChunkReadChecksumMismatch(t *testing.T) {
	a, root, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{ChunkSizeMB: 1})
	defer cleanup()

	data := bytes.Repeat([]byte("checksum"), 200000)
	if err := a.Write("archive.bin", bytes.NewReader(data)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	chunkPath := firstWebDAVChunkPath(t, root)
	bad := bytes.Repeat([]byte("x"), 1024*1024)
	if err := os.WriteFile(chunkPath, bad, 0600); err != nil { // #nosec G306 -- test corruption file is under t.TempDir.
		t.Fatalf("corrupt chunk: %v", err)
	}

	rc, err := a.Read("archive.bin")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	_, err = io.ReadAll(rc)
	_ = rc.Close()
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("ReadAll() error = %v, want checksum mismatch", err)
	}
}

func TestWebDAVChunkFailureCleansUploadedChunks(t *testing.T) {
	// Shorten retry backoffs so the exhausted-retry path completes
	// quickly. Restore on exit so other tests still see production
	// defaults if executed in the same binary invocation.
	originalBackoffs := webDAVChunkRetryBackoffs
	webDAVChunkRetryBackoffs = []time.Duration{1 * time.Millisecond, 1 * time.Millisecond}
	defer func() { webDAVChunkRetryBackoffs = originalBackoffs }()

	root := t.TempDir()
	putCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, ".dat") {
			putCount++
			if putCount >= 2 {
				http.Error(w, "forced chunk failure", http.StatusInternalServerError)
				return
			}
		}
		testWebDAVHandler(root).ServeHTTP(w, r)
	}))
	defer server.Close()
	a, err := NewWebDAVAdapter(WebDAVConfig{URL: server.URL, ChunkSizeMB: 1})
	if err != nil {
		t.Fatal(err)
	}

	data := bytes.Repeat([]byte("cleanup"), 300000)
	if err := a.Write("archive.bin", bytes.NewReader(data)); err == nil {
		t.Fatal("Write() succeeded, want forced chunk failure")
	}
	if got := countFilesWithSuffix(t, root, ".dat"); got != 0 {
		t.Fatalf("partial chunks left behind = %d, want 0", got)
	}
}

// TestWebDAVChunkRetryFailFastOn4xx asserts the classifier in
// isWebDAVRetriable short-circuits permanent failures (e.g. 401, 403,
// 413) instead of burning through the full exponential backoff schedule.
func TestWebDAVChunkRetryFailFastOn4xx(t *testing.T) {
	root := t.TempDir()
	putCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, ".dat") {
			putCount++
			http.Error(w, "no permission", http.StatusForbidden)
			return
		}
		testWebDAVHandler(root).ServeHTTP(w, r)
	}))
	defer server.Close()
	a, err := NewWebDAVAdapter(WebDAVConfig{URL: server.URL, ChunkSizeMB: 1})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	data := bytes.Repeat([]byte("noauth"), 300000)
	if err := a.Write("archive.bin", bytes.NewReader(data)); err == nil {
		t.Fatal("Write() succeeded, want 403")
	}
	elapsed := time.Since(start)
	// Production backoffs total ~12s. Fail-fast must skip them and
	// complete in well under a second even on a slow runner.
	if elapsed > 2*time.Second {
		t.Fatalf("Write() took %v, expected fail-fast on 403 (<2s)", elapsed)
	}
	if putCount > 1 {
		t.Fatalf("403 was retried %d times; expected single attempt", putCount)
	}
}

func TestIsWebDAVRetriable(t *testing.T) {
	t.Parallel()
	if isWebDAVRetriable(nil) {
		t.Fatal("nil error must not be retriable")
	}
	if !isWebDAVRetriable(ErrUploadStalled) {
		t.Fatal("ErrUploadStalled must be retriable")
	}
	if !isWebDAVRetriable(errors.New("dial tcp: connection refused")) {
		t.Fatal("network error must be retriable")
	}
	// Status-bearing path errors. gowebdav wraps responses in
	// *os.PathError with the status code prefixed in the Err text.
	mk := func(code, text string) error {
		return &os.PathError{Op: "PUT", Path: "/x", Err: errors.New(code + " " + text)}
	}
	for _, code := range []string{"500", "502", "503", "504", "423", "409", "429"} {
		if !isWebDAVRetriable(mk(code, "fail")) {
			t.Errorf("status %s must be retriable", code)
		}
	}
	for _, code := range []string{"400", "401", "403", "404", "413"} {
		if isWebDAVRetriable(mk(code, "fail")) {
			t.Errorf("status %s must NOT be retriable", code)
		}
	}
}

func TestWebDAVRejectsTraversal(t *testing.T) {
	t.Parallel()
	a, err := NewWebDAVAdapter(WebDAVConfig{URL: "https://x.test/"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.fullPath("../escape", false); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if _, err := a.fullPath("ok/relative", false); err != nil {
		t.Fatalf("legit path rejected: %v", err)
	}
}

// TestWebDAVNoOverallTimeoutByDefault asserts the regression behind the
// #83 follow-up report: previously the WebDAV client had a hard 60-second
// http.Client.Timeout that killed multi-GB PUTs ("context deadline
// exceeded ... Client.Timeout exceeded while awaiting headers"). Default
// configs must carry TimeoutSeconds=0 (no overall lifetime cap) so that
// client() leaves http.Client.Timeout at its zero value. Only an explicit
// TimeoutSeconds opts the operator back into a hard deadline.
func TestWebDAVNoOverallTimeoutByDefault(t *testing.T) {
	t.Parallel()
	a, err := NewWebDAVAdapter(WebDAVConfig{URL: "https://x.test/"})
	if err != nil {
		t.Fatal(err)
	}
	if a.config.TimeoutSeconds != 0 {
		t.Fatalf("default TimeoutSeconds = %d, want 0 (unlimited)", a.config.TimeoutSeconds)
	}
	// client() must succeed without panic when timeout is unset.
	if c := a.client(); c == nil {
		t.Fatal("client() returned nil")
	}

	a2, err := NewWebDAVAdapter(WebDAVConfig{URL: "https://x.test/", TimeoutSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if a2.config.TimeoutSeconds != 600 {
		t.Fatalf("explicit TimeoutSeconds=600 -> %d, want 600", a2.config.TimeoutSeconds)
	}
}

// TestWebDAVStallTimeoutDefaults locks in the 5-minute default watchdog
// window and respects negative-value opt-out.
func TestWebDAVStallTimeoutDefaults(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  int
		want time.Duration
	}{
		{"zero defaults to 5m", 0, 5 * time.Minute},
		{"explicit override", 30, 30 * time.Second},
		{"negative disables", -1, 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewWebDAVAdapter(WebDAVConfig{URL: "https://x.test/", StallTimeoutSeconds: tc.cfg})
			if err != nil {
				t.Fatal(err)
			}
			if got := a.stallTimeout(); got != tc.want {
				t.Fatalf("stallTimeout() = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestStallReaderFiresOnNoProgress ensures the upload watchdog aborts a
// reader that delivers zero bytes for longer than the configured window.
func TestStallReaderFiresOnNoProgress(t *testing.T) {
	t.Parallel()
	// blockingReader returns 0,nil forever until released — simulates a
	// stuck upload where the upstream goroutine is alive but no data is
	// flowing.
	br := &blockingReader{}
	sr := newStallReader(br, 200*time.Millisecond)
	defer sr.cancel()

	deadline := time.After(2 * time.Second)
	for {
		buf := make([]byte, 16)
		_, err := sr.Read(buf)
		if errors.Is(err, ErrUploadStalled) {
			return // success
		}
		if err != nil && err != io.EOF {
			t.Fatalf("unexpected error: %v", err)
		}
		select {
		case <-deadline:
			t.Fatal("stallReader did not fire ErrUploadStalled in time")
		default:
		}
	}
}

// TestStallReaderAllowsSlowProgress confirms a slow-but-steady reader is
// not falsely aborted: as long as some bytes flow within the window, the
// watchdog stays armed but never fires.
func TestStallReaderAllowsSlowProgress(t *testing.T) {
	t.Parallel()
	src := bytes.NewReader(bytes.Repeat([]byte("a"), 1024))
	sr := newStallReader(src, 500*time.Millisecond)
	defer sr.cancel()

	read := 0
	buf := make([]byte, 32)
	for {
		n, err := sr.Read(buf)
		read += n
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		time.Sleep(20 * time.Millisecond) // well under 500ms window
	}
	if read != 1024 {
		t.Fatalf("read %d bytes, want 1024", read)
	}
}

// blockingReader returns (0, nil) indefinitely. Used to simulate a stuck
// upload pipe for the stall watchdog tests.
type blockingReader struct{}

func (b *blockingReader) Read(_ []byte) (int, error) {
	time.Sleep(50 * time.Millisecond)
	return 0, nil
}

// TestWebDAVReadRange exercises HTTP Range support via httptest. Vault's
// dedup layer reads small slices of large pack files, so the WebDAV adapter
// must issue a Range request rather than downloading the whole object.
func TestWebDAVReadRange(t *testing.T) {
	t.Parallel()
	payload := []byte("the quick brown fox jumps over the lazy dog")

	// Track that the server actually received Range: bytes=10-14.
	var gotRange string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		gotRange = r.Header.Get("Range")
		// Parse Range header: "bytes=START-END" (inclusive).
		if !strings.HasPrefix(gotRange, "bytes=") {
			http.Error(w, "missing range", http.StatusBadRequest)
			return
		}
		spec := strings.TrimPrefix(gotRange, "bytes=")
		parts := strings.SplitN(spec, "-", 2)
		if len(parts) != 2 {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		start, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			http.Error(w, "bad range start", http.StatusBadRequest)
			return
		}
		end, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			http.Error(w, "bad range end", http.StatusBadRequest)
			return
		}
		if start >= int64(len(payload)) {
			http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if end >= int64(len(payload)) {
			end = int64(len(payload)) - 1
		}
		slice := payload[start : end+1]
		w.Header().Set("Content-Range", "bytes "+parts[0]+"-"+strconv.FormatInt(end, 10)+"/"+strconv.Itoa(len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(slice)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(slice)
	}))
	defer server.Close()

	a, err := NewWebDAVAdapter(WebDAVConfig{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	rc, err := a.ReadRange("file.bin", 10, 5)
	if err != nil {
		t.Fatalf("ReadRange() error = %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(got) != "brown" {
		t.Fatalf("ReadRange returned %q, want %q", got, "brown")
	}
	if gotRange != "bytes=10-14" {
		t.Fatalf("server saw Range %q, want %q", gotRange, "bytes=10-14")
	}
}

// TestWebDAVReadRangeFallbackOn200 exercises the fallback path for servers
// that ignore Range and return the entire 200 OK body. The adapter must
// slice the response client-side so callers still get the requested window.
func TestWebDAVReadRangeFallbackOn200(t *testing.T) {
	t.Parallel()
	payload := []byte("the quick brown fox jumps over the lazy dog")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately ignore Range — emulate a server that strips the header.
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	a, err := NewWebDAVAdapter(WebDAVConfig{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	rc, err := a.ReadRange("file.bin", 10, 5)
	if err != nil {
		t.Fatalf("ReadRange() error = %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(got) != "brown" {
		t.Fatalf("ReadRange fallback returned %q, want %q", got, "brown")
	}
}

// TestWebDAVReadRangeChunkedObject — ReadRange on a chunked (sidecar
// manifest) object must serve logical object bytes across chunk boundaries.
// Restores stream every chunked object through ReadRange (the resumable
// reader opens its very first stream with ReadRange at offset 0); before
// chunk support here, that request 404'd — nothing exists at the logical
// path of a chunked upload — and restoring any chunked WebDAV object failed.
func TestWebDAVReadRangeChunkedObject(t *testing.T) {
	a, _, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{ChunkSizeMB: 1})
	defer cleanup()

	const chunk = 1024 * 1024
	data := make([]byte, 2*chunk+512) // three chunks: 1 MiB, 1 MiB, 512 B
	for i := range data {
		data[i] = byte(i % 251)
	}
	if err := a.Write("job/item/archive.bin", bytes.NewReader(data)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	size := int64(len(data))
	cases := []struct {
		name           string
		offset, length int64
	}{
		{"whole object", 0, size},
		{"within first chunk", 10, 100},
		{"spans first boundary", chunk - 64, 128},
		{"spans all chunks", 512, size - 1024},
		{"tail of last chunk", size - 100, 100},
		{"length past EOF truncates", size - 100, 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc, err := a.ReadRange("job/item/archive.bin", tc.offset, tc.length)
			if err != nil {
				t.Fatalf("ReadRange(%d, %d) error = %v", tc.offset, tc.length, err)
			}
			got, err := io.ReadAll(rc)
			_ = rc.Close()
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			end := tc.offset + tc.length
			if end > size {
				end = size
			}
			if !bytes.Equal(got, data[tc.offset:end]) {
				t.Fatalf("ReadRange(%d, %d) returned %d bytes with wrong content, want bytes [%d,%d)",
					tc.offset, tc.length, len(got), tc.offset, end)
			}
		})
	}

	// Offset at or past EOF mirrors plain-object HTTP 416 semantics: an error,
	// not an empty success.
	if rc, err := a.ReadRange("job/item/archive.bin", size, 1); err == nil {
		_ = rc.Close()
		t.Fatal("ReadRange(EOF, 1) succeeded, want error")
	}

	// A genuinely missing object must still fail cleanly, not be mistaken for
	// a chunked one.
	if rc, err := a.ReadRange("job/item/missing.bin", 0, 4); err == nil {
		_ = rc.Close()
		t.Fatal("ReadRange(missing) succeeded, want error")
	}
}

func newTestWebDAVAdapter(t *testing.T, cfg WebDAVConfig) (*WebDAVAdapter, string, func()) {
	t.Helper()
	root := t.TempDir()
	server := httptest.NewServer(testWebDAVHandler(root))
	cfg.URL = server.URL
	a, err := NewWebDAVAdapter(cfg)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return a, root, server.Close
}

func testWebDAVHandler(root string) http.Handler {
	return &xwebdav.Handler{
		FileSystem: xwebdav.Dir(root),
		LockSystem: xwebdav.NewMemLS(),
	}
}

func firstWebDAVChunkPath(t *testing.T, root string) string {
	t.Helper()
	var found string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".dat") && found == "" {
			found = p
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == "" {
		t.Fatal("no chunk file found")
	}
	return found
}

func countFilesWithSuffix(t *testing.T, root, suffix string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, suffix) {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWebDAVGetCapacityWithQuota(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			body, _ := io.ReadAll(r.Body)
			if bytes.Contains(body, []byte("quota-available-bytes")) {
				w.Header().Set("Content-Type", "application/xml; charset=utf-8")
				w.WriteHeader(207)
				fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/</d:href>
    <d:propstat>
      <d:prop>
        <d:quota-available-bytes>113013082759</d:quota-available-bytes>
        <d:quota-used-bytes>230584300921</d:quota-used-bytes>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`)
				return
			}
		}
		testWebDAVHandler(root).ServeHTTP(w, r)
	}))
	defer server.Close()
	a, err := NewWebDAVAdapter(WebDAVConfig{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	cap, err := a.GetCapacity(context.Background())
	if err != nil {
		t.Fatalf("GetCapacity: %v", err)
	}
	if cap.Source != "webdav-quota" {
		t.Errorf("source = %q, want webdav-quota", cap.Source)
	}
	if cap.UsedBytes != 230584300921 {
		t.Errorf("used = %d", cap.UsedBytes)
	}
	if cap.FreeBytes != 113013082759 {
		t.Errorf("free = %d", cap.FreeBytes)
	}
	if want := int64(230584300921) + int64(113013082759); cap.TotalBytes != want {
		t.Errorf("total = %d, want %d", cap.TotalBytes, want)
	}
}

func TestWebDAVGetCapacityNoQuotaServer(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// The default xwebdav handler doesn't return quota props.
	server := httptest.NewServer(testWebDAVHandler(root))
	defer server.Close()
	a, err := NewWebDAVAdapter(WebDAVConfig{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	cap, err := a.GetCapacity(context.Background())
	if err != nil {
		t.Fatalf("GetCapacity: %v", err)
	}
	if cap.Source != "webdav-quota" {
		t.Errorf("source = %q, want webdav-quota", cap.Source)
	}
	if cap.TotalBytes != 0 {
		t.Errorf("expected TotalBytes=0 fallback, got %d", cap.TotalBytes)
	}
}

// TestWebDAVGetCapacityNextcloudSentinel locks in the Nextcloud / ownCloud
// "no per-user quota" convention: the server returns
// <d:quota-available-bytes>-3</d:quota-available-bytes> (or -1 for
// "unlimited") together with a real <d:quota-used-bytes> count. The
// adapter must emit UsedBytes=<real number> with TotalBytes=0 so the
// UI's "used-only" path renders "Used: 565 MB (no quota)" rather
// than hiding the real number behind a "no quota reported" fallback.
//
// This case is the most common Nextcloud configuration in the wild —
// admins frequently don't set per-user quotas, so every Nextcloud
// destination would otherwise look identical to a server that
// genuinely doesn't implement RFC 4331.
func TestWebDAVGetCapacityNextcloudSentinel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			body, _ := io.ReadAll(r.Body)
			if bytes.Contains(body, []byte("quota-available-bytes")) {
				w.Header().Set("Content-Type", "application/xml; charset=utf-8")
				w.WriteHeader(207)
				// -3 is Nextcloud's "unknown" sentinel; -1 would be "unlimited".
				// Same handling either way — both are < 0.
				fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/</d:href>
    <d:propstat>
      <d:prop>
        <d:quota-available-bytes>-3</d:quota-available-bytes>
        <d:quota-used-bytes>593184297</d:quota-used-bytes>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`)
				return
			}
		}
		testWebDAVHandler(root).ServeHTTP(w, r)
	}))
	defer server.Close()
	a, err := NewWebDAVAdapter(WebDAVConfig{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	cap, err := a.GetCapacity(context.Background())
	if err != nil {
		t.Fatalf("GetCapacity: %v", err)
	}
	if cap.Source != "webdav-quota" {
		t.Errorf("source = %q, want webdav-quota", cap.Source)
	}
	if cap.TotalBytes != 0 {
		t.Errorf("expected TotalBytes=0 (no quota), got %d", cap.TotalBytes)
	}
	if cap.UsedBytes != 593184297 {
		t.Errorf("expected UsedBytes=593184297 (real number kept), got %d", cap.UsedBytes)
	}
	if cap.FreeBytes != 0 {
		t.Errorf("expected FreeBytes=0 when no quota, got %d", cap.FreeBytes)
	}
}

// TestWebDAVListMissingDirIsNotExist verifies that listing a directory that
// does not exist surfaces a not-found error detectable by IsNotExist, so the
// cleanup path can treat an already-deleted directory as success (issue #143).
func TestWebDAVListMissingDirIsNotExist(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{})
	defer cleanup()

	_, err := a.List("does/not/exist")
	if err == nil {
		t.Fatal("List(missing) error = nil, want a not-found error")
	}
	if !IsNotExist(err) {
		t.Errorf("IsNotExist(List error) = false, want true; err = %v", err)
	}
}
