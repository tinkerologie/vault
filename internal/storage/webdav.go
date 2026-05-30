package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ruaan-deysel/vault/internal/safepath"
	"github.com/studio-b12/gowebdav"
)

// WebDAVConfig holds configuration for a WebDAV storage adapter.
//
// WebDAV is a stateless HTTP-based protocol: each operation opens, authenticates,
// transfers, and closes its own connection rather than holding a persistent
// session. This avoids the per-user concurrent connection caps that affect
// SFTP/SMB on managed providers (see #83).
type WebDAVConfig struct {
	URL                string `json:"url"`                  // Full server endpoint, e.g. "https://webdav.example.com/"
	Username           string `json:"username"`             // Optional; some servers allow anonymous access.
	Password           string `json:"password"`             // Optional.
	BasePath           string `json:"base_path"`            // Optional sub-directory under URL.
	InsecureSkipVerify bool   `json:"insecure_skip_verify"` // Skip TLS cert validation (self-signed certs).

	// TimeoutSeconds is an optional overall request lifetime cap. It maps to
	// http.Client.Timeout and includes connect + TLS + headers + body
	// transfer. The default of 0 means **unlimited** so multi-GB uploads
	// over slow WAN links can complete; stuck connections are still detected
	// via the per-phase transport timeouts (dial, TLS handshake, response
	// headers) plus the stall watchdog below. Power-users can set this for
	// a hard ceiling per request. (closes #83 comment)
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`

	// StallTimeoutSeconds aborts an in-flight upload if no bytes flow for
	// this many seconds. Default is 300 (5 minutes). Set to a negative
	// value to disable. This is the primary safety net against hung TCP
	// connections that the OS keepalive has not yet torn down.
	StallTimeoutSeconds int `json:"stall_timeout_seconds,omitempty"`

	// ChunkSizeMB controls WebDAV chunked logical-file uploads. The default of
	// 0 uses 50 MiB chunks. Negative values disable chunking, but WebDAV still
	// writes through a temporary file and WriteStreamWithLength to avoid
	// gowebdav.WriteStream buffering non-seekable readers into memory.
	ChunkSizeMB int `json:"chunk_size_mb,omitempty"`
}

const (
	defaultWebDAVChunkSizeBytes = int64(50 * 1024 * 1024)
	maxWebDAVChunkSizeMB        = 4096
	webDAVManifestVersion       = 1
	webDAVSidecarPrefix         = ".vault-webdav-"
	webDAVManifestSuffix        = ".manifest.json"
)

// webDAVChunkRetryBackoffs holds the per-chunk PUT retry backoff schedule.
// The values are exponential (factor ~3) capped at 8s. Trimmed for Vault's
// blast radius: each chunk is at most a few hundred MiB, and Vault has its
// own job-level retry loop wrapped around the adapter (runner.uploadOnce,
// 4 attempts). Exposed as var so tests can override to keep total runtime
// small.
var webDAVChunkRetryBackoffs = []time.Duration{
	100 * time.Millisecond,
	300 * time.Millisecond,
	1 * time.Second,
	3 * time.Second,
	8 * time.Second,
}

// httpErrorCode extracts the numeric HTTP status code from a gowebdav error.
// gowebdav wraps non-2xx responses in *os.PathError whose Err text starts
// with the status code as a decimal integer (e.g. "423 Locked").
// Returns 0 when the error is not from gowebdav (network/dial/timeout).
func httpErrorCode(err error) int {
	var pe *os.PathError
	if !errors.As(err, &pe) || pe.Err == nil {
		return 0
	}
	parts := strings.SplitN(pe.Err.Error(), " ", 2)
	code, convErr := strconv.Atoi(parts[0])
	if convErr != nil {
		return 0
	}
	return code
}

// isWebDAVRetriable classifies a transfer error as retriable.
//
//   - HTTP 423 (Locked), 409 (Conflict), 429 (Too Many Requests) and any
//     5xx are retriable: the request was understood by the server but
//     could not be processed at this moment.
//   - Stall-watchdog aborts (ErrUploadStalled) are retriable: the next
//     attempt opens a fresh TCP connection.
//   - All 4xx other than the three above are fatal (auth, bad path,
//     payload too large). Retrying just wastes time and bandwidth.
//   - Errors without a status (network, dial, TLS, EOF) are retriable.
func isWebDAVRetriable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUploadStalled) {
		return true
	}
	code := httpErrorCode(err)
	switch code {
	case 0:
		return true // network / transport / non-HTTP failure
	case http.StatusLocked, http.StatusConflict, http.StatusTooManyRequests:
		return true
	}
	return code >= http.StatusInternalServerError
}

type webDAVChunkManifest struct {
	Version   int                `json:"version"`
	Path      string             `json:"path"`
	Size      int64              `json:"size"`
	ChunkSize int64              `json:"chunk_size"`
	Chunks    []webDAVChunkEntry `json:"chunks"`
}

type webDAVChunkEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// String redacts the Password field so accidental logging via fmt verbs
// (%v, %+v, %s) does not leak credentials. The API layer already redacts
// password fields before sending configs to the UI (see handlers.redactConfig);
// this Stringer adds a defence-in-depth layer for log statements.
func (c WebDAVConfig) String() string {
	pw := ""
	if c.Password != "" {
		pw = "<redacted>"
	}
	return fmt.Sprintf("WebDAVConfig{URL:%q Username:%q Password:%s BasePath:%q InsecureSkipVerify:%t TimeoutSeconds:%d StallTimeoutSeconds:%d ChunkSizeMB:%d}",
		c.URL, c.Username, pw, c.BasePath, c.InsecureSkipVerify, c.TimeoutSeconds, c.StallTimeoutSeconds, c.ChunkSizeMB)
}

// MarshalJSON ensures structured loggers (e.g. encoding/json wrappers) cannot
// emit the plaintext password. Persistence does not flow through this method:
// the storage handlers store the request body's JSON string directly into
// the DB Config column, so adding MarshalJSON only affects log/debug paths
// that explicitly marshal a WebDAVConfig value.
func (c WebDAVConfig) MarshalJSON() ([]byte, error) {
	type alias WebDAVConfig // avoid recursion into MarshalJSON
	redacted := alias(c)
	if redacted.Password != "" {
		redacted.Password = "<redacted>"
	}
	return json.Marshal(redacted)
}

// WebDAVAdapter implements Adapter against a WebDAV endpoint.
//
// The adapter creates a fresh gowebdav client per operation rather than
// reusing one. gowebdav's auto-auth probes server capabilities on the first
// request, so reusing a client across goroutines is not strictly safe; each
// operation also makes very few requests, so the cost is negligible.
type WebDAVAdapter struct {
	config WebDAVConfig
}

// NewWebDAVAdapter validates the config and returns an adapter.
func NewWebDAVAdapter(cfg WebDAVConfig) (*WebDAVAdapter, error) {
	url := strings.TrimSpace(cfg.URL)
	if url == "" {
		return nil, fmt.Errorf("webdav: url is required")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("webdav: url must start with http:// or https://")
	}
	if cfg.ChunkSizeMB > maxWebDAVChunkSizeMB {
		return nil, fmt.Errorf("webdav: chunk_size_mb must be <= %d, got %d", maxWebDAVChunkSizeMB, cfg.ChunkSizeMB)
	}
	cfg.URL = strings.TrimRight(url, "/")
	cfg.BasePath = strings.Trim(cfg.BasePath, "/")
	return &WebDAVAdapter{config: cfg}, nil
}

// client builds a fresh WebDAV client.
//
// The previous implementation called gowebdav.Client.SetTimeout(60s), which
// translates to http.Client.Timeout — a deadline that covers the *entire*
// request lifetime including the upload body. That made multi-GB PUTs over
// any real WAN link impossible (closes #83 comment from @SebboGit:
// "context deadline exceeded ... Client.Timeout exceeded while awaiting
// headers" on a 20-container backup to Hetzner Storage Box).
//
// We now use a custom http.Transport with **phase-specific** timeouts:
//
//   - TCP dial: 30 s (cheap, network-level)
//   - TLS handshake: 30 s
//   - Expect-Continue: 5 s
//   - Response headers: 5 min (applies AFTER body is fully sent; some servers
//     buffer the upload to disk before responding)
//   - Idle keep-alive: 90 s
//   - TCP keepalive: 30 s (OS-level dead-peer detection)
//
// We deliberately leave http.Client.Timeout at 0 (unlimited) by default,
// because the upload body transfer has no upper bound that depends on
// anything other than file size and link speed. Stuck/dead connections are
// still caught by the dial/TLS/header timeouts and by the stall watchdog
// wrapped around the upload reader in Write().
//
// Power-users can still cap total request lifetime via TimeoutSeconds.
func (w *WebDAVAdapter) client() *gowebdav.Client {
	c := gowebdav.NewAuthClient(w.config.URL, gowebdav.NewAutoAuth(w.config.Username, w.config.Password))
	c.SetTransport(w.transport())

	if w.config.TimeoutSeconds > 0 {
		c.SetTimeout(time.Duration(w.config.TimeoutSeconds) * time.Second)
	}
	c.SetHeader("Accept-Encoding", "identity")
	// else: leave http.Client.Timeout at its default 0 (no overall deadline).
	return c
}

// transport builds the http.Transport used by both the gowebdav client
// (Write/Read/List/Stat/Delete) and the raw http.Client used by ReadRange.
// Centralising the construction keeps the phase-specific timeouts, TLS
// settings, and proxy honouring in one place. See client() for the rationale
// behind the individual timeout choices.
func (w *WebDAVAdapter) transport() *http.Transport {
	t := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   30 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
	}
	if w.config.InsecureSkipVerify {
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 — opt-in flag for self-signed servers
	}
	return t
}

func (w *WebDAVAdapter) chunkSize() (int64, bool) {
	if w.config.ChunkSizeMB < 0 {
		return 0, false
	}
	if w.config.ChunkSizeMB == 0 {
		return defaultWebDAVChunkSizeBytes, true
	}
	return int64(w.config.ChunkSizeMB) * 1024 * 1024, true
}

// stallTimeout returns the configured no-progress upload abort window, or
// 5 minutes if unset. A negative value disables the watchdog entirely.
func (w *WebDAVAdapter) stallTimeout() time.Duration {
	switch {
	case w.config.StallTimeoutSeconds < 0:
		return 0
	case w.config.StallTimeoutSeconds == 0:
		return 5 * time.Minute
	default:
		return time.Duration(w.config.StallTimeoutSeconds) * time.Second
	}
}

// stallReader wraps an io.Reader and records the timestamp of every
// successful Read. A companion goroutine polls the timestamp and, if no
// bytes have flowed for `timeout`, closes a sentinel pipe that causes the
// next Read to return ErrUploadStalled. This aborts the underlying HTTP
// PUT promptly without depending on a fixed overall deadline.
type stallReader struct {
	src      io.Reader
	lastNano atomic.Int64 // unix nano of last successful Read
	timeout  time.Duration
	cancel   context.CancelFunc
	stalled  atomic.Bool
}

// ErrUploadStalled signals that an upload was aborted because no bytes
// were transferred within the configured stall timeout.
var ErrUploadStalled = errors.New("webdav: upload stalled (no progress for stall_timeout window)")

func newStallReader(src io.Reader, timeout time.Duration) *stallReader {
	ctx, cancel := context.WithCancel(context.Background())
	r := &stallReader{src: src, timeout: timeout, cancel: cancel}
	r.lastNano.Store(time.Now().UnixNano())
	if timeout > 0 {
		go r.watch(ctx)
	}
	return r
}

func (r *stallReader) watch(ctx context.Context) {
	// Poll at min(timeout/4, 30s); cheap and prompt enough.
	interval := r.timeout / 4
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	if interval < time.Second {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			last := time.Unix(0, r.lastNano.Load())
			if now.Sub(last) >= r.timeout {
				log.Printf("webdav: upload stalled — no bytes for %s, aborting", now.Sub(last).Round(time.Second))
				r.stalled.Store(true)
				return
			}
		}
	}
}

func (r *stallReader) Read(p []byte) (int, error) {
	if r.stalled.Load() {
		return 0, ErrUploadStalled
	}
	n, err := r.src.Read(p)
	if n > 0 {
		r.lastNano.Store(time.Now().UnixNano())
	}
	return n, err
}

func (r *stallReader) Close() error {
	r.cancel()
	if closer, ok := r.src.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// fullPath joins the configured base path with an operation-supplied path,
// rejecting any traversal attempts via safepath.JoinUnderBase. WebDAV uses
// "/" as its path separator regardless of host OS, so we keep the result in
// forward-slash form.
func (w *WebDAVAdapter) fullPath(p string, allowRoot bool) (string, error) {
	base := w.config.BasePath
	if base == "" {
		base = "."
	}
	joined, err := safepath.JoinUnderBase(base, p, allowRoot)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", p, err)
	}
	// Normalise to forward slashes; safepath operates on filepath which would
	// use "\\" on Windows builds.
	joined = strings.ReplaceAll(joined, "\\", "/")
	if joined == "." {
		return "/", nil
	}
	return "/" + strings.Trim(joined, "/"), nil
}

func cleanWebDAVLogicalPath(p string) string {
	return strings.Trim(strings.ReplaceAll(p, "\\", "/"), "/")
}

func webDAVSidecarStem(full string) string {
	sum := sha256.Sum256([]byte(full))
	return webDAVSidecarPrefix + path.Base(full) + "." + hex.EncodeToString(sum[:8])
}

func webDAVManifestPath(full string) string {
	return path.Join(path.Dir(full), webDAVSidecarStem(full)+webDAVManifestSuffix)
}

func webDAVChunkDir(full string) string {
	return path.Join(path.Dir(full), webDAVSidecarStem(full)+".chunks")
}

// webDAVChunkPath returns the path of the Nth chunk file inside the sidecar
// chunks directory. The .dat suffix is deliberately chosen to avoid the
// .part / .filepart filenames that Nextcloud (and other Sabre/DAV-based
// servers such as ownCloud) reserve for their own in-progress upload
// mechanism and reject with HTTP 400 via the default
// forbidden_filename_extensions / blacklisted_files_regex config. Changing
// this suffix is safe: each chunk's absolute path is stored in the
// manifest, so historical restore points continue to read correctly from
// their original .part chunks.
func webDAVChunkPath(full string, index int) string {
	return path.Join(webDAVChunkDir(full), fmt.Sprintf("%06d.dat", index))
}

func isWebDAVSidecarName(name string) bool {
	return strings.HasPrefix(name, webDAVSidecarPrefix)
}

func isWebDAVNotFound(err error) bool {
	return err != nil && gowebdav.IsErrNotFound(err)
}

func readWebDAVChunk(r io.Reader, limit int64) ([]byte, bool, error) {
	var buf bytes.Buffer
	_, err := io.CopyN(&buf, r, limit)
	if err == nil {
		return buf.Bytes(), false, nil
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return buf.Bytes(), true, nil
	}
	return nil, false, err
}

func (w *WebDAVAdapter) writeStreamWithLength(c *gowebdav.Client, full string, reader io.Reader, size int64) error {
	sr := newStallReader(reader, w.stallTimeout())
	defer sr.Close()
	if err := c.WriteStreamWithLength(full, sr, size, 0640); err != nil {
		if sr.stalled.Load() {
			return fmt.Errorf("webdav: write %s: %w", full, ErrUploadStalled)
		}
		return fmt.Errorf("webdav: write %s: %w", full, err)
	}
	return nil
}

func (w *WebDAVAdapter) writeBytesWithChunkRetry(c *gowebdav.Client, full string, data []byte) error {
	var lastErr error
	for attempt := 0; attempt <= len(webDAVChunkRetryBackoffs); attempt++ {
		if attempt > 0 {
			time.Sleep(webDAVChunkRetryBackoffs[attempt-1])
		}
		lastErr = w.writeStreamWithLength(c, full, bytes.NewReader(data), int64(len(data)))
		if lastErr == nil {
			return nil
		}
		// Status-aware fail-fast: auth/path/payload errors are never
		// transient. Retrying just hides the real problem and wastes time.
		if !isWebDAVRetriable(lastErr) {
			log.Printf("webdav: non-retriable chunk PUT error for %s: %v", full, lastErr)
			return lastErr
		}
	}
	return lastErr
}

func (w *WebDAVAdapter) readManifest(c *gowebdav.Client, full string) (webDAVChunkManifest, bool, error) {
	rc, err := c.ReadStream(webDAVManifestPath(full))
	if err != nil {
		if isWebDAVNotFound(err) {
			return webDAVChunkManifest{}, false, nil
		}
		return webDAVChunkManifest{}, false, fmt.Errorf("webdav: read chunk manifest %s: %w", webDAVManifestPath(full), err)
	}
	defer rc.Close()
	var manifest webDAVChunkManifest
	if err := json.NewDecoder(rc).Decode(&manifest); err != nil {
		return webDAVChunkManifest{}, false, fmt.Errorf("webdav: decode chunk manifest %s: %w", webDAVManifestPath(full), err)
	}
	if manifest.Version != webDAVManifestVersion {
		return webDAVChunkManifest{}, false, fmt.Errorf("webdav: unsupported chunk manifest version %d", manifest.Version)
	}
	return manifest, true, nil
}

func (w *WebDAVAdapter) writeBufferedSingle(c *gowebdav.Client, full string, data []byte) error {
	if err := w.deleteChunkSidecars(c, full); err != nil {
		return err
	}
	return w.writeStreamWithLength(c, full, bytes.NewReader(data), int64(len(data)))
}

func (w *WebDAVAdapter) writeTempSingle(c *gowebdav.Client, full string, reader io.Reader) error {
	if err := w.deleteChunkSidecars(c, full); err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "vault-webdav-upload-*")
	if err != nil {
		return fmt.Errorf("webdav: create upload temp file: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	size, err := io.Copy(tmp, reader)
	if err != nil {
		return fmt.Errorf("webdav: spool upload temp file: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("webdav: rewind upload temp file: %w", err)
	}
	return w.writeStreamWithLength(c, full, tmp, size)
}

func (w *WebDAVAdapter) writeChunked(c *gowebdav.Client, logicalPath string, full string, firstChunk []byte, reader io.Reader, chunkSize int64) error {
	if err := w.deleteChunkSidecars(c, full); err != nil {
		return err
	}
	if err := c.Remove(full); err != nil && !isWebDAVNotFound(err) {
		return fmt.Errorf("webdav: remove existing %s: %w", full, err)
	}

	manifest := webDAVChunkManifest{
		Version:   webDAVManifestVersion,
		Path:      logicalPath,
		ChunkSize: chunkSize,
	}
	uploaded := make([]string, 0)
	cleanup := func() {
		for _, chunkPath := range uploaded {
			if err := c.Remove(chunkPath); err != nil && !isWebDAVNotFound(err) {
				log.Printf("webdav: cleanup: failed to delete chunk %s: %v", chunkPath, err)
			}
		}
		if err := c.Remove(webDAVManifestPath(full)); err != nil && !isWebDAVNotFound(err) {
			log.Printf("webdav: cleanup: failed to delete chunk manifest %s: %v", webDAVManifestPath(full), err)
		}
		// Best-effort: remove the now-empty chunks collection. createParentCollection
		// inside gowebdav's WriteStreamWithLength creates this directory before the
		// first PUT, so a failed first-chunk upload (e.g. server-side filename
		// rejection) would otherwise leave a dangling empty collection on the
		// remote. Errors here are non-fatal; a stale chunk file elsewhere or
		// servers that reject DELETE on non-empty collections are tolerated.
		if err := c.Remove(webDAVChunkDir(full)); err != nil && !isWebDAVNotFound(err) {
			log.Printf("webdav: cleanup: failed to delete chunk dir %s: %v", webDAVChunkDir(full), err)
		}
	}

	writeChunk := func(data []byte) error {
		index := len(manifest.Chunks)
		chunkPath := webDAVChunkPath(full, index)
		sum := sha256.Sum256(data)
		if err := w.writeBytesWithChunkRetry(c, chunkPath, data); err != nil {
			return err
		}
		uploaded = append(uploaded, chunkPath)
		manifest.Chunks = append(manifest.Chunks, webDAVChunkEntry{
			Path:   chunkPath,
			Size:   int64(len(data)),
			SHA256: hex.EncodeToString(sum[:]),
		})
		manifest.Size += int64(len(data))
		return nil
	}

	if err := writeChunk(firstChunk); err != nil {
		cleanup()
		return err
	}
	for {
		chunk, eof, err := readWebDAVChunk(reader, chunkSize)
		if err != nil {
			cleanup()
			return fmt.Errorf("webdav: read chunk for %s: %w", full, err)
		}
		if len(chunk) > 0 {
			if err := writeChunk(chunk); err != nil {
				cleanup()
				return err
			}
		}
		if eof {
			break
		}
	}

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		cleanup()
		return fmt.Errorf("webdav: encode chunk manifest: %w", err)
	}
	if err := w.writeBytesWithChunkRetry(c, webDAVManifestPath(full), manifestBytes); err != nil {
		cleanup()
		return err
	}
	return nil
}

func (w *WebDAVAdapter) deleteChunkSidecars(c *gowebdav.Client, full string) error {
	manifest, found, err := w.readManifest(c, full)
	if err != nil {
		return err
	}
	if found {
		for _, chunk := range manifest.Chunks {
			if err := c.Remove(chunk.Path); err != nil && !isWebDAVNotFound(err) {
				return fmt.Errorf("webdav: delete chunk %s: %w", chunk.Path, err)
			}
		}
	}
	if err := c.Remove(webDAVManifestPath(full)); err != nil && !isWebDAVNotFound(err) {
		return fmt.Errorf("webdav: delete chunk manifest %s: %w", webDAVManifestPath(full), err)
	}
	return nil
}

func (w *WebDAVAdapter) Write(p string, reader io.Reader) error {
	full, err := w.fullPath(p, false)
	if err != nil {
		return err
	}
	c := w.client()
	if dir := path.Dir(full); dir != "/" && dir != "." {
		if err := c.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("webdav: mkdir %s: %w", dir, err)
		}
	}
	chunkSize, chunkingEnabled := w.chunkSize()
	if !chunkingEnabled {
		return w.writeTempSingle(c, full, reader)
	}
	firstChunk, eof, err := readWebDAVChunk(reader, chunkSize)
	if err != nil {
		return fmt.Errorf("webdav: read upload %s: %w", full, err)
	}
	if eof {
		return w.writeBufferedSingle(c, full, firstChunk)
	}
	return w.writeChunked(c, cleanWebDAVLogicalPath(p), full, firstChunk, reader, chunkSize)
}

func (w *WebDAVAdapter) Read(p string) (io.ReadCloser, error) {
	full, err := w.fullPath(p, false)
	if err != nil {
		return nil, err
	}
	c := w.client()
	manifest, found, err := w.readManifest(c, full)
	if err != nil {
		return nil, err
	}
	if found {
		return &webDAVChunkReader{client: c, manifest: manifest}, nil
	}
	rc, err := c.ReadStream(full)
	if err != nil {
		return nil, fmt.Errorf("webdav: read %s: %w", full, err)
	}
	return rc, nil
}

// ReadRange fetches a half-open byte slice [offset, offset+length) of a
// remote object via an HTTP Range request. gowebdav does not expose Range,
// so we build the request by hand using the same transport as client().
//
// Servers that honour Range respond 206 Partial Content; servers that
// silently ignore it return 200 OK with the full body, which we slice
// client-side so the caller still gets the requested window. Chunked
// (sidecar manifest) uploads are NOT supported here: dedup-managed objects
// are pack files written as single PUTs, and the chunked manifest format
// is invisible to Range reads. Detecting a manifest at ReadRange time
// would defeat the purpose of avoiding a full download.
func (w *WebDAVAdapter) ReadRange(p string, offset, length int64) (io.ReadCloser, error) {
	if offset < 0 || length < 0 {
		return nil, fmt.Errorf("webdav: invalid range offset=%d length=%d", offset, length)
	}
	full, err := w.fullPath(p, false)
	if err != nil {
		return nil, err
	}
	if length == 0 {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}

	target := strings.TrimRight(w.config.URL, "/") + full
	req, err := http.NewRequest(http.MethodGet, target, nil) // #nosec G107 //nolint:gosec // w.config.URL is the admin-configured WebDAV destination URL — not user input
	if err != nil {
		return nil, fmt.Errorf("webdav: build range request: %w", err)
	}
	if w.config.Username != "" || w.config.Password != "" {
		req.SetBasicAuth(w.config.Username, w.config.Password)
	}
	// HTTP Range is inclusive on both ends.
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	req.Header.Set("Accept-Encoding", "identity")

	httpClient := &http.Client{Transport: w.transport()}
	if w.config.TimeoutSeconds > 0 {
		httpClient.Timeout = time.Duration(w.config.TimeoutSeconds) * time.Second
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("webdav: range get %s: %w", full, err)
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		// Server honoured Range. Cap reads at length in case it sent more.
		return &rangeReader{Reader: io.LimitReader(resp.Body, length), closer: resp.Body}, nil
	case http.StatusOK:
		// Server ignored Range and returned the full body. Skip the leading
		// bytes and slice to length so the caller gets the requested window.
		if _, err := io.CopyN(io.Discard, resp.Body, offset); err != nil {
			_ = resp.Body.Close()
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("webdav: offset %d at or past EOF", offset)
			}
			return nil, fmt.Errorf("webdav: skip to offset %d: %w", offset, err)
		}
		return &rangeReader{Reader: io.LimitReader(resp.Body, length), closer: resp.Body}, nil
	case http.StatusRequestedRangeNotSatisfiable:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("webdav: range not satisfiable for %s [%d-%d]", full, offset, offset+length-1)
	default:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("webdav: unexpected status %d for %s range request", resp.StatusCode, full)
	}
}

func (w *WebDAVAdapter) Delete(p string) error {
	full, err := w.fullPath(p, false)
	if err != nil {
		return err
	}
	c := w.client()
	if err := c.Remove(full); err != nil && !isWebDAVNotFound(err) {
		return fmt.Errorf("webdav: delete %s: %w", full, err)
	}
	if err := w.deleteChunkSidecars(c, full); err != nil {
		return err
	}
	return nil
}

func (w *WebDAVAdapter) List(prefix string) ([]FileInfo, error) {
	full, err := w.fullPath(prefix, true)
	if err != nil {
		return nil, err
	}
	c := w.client()
	entries, err := c.ReadDir(full)
	if err != nil {
		return nil, fmt.Errorf("webdav: list %s: %w", full, err)
	}
	out := make([]FileInfo, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if isWebDAVSidecarName(e.Name()) {
			if !e.IsDir() && strings.HasSuffix(e.Name(), webDAVManifestSuffix) {
				manifestPath := path.Join(full, e.Name())
				rc, readErr := c.ReadStream(manifestPath)
				if readErr != nil {
					return nil, fmt.Errorf("webdav: read chunk manifest %s: %w", manifestPath, readErr)
				}
				var manifest webDAVChunkManifest
				decodeErr := json.NewDecoder(rc).Decode(&manifest)
				_ = rc.Close()
				if decodeErr != nil {
					return nil, fmt.Errorf("webdav: decode chunk manifest %s: %w", manifestPath, decodeErr)
				}
				if manifest.Version == webDAVManifestVersion && manifest.Path != "" {
					out = append(out, FileInfo{Path: manifest.Path, Size: manifest.Size, ModTime: e.ModTime(), IsDir: false})
					seen[manifest.Path] = struct{}{}
				}
			}
			continue
		}
		relPath := path.Join(prefix, e.Name())
		if _, ok := seen[relPath]; ok {
			continue
		}
		out = append(out, FileInfo{
			Path:    relPath,
			Size:    e.Size(),
			ModTime: e.ModTime(),
			IsDir:   e.IsDir(),
		})
	}
	return out, nil
}

func (w *WebDAVAdapter) Stat(p string) (FileInfo, error) {
	full, err := w.fullPath(p, false)
	if err != nil {
		return FileInfo{}, err
	}
	c := w.client()
	manifest, found, err := w.readManifest(c, full)
	if err != nil {
		return FileInfo{}, err
	}
	if found {
		return FileInfo{Path: p, Size: manifest.Size, IsDir: false}, nil
	}
	info, err := c.Stat(full)
	if err != nil {
		return FileInfo{}, fmt.Errorf("webdav: stat %s: %w", full, err)
	}
	return FileInfo{
		Path:    p,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, nil
}

// TestConnection verifies the configured endpoint is reachable and the base
// path is readable. If the base path is missing (404) we attempt to create
// it; any other error (auth, 5xx, network) is surfaced unchanged so callers
// can see the real failure rather than a misleading mkdir error.
func (w *WebDAVAdapter) TestConnection() error {
	c := w.client()
	target := "/"
	if w.config.BasePath != "" {
		target = "/" + strings.Trim(w.config.BasePath, "/")
	}
	if _, err := c.ReadDir(target); err == nil {
		return nil
	} else if !gowebdav.IsErrNotFound(err) {
		return fmt.Errorf("webdav: connection test failed: %w", err)
	}
	// Base path missing — attempt to create it. This both proves write
	// access and avoids forcing the operator to pre-create the directory.
	if err := c.MkdirAll(target, 0750); err != nil {
		return fmt.Errorf("webdav: connection test failed: %w", err)
	}
	return nil
}

type webDAVChunkReader struct {
	client      *gowebdav.Client
	manifest    webDAVChunkManifest
	index       int
	current     io.ReadCloser
	currentHash hash.Hash
	currentSize int64
}

func (r *webDAVChunkReader) Read(p []byte) (int, error) {
	for {
		if r.current == nil {
			if r.index >= len(r.manifest.Chunks) {
				return 0, io.EOF
			}
			rc, err := r.client.ReadStream(r.manifest.Chunks[r.index].Path)
			if err != nil {
				return 0, fmt.Errorf("webdav: read chunk %s: %w", r.manifest.Chunks[r.index].Path, err)
			}
			r.current = rc
			r.currentHash = sha256.New()
			r.currentSize = 0
		}

		n, err := r.current.Read(p)
		if n > 0 {
			_, _ = r.currentHash.Write(p[:n])
			r.currentSize += int64(n)
			return n, nil
		}
		if errors.Is(err, io.EOF) {
			if closeErr := r.current.Close(); closeErr != nil {
				return 0, fmt.Errorf("webdav: close chunk %s: %w", r.manifest.Chunks[r.index].Path, closeErr)
			}
			chunk := r.manifest.Chunks[r.index]
			if r.currentSize != chunk.Size {
				return 0, fmt.Errorf("webdav: chunk %s size mismatch: got %d, want %d", chunk.Path, r.currentSize, chunk.Size)
			}
			actual := hex.EncodeToString(r.currentHash.Sum(nil))
			if actual != chunk.SHA256 {
				return 0, fmt.Errorf("webdav: chunk %s checksum mismatch: got %s, want %s", chunk.Path, actual, chunk.SHA256)
			}
			r.current = nil
			r.index++
			continue
		}
		if err != nil {
			return 0, err
		}
		return 0, nil
	}
}

func (r *webDAVChunkReader) Close() error {
	if r.current != nil {
		return r.current.Close()
	}
	return nil
}

const webdavQuotaPropfindBody = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:quota-available-bytes/>
    <d:quota-used-bytes/>
  </d:prop>
</d:propfind>`

// GetCapacity issues a Depth:0 PROPFIND requesting the RFC 4331 quota
// properties. Nextcloud, ownCloud, and any Sabre/DAV-based server
// supports these. Servers that don't (apache mod_dav default, some
// hand-rolled servers) return a 207 multistatus with absent or "404"
// property statuses; we return a zero-Total Capacity with Source set
// so consumers can render the "no quota reported" path without
// treating the destination as failed.
//
// Network / transport errors and non-207/200 HTTP statuses DO surface
// as errors so support reports show real connectivity problems.
func (w *WebDAVAdapter) GetCapacity(ctx context.Context) (Capacity, error) {
	if err := ctx.Err(); err != nil {
		return Capacity{}, err
	}
	target := strings.TrimRight(w.config.URL, "/")
	if bp := strings.Trim(w.config.BasePath, "/"); bp != "" {
		target += "/" + bp
	}
	target += "/"
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", target, strings.NewReader(webdavQuotaPropfindBody)) // #nosec G107 — URL is admin-configured
	if err != nil {
		return Capacity{}, fmt.Errorf("webdav: build propfind: %w", err)
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	if w.config.Username != "" || w.config.Password != "" {
		req.SetBasicAuth(w.config.Username, w.config.Password)
	}
	httpClient := &http.Client{Transport: w.transport()}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Capacity{}, fmt.Errorf("webdav: propfind: %w", err)
	}
	defer resp.Body.Close()

	probedAt := time.Now().UTC()
	// Servers that don't implement the quota props commonly return one
	// of these codes. Treat them as "no quota reported" and return a
	// zero-Total Capacity rather than a hard error.
	if resp.StatusCode == http.StatusNotImplemented ||
		resp.StatusCode == http.StatusMethodNotAllowed {
		return Capacity{ProbedAt: probedAt, Source: "webdav-quota"}, nil
	}
	if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode != http.StatusOK {
		return Capacity{}, fmt.Errorf("webdav: propfind status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Capacity{}, fmt.Errorf("webdav: read propfind body: %w", err)
	}
	used, available, ok := parseWebDAVQuotaProps(body)
	if !ok {
		// Server returned 207 multistatus but didn't carry both quota
		// props (or wrapped them in a 404 propstat). Same fallback as
		// the 501/405 case.
		return Capacity{ProbedAt: probedAt, Source: "webdav-quota"}, nil
	}
	// Nextcloud sentinel values for non-RFC-4331 cases:
	//   -1 = unlimited (admin has set no per-user quota)
	//   -2 = "unknown" (the value cannot currently be determined)
	//   -3 = "unknown" (the user is shared and no quota applies)
	// In all three cases we still know the real used-bytes count, so
	// emit Capacity{TotalBytes:0, UsedBytes:used} — the UI's "used-only"
	// path renders that as "Used: 565 MB (no quota)" rather than hiding
	// the number entirely. RFC 4331 says quota-available-bytes MUST be
	// a non-negative integer, so any negative value is a Nextcloud
	// extension; treat them all as "no quota set".
	if available < 0 {
		return Capacity{
			UsedBytes: used,
			ProbedAt:  probedAt,
			Source:    "webdav-quota",
		}, nil
	}
	return Capacity{
		TotalBytes: used + available,
		UsedBytes:  used,
		FreeBytes:  available,
		ProbedAt:   probedAt,
		Source:     "webdav-quota",
	}, nil
}

// parseWebDAVQuotaProps extracts <d:quota-used-bytes> and
// <d:quota-available-bytes> from a PROPFIND multistatus body. Returns
// (used, available, true) on success or (0, 0, false) when either is
// missing or unparsable. Tolerant of mixed namespace prefixes
// (Nextcloud emits "d:", some servers use "D:" or no prefix at all)
// by matching on the local element name only.
func parseWebDAVQuotaProps(body []byte) (used int64, available int64, ok bool) {
	used, uok := extractWebDAVPropInt(body, "quota-used-bytes")
	available, aok := extractWebDAVPropInt(body, "quota-available-bytes")
	return used, available, uok && aok
}

// extractWebDAVPropInt finds the first matching property element by
// local name (ignoring any namespace prefix) and returns its integer
// contents. Negative values are accepted because Nextcloud uses them
// as sentinels for "no quota" (-1 unlimited, -2 / -3 unknown); the
// caller decides how to interpret a negative result. Returns
// (0, false) if the element is missing, contains non-digit characters
// other than a leading minus, or the propstat surrounding it carries
// a non-200 status
// (which servers use to say "this property isn't supported").
//
// The regex is bounded ("\d+") and the element name is QuoteMeta'd,
// so the overall pattern is safe to compile on every call. Avoids
// pulling in an XML parser for a one-shot two-element extraction.
func extractWebDAVPropInt(body []byte, localName string) (int64, bool) {
	re := regexp.MustCompile(`<[^>]*` + regexp.QuoteMeta(localName) + `[^>]*>\s*(-?\d+)\s*<`)
	m := re.FindSubmatch(body)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Usage queries the WebDAV server's RFC 4331 quota properties via PROPFIND.
// Servers that support quota-available-bytes and quota-used-bytes return real
// free/total values. Servers that don't (apache mod_dav default, generic
// WebDAV servers) return ErrUsageNotSupported.
//
// Nextcloud servers that have set unlimited quota (-1) or report "unknown"
// (-2, -3) also return ErrUsageNotSupported since no useful free/total can
// be derived.
func (w *WebDAVAdapter) Usage() (free, total int64, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cap, err := w.GetCapacity(ctx)
	if err != nil {
		// Intentional graceful degradation: all GetCapacity errors (including
		// transient network/auth failures) are mapped to ErrUsageNotSupported.
		// Callers cannot distinguish transient from permanent unavailability.
		return 0, 0, ErrUsageNotSupported
	}
	// TotalBytes == 0 means the server didn't report quota (or it's unlimited).
	if cap.TotalBytes == 0 {
		return 0, 0, ErrUsageNotSupported
	}
	return cap.FreeBytes, cap.TotalBytes, nil
}

var _ Adapter = (*WebDAVAdapter)(nil)
