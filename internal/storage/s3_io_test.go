package storage

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- stripPrefix (pure helper) --------------------------------------

func TestStripPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		key    string
		basePf string
		want   string
	}{
		{"empty basePf", "a/b.txt", "", "a/b.txt"},
		{"empty basePf with leading slash", "/a/b.txt", "", "a/b.txt"},
		{"basePf prefix match", "vault/a/b.txt", "vault/", "a/b.txt"},
		{"basePf not prefix", "other/a", "vault/", "other/a"},
		// stripPrefix doesn't normalise leading slashes when basePf is set:
		// "/vault/a" does NOT start with "vault/", so only the leading "/"
		// is trimmed. Documenting actual behaviour.
		{"basePf with leading slash on key", "/vault/a", "vault/", "vault/a"},
		{"exact basePf", "vault/", "vault/", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := stripPrefix(tc.key, tc.basePf); got != tc.want {
				t.Errorf("stripPrefix(%q, %q) = %q, want %q", tc.key, tc.basePf, got, tc.want)
			}
		})
	}
}

// ---- ctxOp (pure helper) --------------------------------------------

func TestCtxOp_HasDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := ctxOp()
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("ctxOp returned context without deadline")
	}
	if dl.IsZero() {
		t.Fatal("ctxOp deadline is zero time")
	}
}

// ---- S3 in-memory mock ----------------------------------------------

// s3Mock is an httptest-driven, in-memory S3 fake covering the operations
// exercised by the adapter: PutObject, GetObject (incl. Range),
// HeadObject, DeleteObject, ListObjectsV2, HeadBucket, and the
// multipart upload sequence (CreateMultipartUpload + UploadPart +
// CompleteMultipartUpload).
//
// The mock is deliberately minimal: it understands path-style
// requests only (we always configure the adapter with ForcePathStyle)
// and ignores authentication entirely (the SDK still signs requests;
// the mock just doesn't verify the signature).
type s3Mock struct {
	mu      sync.Mutex
	bucket  string
	objects map[string][]byte // key -> body
	// multipart state: uploadID -> ordered part bodies
	parts map[string][][]byte
	// When getChunkSize > 0, plain GetObject responses are written in
	// chunks of that size with getChunkDelay between chunks, simulating a
	// slow stream so tests can prove reads are not deadline-bound (#164).
	getChunkSize  int
	getChunkDelay time.Duration
}

func newS3Mock(bucket string) *s3Mock {
	return &s3Mock{
		bucket:  bucket,
		objects: map[string][]byte{},
		parts:   map[string][][]byte{},
	}
}

// extractKey returns the object key from a path-style S3 URL path of
// the form "/<bucket>/<key...>". Returns "" for "/<bucket>" or
// "/<bucket>/" (bucket-level operation).
func (m *s3Mock) extractKey(p string) string {
	p = strings.TrimPrefix(p, "/")
	parts := strings.SplitN(p, "/", 2)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

func (m *s3Mock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	q := r.URL.Query()
	key := m.extractKey(r.URL.Path)

	// Multipart upload init (?uploads).
	if r.Method == http.MethodPost && q.Has("uploads") {
		uploadID := fmt.Sprintf("u%d", len(m.parts)+1)
		m.parts[uploadID] = nil
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0"?>
<InitiateMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId>
</InitiateMultipartUploadResult>`, m.bucket, key, uploadID)
		return
	}

	// Multipart upload complete (?uploadId=...).
	if r.Method == http.MethodPost && q.Get("uploadId") != "" {
		uploadID := q.Get("uploadId")
		// Stitch parts in number order. The SDK uploads them in order,
		// so the slice index matches partNumber-1.
		var body []byte
		for _, p := range m.parts[uploadID] {
			body = append(body, p...)
		}
		m.objects[key] = body
		delete(m.parts, uploadID)
		// Discard the CompleteMultipartUpload body — we already have the bytes.
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0"?>
<CompleteMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Location>http://example/%s/%s</Location>
  <Bucket>%s</Bucket><Key>%s</Key><ETag>"abc"</ETag>
</CompleteMultipartUploadResult>`, m.bucket, key, m.bucket, key)
		return
	}

	// Multipart upload part (?partNumber=N&uploadId=...).
	if r.Method == http.MethodPut && q.Get("partNumber") != "" {
		uploadID := q.Get("uploadId")
		partNum, _ := strconv.Atoi(q.Get("partNumber"))
		body, _ := io.ReadAll(r.Body)
		// Grow slice as needed.
		for len(m.parts[uploadID]) < partNum {
			m.parts[uploadID] = append(m.parts[uploadID], nil)
		}
		m.parts[uploadID][partNum-1] = body
		w.Header().Set("ETag", `"part-`+q.Get("partNumber")+`"`)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Single-shot PutObject.
	if r.Method == http.MethodPut && key != "" {
		body, _ := io.ReadAll(r.Body)
		m.objects[key] = body
		w.Header().Set("ETag", `"single"`)
		w.WriteHeader(http.StatusOK)
		return
	}

	// HEAD on a key (Stat) or on a bucket (TestConnection / HeadBucket).
	if r.Method == http.MethodHead {
		if key == "" {
			// HeadBucket: 200 means the bucket is reachable.
			w.WriteHeader(http.StatusOK)
			return
		}
		obj, ok := m.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(obj)))
		w.WriteHeader(http.StatusOK)
		return
	}

	// DELETE object.
	if r.Method == http.MethodDelete {
		delete(m.objects, key)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// ListObjectsV2.
	if r.Method == http.MethodGet && q.Get("list-type") == "2" {
		prefix := q.Get("prefix")
		delimiter := q.Get("delimiter")
		w.Header().Set("Content-Type", "application/xml")
		var sb strings.Builder
		sb.WriteString(`<?xml version="1.0"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
		sb.WriteString("<Name>")
		sb.WriteString(m.bucket)
		sb.WriteString("</Name><IsTruncated>false</IsTruncated>")
		seenPrefixes := map[string]bool{}
		for k, body := range m.objects {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			if delimiter != "" {
				rest := strings.TrimPrefix(k, prefix)
				if idx := strings.Index(rest, delimiter); idx >= 0 {
					cp := prefix + rest[:idx+len(delimiter)]
					if !seenPrefixes[cp] {
						sb.WriteString("<CommonPrefixes><Prefix>")
						sb.WriteString(cp)
						sb.WriteString("</Prefix></CommonPrefixes>")
						seenPrefixes[cp] = true
					}
					continue
				}
			}
			fmt.Fprintf(&sb, "<Contents><Key>%s</Key><Size>%d</Size></Contents>", k, len(body))
		}
		sb.WriteString("</ListBucketResult>")
		_, _ = w.Write([]byte(sb.String()))
		return
	}

	// GetObject (with optional Range).
	if r.Method == http.MethodGet && key != "" {
		obj, ok := m.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if rng := r.Header.Get("Range"); rng != "" {
			// "bytes=start-end" inclusive.
			spec := strings.TrimPrefix(rng, "bytes=")
			parts := strings.SplitN(spec, "-", 2)
			start, _ := strconv.ParseInt(parts[0], 10, 64)
			end, _ := strconv.ParseInt(parts[1], 10, 64)
			if start >= int64(len(obj)) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			if end >= int64(len(obj)) {
				end = int64(len(obj)) - 1
			}
			slice := obj[start : end+1]
			w.Header().Set("Content-Length", strconv.Itoa(len(slice)))
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(obj)))
			w.WriteHeader(http.StatusPartialContent)
			if m.getChunkSize > 0 {
				flusher, _ := w.(http.Flusher)
				for cs := 0; cs < len(slice); cs += m.getChunkSize {
					ce := cs + m.getChunkSize
					if ce > len(slice) {
						ce = len(slice)
					}
					_, _ = w.Write(slice[cs:ce])
					if flusher != nil {
						flusher.Flush()
					}
					time.Sleep(m.getChunkDelay)
				}
				return
			}
			_, _ = w.Write(slice)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(obj)))
		w.WriteHeader(http.StatusOK)
		if m.getChunkSize > 0 {
			flusher, _ := w.(http.Flusher)
			for start := 0; start < len(obj); start += m.getChunkSize {
				end := start + m.getChunkSize
				if end > len(obj) {
					end = len(obj)
				}
				_, _ = w.Write(obj[start:end])
				if flusher != nil {
					flusher.Flush()
				}
				time.Sleep(m.getChunkDelay)
			}
			return
		}
		_, _ = w.Write(obj)
		return
	}

	w.WriteHeader(http.StatusNotImplemented)
}

func newS3MockAdapter(t *testing.T) (*S3Adapter, *s3Mock, func()) {
	t.Helper()
	mock := newS3Mock("vault-bk")
	server := httptest.NewServer(mock)
	a, err := NewS3Adapter(S3Config{
		Bucket:         "vault-bk",
		Region:         "us-east-1",
		AccessKey:      "AK",
		SecretKey:      "SK",
		Endpoint:       server.URL,
		ForcePathStyle: true,
	})
	if err != nil {
		server.Close()
		t.Fatalf("NewS3Adapter: %v", err)
	}
	return a, mock, server.Close
}

// ---- Write (single PUT) ---------------------------------------------

func TestS3Write_SinglePut(t *testing.T) {
	t.Parallel()
	a, mock, cleanup := newS3MockAdapter(t)
	defer cleanup()

	payload := []byte("hello s3 single put")
	if err := a.Write("file.bin", bytes.NewReader(payload)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := mock.objects["file.bin"]; !bytes.Equal(got, payload) {
		t.Errorf("stored payload = %q, want %q", got, payload)
	}
}

// ---- Write (multipart) ----------------------------------------------

// TestS3Write_Multipart exercises the CreateMultipartUpload / UploadPart /
// CompleteMultipartUpload path. The transfer manager kicks into
// multipart mode when the body exceeds 2 * partSize (the SDK reads two
// parts before deciding to switch). We configure a 5 MiB part size
// and feed an 11 MiB body so multipart is guaranteed.
func TestS3Write_Multipart(t *testing.T) {
	a, mock, cleanup := newS3MockAdapter(t)
	defer cleanup()
	// Reduce partSize so the multipart path triggers without a huge body.
	a.partSizeBytes = 5 * 1024 * 1024 // 5 MiB
	// Re-create the uploader bound to the reduced part size.
	// (The default 64 MiB partSize would otherwise force a 130 MiB body.)
	// We bypass the public API and stitch the upload by re-running
	// NewS3Adapter with explicit PartSizeMB=5; simpler approach:
	a2, err := NewS3Adapter(S3Config{
		Bucket: "vault-bk", Region: "us-east-1",
		AccessKey: "AK", SecretKey: "SK",
		Endpoint: a.config.Endpoint, ForcePathStyle: true,
		PartSizeMB: 5,
	})
	if err != nil {
		t.Fatal(err)
	}

	payload := make([]byte, 11*1024*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := a2.Write("big.bin", bytes.NewReader(payload)); err != nil {
		t.Fatalf("Write multipart: %v", err)
	}
	got, ok := mock.objects["big.bin"]
	if !ok {
		t.Fatal("multipart object not stored")
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("multipart payload mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

// ---- Read -----------------------------------------------------------

func TestS3Read_Success(t *testing.T) {
	t.Parallel()
	a, mock, cleanup := newS3MockAdapter(t)
	defer cleanup()
	mock.objects["k.bin"] = []byte("hello world")

	rc, err := a.Read("k.bin")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("Read returned %q", got)
	}
}

func TestS3Read_NotFound(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newS3MockAdapter(t)
	defer cleanup()
	if _, err := a.Read("missing.bin"); err == nil {
		t.Fatal("expected error for missing key")
	}
}

// TestCtxStream_NoDeadline pins the #164 contract: the context used for
// streamed response bodies must carry no deadline (a large object may
// legitimately stream for longer than any fixed timer), and cancel must
// still release it.
func TestCtxStream_NoDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := ctxStream()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("ctxStream returned a context WITH a deadline — streamed reads would abort mid-stream (#164)")
	}
	cancel()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("cancel did not close the stream context")
	}
}

// TestS3Read_SlowChunkedStreamCompletes regression-guards #164: a body that
// arrives slowly in many chunks must stream to completion. Before the fix,
// the shared 5-minute op deadline governed the body read and a slow stream
// died with context.DeadlineExceeded (reproduced here in miniature — any
// deadline shorter than the total stream time fails this shape of read).
func TestS3Read_SlowChunkedStreamCompletes(t *testing.T) {
	t.Parallel()
	a, mock, cleanup := newS3MockAdapter(t)
	defer cleanup()

	payload := bytes.Repeat([]byte("0123456789abcdef"), 512) // 8 KiB
	mock.objects["slow.bin"] = payload
	mock.getChunkSize = 512
	mock.getChunkDelay = 20 * time.Millisecond // 16 chunks ≈ 320 ms total

	// Both streaming methods must ride the deadline-free stream context; the
	// helper contract itself is pinned by TestCtxStream_NoDeadline.
	t.Run("Read", func(t *testing.T) {
		rc, err := a.Read("slow.bin")
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		got, err := io.ReadAll(rc)
		closeErr := rc.Close()
		if err != nil {
			t.Fatalf("slow stream aborted mid-read: %v", err)
		}
		if closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("slow stream returned %d bytes, want %d", len(got), len(payload))
		}
	})

	t.Run("ReadRange", func(t *testing.T) {
		rc, err := a.ReadRange("slow.bin", 0, int64(len(payload)))
		if err != nil {
			t.Fatalf("ReadRange: %v", err)
		}
		got, err := io.ReadAll(rc)
		closeErr := rc.Close()
		if err != nil {
			t.Fatalf("slow ranged stream aborted mid-read: %v", err)
		}
		if closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("slow ranged stream returned %d bytes, want %d", len(got), len(payload))
		}
	})
}

func TestS3Read_TraversalRejected(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newS3MockAdapter(t)
	defer cleanup()
	if _, err := a.Read("../escape"); err == nil {
		t.Error("expected traversal to be rejected")
	}
}

// ---- ReadRange ------------------------------------------------------

func TestS3ReadRange_Slice(t *testing.T) {
	t.Parallel()
	a, mock, cleanup := newS3MockAdapter(t)
	defer cleanup()
	mock.objects["k.bin"] = []byte("the quick brown fox")

	rc, err := a.ReadRange("k.bin", 4, 5)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "quick" {
		t.Errorf("ReadRange got %q, want %q", got, "quick")
	}
}

func TestS3ReadRange_ZeroLengthShortCircuit(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newS3MockAdapter(t)
	defer cleanup()
	rc, err := a.ReadRange("anything", 0, 0)
	if err != nil {
		t.Fatalf("ReadRange(0,0): %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if len(got) != 0 {
		t.Errorf("expected empty, got %d bytes", len(got))
	}
}

func TestS3ReadRange_RejectsNegativeArgs(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newS3MockAdapter(t)
	defer cleanup()
	if _, err := a.ReadRange("k", -1, 10); err == nil {
		t.Error("negative offset must be rejected")
	}
	if _, err := a.ReadRange("k", 0, -1); err == nil {
		t.Error("negative length must be rejected")
	}
}

func TestS3ReadRange_NotFound(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newS3MockAdapter(t)
	defer cleanup()
	if _, err := a.ReadRange("nope", 0, 5); err == nil {
		t.Fatal("expected error for missing key")
	}
}

// ---- s3ReadCloser.Close (cancel propagation) ------------------------

// TestS3ReadCloser_CloseCancelsContext confirms that closing the body
// invokes the deferred cancel func attached at Read time.
func TestS3ReadCloser_CloseCancelsContext(t *testing.T) {
	t.Parallel()
	a, mock, cleanup := newS3MockAdapter(t)
	defer cleanup()
	mock.objects["k.bin"] = []byte("xyz")

	rc, err := a.Read("k.bin")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// We can't directly observe the cancel func, but we can confirm
	// Close returns nil and does not panic. The cancel func is also
	// exercised by the GET-with-Range path.
	if err := rc.Close(); err != nil {
		t.Errorf("Close returned %v", err)
	}
}

// ---- Delete ---------------------------------------------------------

func TestS3Delete_Success(t *testing.T) {
	t.Parallel()
	a, mock, cleanup := newS3MockAdapter(t)
	defer cleanup()
	mock.objects["k.bin"] = []byte("data")
	if err := a.Delete("k.bin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := mock.objects["k.bin"]; ok {
		t.Error("key still present after Delete")
	}
}

func TestS3Delete_TraversalRejected(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newS3MockAdapter(t)
	defer cleanup()
	if err := a.Delete("../escape"); err == nil {
		t.Error("expected traversal to be rejected")
	}
}

// ---- List -----------------------------------------------------------

func TestS3List_FilesAndDirs(t *testing.T) {
	t.Parallel()
	a, mock, cleanup := newS3MockAdapter(t)
	defer cleanup()
	// Three top-level keys: two as files under "job/", one as a file
	// inside "job/sub/" so that ListObjectsV2 with delimiter="/" emits
	// a CommonPrefix for "job/sub/".
	mock.objects["job/a.bin"] = []byte("a")
	mock.objects["job/b.bin"] = []byte("bb")
	mock.objects["job/sub/c.bin"] = []byte("ccc")

	entries, err := a.List("job")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var (
		sawDir   bool
		sawFileA bool
		sawFileB bool
	)
	for _, e := range entries {
		if e.IsDir && (e.Path == "job/sub" || e.Path == "job/sub/") {
			sawDir = true
		}
		if e.Path == "job/a.bin" {
			sawFileA = true
		}
		if e.Path == "job/b.bin" {
			sawFileB = true
		}
	}
	if !sawDir {
		t.Errorf("expected CommonPrefix for job/sub, got %+v", entries)
	}
	if !sawFileA || !sawFileB {
		t.Errorf("missing files in list: %+v", entries)
	}
}

// ---- Stat -----------------------------------------------------------

func TestS3Stat_Success(t *testing.T) {
	t.Parallel()
	a, mock, cleanup := newS3MockAdapter(t)
	defer cleanup()
	mock.objects["k.bin"] = []byte("123456")

	info, err := a.Stat("k.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size != 6 {
		t.Errorf("Stat.Size = %d, want 6", info.Size)
	}
	if info.IsDir {
		t.Errorf("Stat.IsDir should be false")
	}
}

func TestS3Stat_NotFound(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newS3MockAdapter(t)
	defer cleanup()
	if _, err := a.Stat("missing.bin"); err == nil {
		t.Fatal("expected error for missing key")
	}
}

// ---- TestConnection -------------------------------------------------

func TestS3TestConnection_Success(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newS3MockAdapter(t)
	defer cleanup()
	if err := a.TestConnection(); err != nil {
		t.Errorf("TestConnection on mock: %v", err)
	}
}

func TestS3TestConnection_NotFound(t *testing.T) {
	t.Parallel()
	// Server that returns 404 NotFound for HeadBucket — wraps as types.NotFound.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	a, err := NewS3Adapter(S3Config{
		Bucket: "nope", Region: "us-east-1",
		AccessKey: "AK", SecretKey: "SK",
		Endpoint: server.URL, ForcePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.TestConnection(); err == nil {
		t.Fatal("expected NotFound error")
	}
}

func TestS3TestConnection_AuthFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	a, err := NewS3Adapter(S3Config{
		Bucket: "b", Region: "us-east-1",
		AccessKey: "AK", SecretKey: "SK",
		Endpoint: server.URL, ForcePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.TestConnection(); err == nil {
		t.Fatal("expected error on 403")
	}
}

// ---- Write input-validation propagation -----------------------------

func TestS3Write_TraversalRejected(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newS3MockAdapter(t)
	defer cleanup()
	if err := a.Write("../escape", bytes.NewReader([]byte("x"))); err == nil {
		t.Error("expected traversal to be rejected")
	}
}

func TestS3Stat_TraversalRejected(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newS3MockAdapter(t)
	defer cleanup()
	if _, err := a.Stat("../escape"); err == nil {
		t.Error("expected traversal to be rejected by Stat")
	}
}

func TestS3List_TraversalRejected(t *testing.T) {
	t.Parallel()
	a, _, cleanup := newS3MockAdapter(t)
	defer cleanup()
	if _, err := a.List("../escape"); err == nil {
		t.Error("expected traversal to be rejected by List")
	}
}

// TestS3ReadCloser_CloseIsIdempotent — calling Close twice must not
// panic and the second call's error must be benign (the underlying
// body.Close may report "already closed" but that's surfaced verbatim;
// the cancel func tolerates re-invocation).
func TestS3ReadCloser_CloseIsIdempotent(t *testing.T) {
	t.Parallel()
	a, mock, cleanup := newS3MockAdapter(t)
	defer cleanup()
	mock.objects["k.bin"] = []byte("x")
	rc, err := a.Read("k.bin")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	_ = rc.Close()
	// Second close: don't assert err== nil, just no panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("second Close panicked: %v", r)
		}
	}()
	_ = rc.Close()
}
