package storage

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"path"
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
	return fmt.Sprintf("WebDAVConfig{URL:%q Username:%q Password:%s BasePath:%q InsecureSkipVerify:%t TimeoutSeconds:%d StallTimeoutSeconds:%d}",
		c.URL, c.Username, pw, c.BasePath, c.InsecureSkipVerify, c.TimeoutSeconds, c.StallTimeoutSeconds)
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

	transport := &http.Transport{
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
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 — opt-in flag for self-signed servers
	}
	c.SetTransport(transport)

	if w.config.TimeoutSeconds > 0 {
		c.SetTimeout(time.Duration(w.config.TimeoutSeconds) * time.Second)
	}
	// else: leave http.Client.Timeout at its default 0 (no overall deadline).
	return c
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
	// Wrap the reader in a stall watchdog so we abort hung uploads promptly
	// instead of relying on the OS keepalive (which can take minutes). The
	// watchdog has no fixed deadline; it only fires when no bytes flow for
	// the configured window, so slow-but-progressing transfers of any size
	// will complete. (See client() comment block for #83 background.)
	sr := newStallReader(reader, w.stallTimeout())
	defer sr.cancel()
	if err := c.WriteStream(full, sr, 0640); err != nil {
		if sr.stalled.Load() {
			return fmt.Errorf("webdav: write %s: %w", full, ErrUploadStalled)
		}
		return fmt.Errorf("webdav: write %s: %w", full, err)
	}
	return nil
}

func (w *WebDAVAdapter) Read(p string) (io.ReadCloser, error) {
	full, err := w.fullPath(p, false)
	if err != nil {
		return nil, err
	}
	rc, err := w.client().ReadStream(full)
	if err != nil {
		return nil, fmt.Errorf("webdav: read %s: %w", full, err)
	}
	return rc, nil
}

func (w *WebDAVAdapter) Delete(p string) error {
	full, err := w.fullPath(p, false)
	if err != nil {
		return err
	}
	if err := w.client().Remove(full); err != nil {
		return fmt.Errorf("webdav: delete %s: %w", full, err)
	}
	return nil
}

func (w *WebDAVAdapter) List(prefix string) ([]FileInfo, error) {
	full, err := w.fullPath(prefix, true)
	if err != nil {
		return nil, err
	}
	entries, err := w.client().ReadDir(full)
	if err != nil {
		return nil, fmt.Errorf("webdav: list %s: %w", full, err)
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, FileInfo{
			Path:    path.Join(prefix, e.Name()),
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
	info, err := w.client().Stat(full)
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

var _ Adapter = (*WebDAVAdapter)(nil)
