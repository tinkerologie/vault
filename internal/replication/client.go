package replication

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to a remote Vault API server.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a replication client for the given remote Vault URL.
// The apiKey is sent as an X-API-Key header for authentication.
func NewClient(baseURL, apiKey string) (*Client, error) {
	normalizedBaseURL, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	return &Client{
		baseURL: normalizedBaseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
	}, nil
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

// GetJob returns a single job from the remote Vault instance.
func (c *Client) GetJob(jobID int64) (*RemoteJob, error) {
	var job RemoteJob
	if err := c.getJSON(fmt.Sprintf("/api/v1/jobs/%d", jobID), &job); err != nil {
		return nil, fmt.Errorf("get remote job %d: %w", jobID, err)
	}
	return &job, nil
}

// ListRestorePoints returns the restore points for a remote job.
func (c *Client) ListRestorePoints(jobID int64) ([]RemoteRestorePoint, error) {
	var rps []RemoteRestorePoint
	if err := c.getJSON(fmt.Sprintf("/api/v1/jobs/%d/restore-points", jobID), &rps); err != nil {
		return nil, fmt.Errorf("list remote restore points for job %d: %w", jobID, err)
	}
	return rps, nil
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

// DownloadFile streams a file from the remote storage destination.
// The caller is responsible for closing the returned ReadCloser.
func (c *Client) DownloadFile(storageID int64, filePath string) (io.ReadCloser, error) {
	reqPath := fmt.Sprintf("/api/v1/storage/%d/files", storageID)
	resp, err := c.doRequestWithParams(http.MethodGet, reqPath, map[string]string{"path": filePath})
	if err != nil {
		return nil, fmt.Errorf("download file %q: %w", filePath, err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("download file %q: status %d: %s", filePath, resp.StatusCode, body)
	}
	return resp.Body, nil
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("status %d: %s", resp.StatusCode, body)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

// doRequestWithParams builds and executes an HTTP request with query params.
func (c *Client) doRequestWithParams(method, path string, params map[string]string) (*http.Response, error) {
	u, err := url.JoinPath(c.baseURL, path)
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
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	return c.httpClient.Do(req)
}
