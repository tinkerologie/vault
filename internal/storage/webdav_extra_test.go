package storage

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- String / MarshalJSON redaction ----------------------------------

func TestWebDAVConfig_StringRedactsPassword(t *testing.T) {
	t.Parallel()
	c := WebDAVConfig{
		URL:      "https://x.test/",
		Username: "user",
		Password: "supersecret",
		BasePath: "vault",
	}
	got := c.String()
	if strings.Contains(got, "supersecret") {
		t.Errorf("String() leaked password: %q", got)
	}
	if !strings.Contains(got, "<redacted>") {
		t.Errorf("String() missing <redacted> marker: %q", got)
	}
	if !strings.Contains(got, "user") || !strings.Contains(got, "https://x.test/") {
		t.Errorf("String() missing non-secret fields: %q", got)
	}
}

func TestWebDAVConfig_StringEmptyPasswordOmitted(t *testing.T) {
	t.Parallel()
	c := WebDAVConfig{URL: "https://x.test/", Username: "user"}
	got := c.String()
	if strings.Contains(got, "<redacted>") {
		t.Errorf("empty password should not render <redacted>: %q", got)
	}
}

func TestWebDAVConfig_MarshalJSONRedactsPassword(t *testing.T) {
	t.Parallel()
	c := WebDAVConfig{URL: "https://x.test/", Username: "u", Password: "s3cret"}
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "s3cret") {
		t.Errorf("MarshalJSON leaked password: %s", s)
	}
	// json.Marshal HTML-escapes "<" / ">" by default, so the in-wire form
	// is "<redacted>". Decode round-trip to assert the logical
	// value rather than coupling to the encoder's escape rules.
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got["password"] != "<redacted>" {
		t.Errorf("MarshalJSON password = %v, want %q", got["password"], "<redacted>")
	}
}

func TestWebDAVConfig_MarshalJSONEmptyPasswordOmitted(t *testing.T) {
	t.Parallel()
	c := WebDAVConfig{URL: "https://x.test/", Username: "u"}
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// Password should be "" verbatim, not "<redacted>".
	if pw, ok := got["password"].(string); ok && pw == "<redacted>" {
		t.Errorf("empty password should not render <redacted>: %s", out)
	}
}

// ---- writeTempSingle (chunking disabled path) ------------------------

// TestWebDAVWriteTempSingle drives the "ChunkSizeMB < 0 disables
// chunking" path: Write() falls through to writeTempSingle which
// spools the body to a host temp file before issuing a single PUT.
func TestWebDAVWriteTempSingle(t *testing.T) {
	a, root, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{ChunkSizeMB: -1})
	defer cleanup()

	payload := []byte("temp single payload — non-chunked")
	if err := a.Write("file.bin", bytes.NewReader(payload)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "file.bin")) // #nosec G304 — root is t.TempDir
	if err != nil {
		t.Fatalf("read server file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload mismatch: got %q, want %q", got, payload)
	}
}

// ---- TestConnection branches ----------------------------------------

func TestWebDAVTestConnection_Success(t *testing.T) {
	t.Parallel()
	_, _, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{})
	defer cleanup()
	a, root, cleanup2 := newTestWebDAVAdapter(t, WebDAVConfig{})
	defer cleanup2()
	// Touch a real dir so ReadDir succeeds on the base.
	_ = root
	if err := a.TestConnection(); err != nil {
		t.Errorf("TestConnection on healthy server returned %v", err)
	}
}

// TestWebDAVTestConnection_MissingBasePathMkdir exercises the 404 →
// MkdirAll path: the configured base path doesn't exist yet; the
// adapter must create it and return success.
func TestWebDAVTestConnection_MissingBasePathMkdir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	server := httptest.NewServer(testWebDAVHandler(root))
	defer server.Close()

	a, err := NewWebDAVAdapter(WebDAVConfig{URL: server.URL, BasePath: "auto-created"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.TestConnection(); err != nil {
		t.Fatalf("TestConnection should auto-create base path, got %v", err)
	}
	// Confirm the directory now exists on the server filesystem.
	if _, err := os.Stat(filepath.Join(root, "auto-created")); err != nil {
		t.Errorf("base path was not created: %v", err)
	}
}

// TestWebDAVTestConnection_AuthFailure ensures auth errors propagate.
// We point the adapter at a server that returns 401 to every PROPFIND.
func TestWebDAVTestConnection_AuthFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
		http.Error(w, "auth required", http.StatusUnauthorized)
	}))
	defer server.Close()

	a, err := NewWebDAVAdapter(WebDAVConfig{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.TestConnection(); err == nil {
		t.Fatal("expected error on 401, got nil")
	}
}

// TestWebDAVTestConnection_ServerError exercises the non-404 error
// branch of TestConnection (500 from the server must propagate
// unchanged so the operator sees the real failure).
func TestWebDAVTestConnection_ServerError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	a, err := NewWebDAVAdapter(WebDAVConfig{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.TestConnection(); err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}

// ---- Delete + Read error branches -----------------------------------

// TestWebDAVDelete_NotFoundIsIdempotent ensures Delete returns nil
// when the target doesn't exist (the WebDAV server returns 404, which
// the adapter swallows so re-runs are safe).
func TestWebDAVDelete_NotFoundIsIdempotent(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{})
	defer cleanup()

	if err := a.Delete("missing/file.bin"); err != nil {
		t.Errorf("Delete on missing file should be nil, got %v", err)
	}
}

// TestWebDAVRead_NotFoundReturnsError exercises the non-chunked Read
// failure path (no manifest, ReadStream fails with 404). The wrapping
// is what we're asserting (wrapped error, not raw gowebdav error).
func TestWebDAVRead_NotFoundReturnsError(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{})
	defer cleanup()

	if _, err := a.Read("missing/file.bin"); err == nil {
		t.Fatal("expected error from Read of missing file")
	}
}

// TestWebDAVReadRange_RejectsNegativeArgs covers the input-validation
// branch at the top of ReadRange.
func TestWebDAVReadRange_RejectsNegativeArgs(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{})
	defer cleanup()
	if _, err := a.ReadRange("x", -1, 10); err == nil {
		t.Error("negative offset should be rejected")
	}
	if _, err := a.ReadRange("x", 0, -1); err == nil {
		t.Error("negative length should be rejected")
	}
}

// TestWebDAVReadRange_ZeroLengthReturnsEmpty hits the early-return for
// length == 0 — no request is issued.
func TestWebDAVReadRange_ZeroLengthReturnsEmpty(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{})
	defer cleanup()
	rc, err := a.ReadRange("anything", 0, 0)
	if err != nil {
		t.Fatalf("ReadRange(0,0): %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("zero-length range returned %d bytes, want 0", len(got))
	}
	_ = rc.Close()
}

// TestWebDAVStat_NotFoundReturnsError covers the non-chunked Stat
// failure path (no manifest sidecar exists, server returns 404).
func TestWebDAVStat_NotFoundReturnsError(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{})
	defer cleanup()
	if _, err := a.Stat("nope/missing.bin"); err == nil {
		t.Fatal("expected error from Stat of missing file")
	}
}

// TestWebDAVDelete_BlocksTraversal covers the fullPath traversal check
// reached via Delete (mirror of fullPath rejection on the other methods).
func TestWebDAVDelete_BlocksTraversal(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{})
	defer cleanup()
	if err := a.Delete("../escape"); err == nil {
		t.Error("traversal should be rejected by Delete")
	}
}

// TestWebDAVList_BlocksTraversal — same for List.
func TestWebDAVList_BlocksTraversal(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{})
	defer cleanup()
	if _, err := a.List("../escape"); err == nil {
		t.Error("traversal should be rejected by List")
	}
}

// TestWebDAVStat_BlocksTraversal — same for Stat.
func TestWebDAVStat_BlocksTraversal(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{})
	defer cleanup()
	if _, err := a.Stat("../escape"); err == nil {
		t.Error("traversal should be rejected by Stat")
	}
}

// TestWebDAVWrite_BlocksTraversal — same for Write.
func TestWebDAVWrite_BlocksTraversal(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newTestWebDAVAdapter(t, WebDAVConfig{})
	defer cleanup()
	if err := a.Write("../escape", strings.NewReader("data")); err == nil {
		t.Error("traversal should be rejected by Write")
	}
}
