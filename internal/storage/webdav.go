package storage

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
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
	return fmt.Sprintf("WebDAVConfig{URL:%q Username:%q Password:%s BasePath:%q InsecureSkipVerify:%t}",
		c.URL, c.Username, pw, c.BasePath, c.InsecureSkipVerify)
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

// client builds a fresh WebDAV client. Sets a sensible HTTP timeout so a
// hung server cannot block the daemon indefinitely.
func (w *WebDAVAdapter) client() *gowebdav.Client {
	c := gowebdav.NewAuthClient(w.config.URL, gowebdav.NewAutoAuth(w.config.Username, w.config.Password))
	c.SetTimeout(60 * time.Second)
	if w.config.InsecureSkipVerify {
		c.SetTransport(&http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 — opt-in flag for self-signed servers
		})
	}
	return c
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
	if err := c.WriteStream(full, reader, 0640); err != nil {
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
