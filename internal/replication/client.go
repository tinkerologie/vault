package replication

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tinkerologie/vault/internal/bounded"
	"github.com/tinkerologie/vault/internal/credentials"
)

// Client talks to a remote Vault API server.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	// download streams files. It has no total timeout; see DownloadFile.
	download *http.Client
}

// NewClient creates a replication client for the given remote Vault URL.
func NewClient(baseURL string) (*Client, error) {
	normalizedBaseURL, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	downloads := peerTransport.Clone()
	downloads.ResponseHeaderTimeout = downloadIdleTimeout
	return &Client{
		baseURL: normalizedBaseURL,
		httpClient: &http.Client{
			Timeout:       10 * time.Minute,
			Transport:     peerTransport,
			CheckRedirect: refuseKeyLeakingRedirect,
		},
		download: &http.Client{
			Transport:     downloads,
			CheckRedirect: refuseKeyLeakingRedirect,
		},
	}, nil
}

// refuseKeyLeakingRedirect stops a redirect to another host or from HTTPS to
// HTTP. Go forwards custom headers across redirects, so following one would hand
// X-API-Key to whatever the redirect names.
func refuseKeyLeakingRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	origin := via[0].URL
	if req.URL.Host != origin.Host {
		return fmt.Errorf("refusing redirect to a different host (%s)", req.URL.Host)
	}
	if origin.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect from https to %s", req.URL.Scheme)
	}
	return nil
}

// PlaintextWarning returns a warning when a replication URL sends the API key
// and backup data unencrypted to another machine, and "" otherwise. Plain HTTP
// stays allowed because most home servers have no TLS.
func PlaintextWarning(baseURL string) string {
	if !credentials.IsPlaintextRemote(baseURL) {
		return ""
	}
	return "This connection uses plain HTTP, so the API key and backup data can be read on the network. Use it only on a network you trust, or use https://."
}

// NewClientWithAPIKey creates a replication client that authenticates using
// the given API key via the X-API-Key header.
func NewClientWithAPIKey(baseURL, apiKey string) (*Client, error) {
	c, err := NewClient(baseURL)
	if err != nil {
		return nil, err
	}
	c.apiKey = apiKey
	return c, nil
}

// SetTimeout overrides the HTTP client timeout. Use a short duration for
// connectivity checks so the caller doesn't block on slow DNS or TCP connects.
func (c *Client) SetTimeout(d time.Duration) {
	c.httpClient.Timeout = d
}

// NormalizeBaseURL validates and canonicalizes a remote Vault base URL.
func NormalizeBaseURL(baseURL string) (string, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return "", fmt.Errorf("url is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("url scheme must be http or https")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("url must not include user credentials")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("url host is required")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("url must not include query strings or fragments")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("url path must be empty")
	}

	parsed.Path = ""
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

// RemoteJob mirrors the fields of db.Job that we need for replication.
type RemoteJob struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	BackupTypeChain string `json:"backup_type_chain"`
	Compression     string `json:"compression"`
	Encryption      string `json:"encryption"`
	ContainerMode   string `json:"container_mode"`
	VMMode          string `json:"vm_mode"`
	RetentionCount  int    `json:"retention_count"`
	RetentionDays   int    `json:"retention_days"`
	StorageDestID   int64  `json:"storage_dest_id"`
	// SourceID is non-zero when the job is itself a replica on the remote
	// peer. Older peers omit the field, so the zero value means "local".
	SourceID int64 `json:"source_id"`
}

// RemoteRestorePoint mirrors the fields of db.RestorePoint needed for replication.
type RemoteRestorePoint struct {
	ID                   int64  `json:"id"`
	JobRunID             int64  `json:"job_run_id"`
	JobID                int64  `json:"job_id"`
	BackupType           string `json:"backup_type"`
	StoragePath          string `json:"storage_path"`
	Metadata             string `json:"metadata"`
	SizeBytes            int64  `json:"size_bytes"`
	ParentRestorePointID int64  `json:"parent_restore_point_id"`
	CreatedAt            string `json:"created_at"`
}

// HealthResponse represents the remote /health endpoint response.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// StorageFile represents a file listed from a storage destination.
type StorageFile struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModTime string `json:"mod_time,omitempty"`
}

// TestConnection verifies connectivity to the remote Vault instance.
func (c *Client) TestConnection() (*HealthResponse, error) {
	var resp HealthResponse
	if err := c.getJSON("/api/v1/health", &resp); err != nil {
		return nil, fmt.Errorf("test connection: %w", err)
	}
	return &resp, nil
}

// ListJobs returns all jobs from the remote Vault instance.
func (c *Client) ListJobs() ([]RemoteJob, error) {
	var jobs []RemoteJob
	if err := c.getJSON("/api/v1/jobs", &jobs); err != nil {
		return nil, fmt.Errorf("list remote jobs: %w", err)
	}
	return jobs, nil
}

// ListRestorePoints returns the restore points for a remote job.
func (c *Client) ListRestorePoints(jobID int64) ([]RemoteRestorePoint, error) {
	var rps []RemoteRestorePoint
	if err := c.getJSON(fmt.Sprintf("/api/v1/jobs/%d/restore-points", jobID), &rps); err != nil {
		return nil, fmt.Errorf("list remote restore points for job %d: %w", jobID, err)
	}
	return rps, nil
}

// RemoteRun mirrors the fields of a remote job run that describe its outcome.
type RemoteRun struct {
	ID          int64  `json:"id"`
	Status      string `json:"status"`
	ItemsTotal  int    `json:"items_total"`
	ItemsDone   int    `json:"items_done"`
	ItemsFailed int    `json:"items_failed"`
}

// maxPeerRuns is how much of a remote job's history ListJobRuns asks for, the
// most the peer's history endpoint returns.
const maxPeerRuns = 1000

// ListJobRuns returns the most recent runs of a remote job, newest first.
func (c *Client) ListJobRuns(jobID int64) ([]RemoteRun, error) {
	var runs []RemoteRun
	path := fmt.Sprintf("/api/v1/jobs/%d/history", jobID)
	if err := c.getJSONWithParams(path, map[string]string{"limit": fmt.Sprint(maxPeerRuns)}, &runs); err != nil {
		return nil, fmt.Errorf("list remote runs for job %d: %w", jobID, err)
	}
	return runs, nil
}

// ListStorageFiles lists files at the given path on a remote storage destination.
func (c *Client) ListStorageFiles(storageID int64, prefix string) ([]StorageFile, error) {
	var files []StorageFile
	path := fmt.Sprintf("/api/v1/storage/%d/list", storageID)
	if err := c.getJSONWithParams(path, map[string]string{"prefix": prefix}, &files); err != nil {
		return nil, fmt.Errorf("list remote storage files: %w", err)
	}
	return files, nil
}

// downloadIdleTimeout is how long a file download may wait for the response
// headers, or between pieces of data, before it is abandoned. Downloads have
// no total time limit, since a large backup file over a slow link can take
// hours.
var downloadIdleTimeout = 2 * time.Minute

// DownloadFile streams a file from the remote storage destination.
// The caller is responsible for closing the returned ReadCloser.
func (c *Client) DownloadFile(storageID int64, filePath string) (io.ReadCloser, error) {
	// The same request, sent by the client without a total timeout.
	streaming := *c
	streaming.httpClient = c.download
	reqPath := fmt.Sprintf("/api/v1/storage/%d/files", storageID)
	resp, err := streaming.doRequestWithParams(http.MethodGet, reqPath, map[string]string{"path": filePath})
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			err = fmt.Errorf("peer sent no data for %s: %w", downloadIdleTimeout, err)
		}
		return nil, fmt.Errorf("download file %q: %w", filePath, err)
	}
	// Every body this client reads, error responses included, goes through
	// the idle reader: it has no total timeout to fall back on.
	body := newIdleReader(resp.Body, downloadIdleTimeout)
	if resp.StatusCode != http.StatusOK {
		resp.Body = body
		err := peerStatusError(resp)
		_ = body.Close()
		return nil, fmt.Errorf("download file %q: %w", filePath, err)
	}
	return body, nil
}

// idleReader is a download body that is closed when a single Read waits more
// than idle for the peer. Only time spent inside Read counts, so a slow
// destination write between reads never looks like a stalled peer.
type idleReader struct {
	rc      io.ReadCloser
	idle    time.Duration
	timer   *time.Timer
	expired atomic.Bool
}

func newIdleReader(rc io.ReadCloser, idle time.Duration) *idleReader {
	r := &idleReader{rc: rc, idle: idle}
	r.timer = time.AfterFunc(idle, func() {
		r.expired.Store(true)
		_ = rc.Close() // unblocks the Read waiting on the peer
	})
	r.timer.Stop() // armed only while a Read is in progress
	return r
}

func (r *idleReader) Read(p []byte) (int, error) {
	if r.expired.Load() {
		return 0, fmt.Errorf("peer sent no data for %s", r.idle)
	}
	r.timer.Reset(r.idle)
	n, err := r.rc.Read(p)
	r.timer.Stop()
	if err != nil && err != io.EOF && r.expired.Load() {
		err = fmt.Errorf("peer sent no data for %s: %w", r.idle, err)
	}
	return n, err
}

func (r *idleReader) Close() error {
	r.timer.Stop()
	return r.rc.Close()
}

// maxPeerJSONBytes limits one JSON response from a peer. Listings of jobs,
// restore points and files stay far below this; file downloads are streamed and
// not subject to it.
var maxPeerJSONBytes int64 = 1 << 30

// Limits on what peerStatusError takes from an error response.
const (
	maxPeerErrorBody  = 4 << 10
	maxPeerErrorRunes = 200
)

// peerStatusError describes a failed peer response by its status code and, when
// the response is JSON, the short "error" message Vault puts there, such as how
// to create an API key. The URL can point at any service, so nothing else from
// the body is returned, and the message is bounded and stripped of control and
// formatting characters.
func peerStatusError(resp *http.Response) error {
	msg := ""
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err == nil && mt == "application/json" {
		var body struct {
			Error string `json:"error"`
		}
		if raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPeerErrorBody)); err == nil && json.Unmarshal(raw, &body) == nil {
			msg = sanitizePeerMessage(body.Error)
		}
	}
	if msg == "" {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return fmt.Errorf("status %d: %s", resp.StatusCode, msg)
}

// sanitizePeerMessage keeps the printable text of a peer message on one line,
// at most maxPeerErrorRunes long.
func sanitizePeerMessage(s string) string {
	printable := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if r == utf8.RuneError || !unicode.IsGraphic(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	msg := []rune(strings.Join(strings.Fields(printable), " "))
	if len(msg) > maxPeerErrorRunes {
		return string(msg[:maxPeerErrorRunes]) + "…"
	}
	return string(msg)
}

// getJSON performs a GET request and decodes the JSON response.
func (c *Client) getJSON(path string, target any) error {
	return c.getJSONWithParams(path, nil, target)
}

// getJSONWithParams performs a GET request with query params and decodes JSON.
func (c *Client) getJSONWithParams(path string, params map[string]string, target any) error {
	resp, err := c.doRequestWithParams(http.MethodGet, path, params)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return peerStatusError(resp)
	}
	return bounded.DecodeJSON(resp.Body, maxPeerJSONBytes, target)
}

// doRequestWithParams builds and executes an HTTP request with query params.
func (c *Client) doRequestWithParams(method, path string, params map[string]string) (*http.Response, error) {
	u, err := url.JoinPath(c.baseURL, path) // #nosec G125 -- base URL is validated; routes are fixed strings/numeric IDs. File paths are encoded as query parameters, never joined here.
	if err != nil {
		return nil, fmt.Errorf("build URL: %w", err)
	}
	if len(params) > 0 {
		parsed, err := url.Parse(u)
		if err != nil {
			return nil, fmt.Errorf("parse URL: %w", err)
		}
		q := parsed.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		parsed.RawQuery = q.Encode()
		u = parsed.String()
	}
	// aikido-ignore-next-line AIK_go_G107 -- u is built from c.baseURL (admin-configured replication source URL) joined with a fixed path; not request-time user input.
	req, err := http.NewRequest(method, u, nil) // #nosec G107
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	return c.httpClient.Do(req)
}
