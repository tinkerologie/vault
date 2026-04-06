package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Microsoft identity platform endpoints for personal (consumer) accounts.
var msOAuth2Endpoint = oauth2.Endpoint{ //nolint:gosec // OAuth endpoint URLs, not credentials
	AuthURL:  "https://login.microsoftonline.com/consumers/oauth2/v2.0/authorize",
	TokenURL: "https://login.microsoftonline.com/consumers/oauth2/v2.0/token",
}

const graphBaseURL = "https://graph.microsoft.com/v1.0"

// OneDriveConfig holds the configuration for OneDrive storage.
type OneDriveConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
	FolderPath   string `json:"folder_path"` // Path relative to drive root, e.g. "Vault/Backups". Empty for root.
}

// OneDriveAdapter implements the Adapter interface for Microsoft OneDrive
// using the Microsoft Graph API.
type OneDriveAdapter struct {
	config OneDriveConfig
}

// Compile-time interface check.
var _ Adapter = (*OneDriveAdapter)(nil)

// NewOneDriveAdapter creates a new OneDrive adapter.
// client_id and client_secret can be empty when VAULT_ONEDRIVE_CLIENT_ID and
// VAULT_ONEDRIVE_CLIENT_SECRET environment variables are set.
func NewOneDriveAdapter(cfg OneDriveConfig) (*OneDriveAdapter, error) {
	if cfg.ClientID == "" {
		cfg.ClientID = os.Getenv("VAULT_ONEDRIVE_CLIENT_ID")
	}
	if cfg.ClientSecret == "" {
		cfg.ClientSecret = os.Getenv("VAULT_ONEDRIVE_CLIENT_SECRET")
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("onedrive credentials not available: set VAULT_ONEDRIVE_CLIENT_ID and VAULT_ONEDRIVE_CLIENT_SECRET environment variables, or provide client_id and client_secret in the storage configuration")
	}
	if cfg.RefreshToken == "" {
		return nil, fmt.Errorf("refresh_token is required; connect to OneDrive via the UI to obtain one")
	}
	return &OneDriveAdapter{config: cfg}, nil
}

// httpClient returns an OAuth2-authenticated HTTP client.
func (o *OneDriveAdapter) httpClient(ctx context.Context) *http.Client {
	cfg := &oauth2.Config{
		ClientID:     o.config.ClientID,
		ClientSecret: o.config.ClientSecret,
		Endpoint:     msOAuth2Endpoint,
		Scopes:       []string{"Files.ReadWrite.All", "offline_access"},
	}
	token := &oauth2.Token{RefreshToken: o.config.RefreshToken}
	return cfg.Client(ctx, token)
}

// driveRootURL returns the URL for the folder root (for listing etc).
func (o *OneDriveAdapter) driveRootURL() string {
	if o.config.FolderPath == "" || o.config.FolderPath == "/" {
		return graphBaseURL + "/me/drive/root"
	}
	p := strings.Trim(o.config.FolderPath, "/")
	return graphBaseURL + "/me/drive/root:/" + url.PathEscape(strings.ReplaceAll(p, "/", "%2F")) + ":"
}

// graphItemByPath returns the MS Graph URL addressing a path-item.
// For a file: /me/drive/root:/path/to/file.txt:
func (o *OneDriveAdapter) graphItemByPath(filePath string) string {
	p := filePath
	if o.config.FolderPath != "" {
		p = strings.TrimRight(o.config.FolderPath, "/") + "/" + strings.TrimLeft(filePath, "/")
	}
	p = strings.Trim(p, "/")
	// Encode each segment individually.
	segments := strings.Split(p, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return graphBaseURL + "/me/drive/root:/" + strings.Join(segments, "/")
}

func (o *OneDriveAdapter) Write(filePath string, reader io.Reader) error {
	ctx := context.Background()
	client := o.httpClient(ctx)

	// Ensure parent folders exist.
	dir := path.Dir(filePath)
	if dir != "" && dir != "." && dir != "/" {
		if err := o.ensureFolder(ctx, client, dir); err != nil {
			return fmt.Errorf("ensure folder: %w", err)
		}
	}

	// Upload via PUT content endpoint.
	// For files up to 250 MB, simple upload works.
	uploadURL := o.graphItemByPath(filePath) + ":/content"

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, reader)
	if err != nil {
		return fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("upload %q: %w", filePath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("upload %q: HTTP %d: %s", filePath, resp.StatusCode, string(body))
	}
	return nil
}

func (o *OneDriveAdapter) Read(filePath string) (io.ReadCloser, error) {
	ctx := context.Background()
	client := o.httpClient(ctx)

	downloadURL := o.graphItemByPath(filePath) + ":/content"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %q: %w", filePath, err)
	}

	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("file not found: %s", filePath)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("download %q: HTTP %d: %s", filePath, resp.StatusCode, string(body))
	}
	return resp.Body, nil
}

func (o *OneDriveAdapter) Delete(filePath string) error {
	ctx := context.Background()
	client := o.httpClient(ctx)

	deleteURL := o.graphItemByPath(filePath) + ":"

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, deleteURL, nil)
	if err != nil {
		return fmt.Errorf("create delete request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("delete %q: %w", filePath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("file not found: %s", filePath)
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("delete %q: HTTP %d: %s", filePath, resp.StatusCode, string(body))
	}
	return nil
}

func (o *OneDriveAdapter) List(prefix string) ([]FileInfo, error) {
	ctx := context.Background()
	client := o.httpClient(ctx)

	// Determine the folder URL.
	var listURL string
	if prefix == "" || prefix == "." || prefix == "/" {
		listURL = o.driveRootURL() + "/children"
	} else {
		listURL = o.graphItemByPath(prefix) + ":/children"
	}

	var files []FileInfo
	nextLink := listURL + "?$select=name,size,lastModifiedDateTime,folder&$top=200"

	for nextLink != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextLink, nil)
		if err != nil {
			return nil, fmt.Errorf("create list request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list %q: %w", prefix, err)
		}

		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("folder not found: %s", prefix)
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			_ = resp.Body.Close()
			return nil, fmt.Errorf("list %q: HTTP %d: %s", prefix, resp.StatusCode, string(body))
		}

		var result graphListResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("decode list response: %w", err)
		}
		_ = resp.Body.Close()

		for _, item := range result.Value {
			modTime, _ := time.Parse(time.RFC3339, item.LastModifiedDateTime)
			entryPath := item.Name
			if prefix != "" && prefix != "." && prefix != "/" {
				entryPath = strings.TrimRight(prefix, "/") + "/" + item.Name
			}
			files = append(files, FileInfo{
				Path:    entryPath,
				Size:    item.Size,
				ModTime: modTime,
				IsDir:   item.Folder != nil,
			})
		}

		nextLink = result.NextLink
	}
	return files, nil
}

func (o *OneDriveAdapter) Stat(filePath string) (FileInfo, error) {
	ctx := context.Background()
	client := o.httpClient(ctx)

	statURL := o.graphItemByPath(filePath) + ":?$select=name,size,lastModifiedDateTime,folder"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statURL, nil)
	if err != nil {
		return FileInfo{}, fmt.Errorf("create stat request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return FileInfo{}, fmt.Errorf("stat %q: %w", filePath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return FileInfo{}, fmt.Errorf("not found: %s", filePath)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return FileInfo{}, fmt.Errorf("stat %q: HTTP %d: %s", filePath, resp.StatusCode, string(body))
	}

	var item graphDriveItem
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return FileInfo{}, fmt.Errorf("decode stat response: %w", err)
	}

	modTime, _ := time.Parse(time.RFC3339, item.LastModifiedDateTime)
	return FileInfo{
		Path:    filePath,
		Size:    item.Size,
		ModTime: modTime,
		IsDir:   item.Folder != nil,
	}, nil
}

func (o *OneDriveAdapter) TestConnection() error {
	ctx := context.Background()
	client := o.httpClient(ctx)

	// Verify we can access the drive root.
	rootURL := o.driveRootURL() + "?$select=id,name"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rootURL, nil)
	if err != nil {
		return fmt.Errorf("create test request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("cannot access drive: HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Verify write permission by creating and deleting a test file.
	testContent := strings.NewReader("vault-connection-test")
	testPath := ".vault-connection-test"
	if err := o.Write(testPath, testContent); err != nil {
		return fmt.Errorf("write permission test failed: %w", err)
	}
	if err := o.Delete(testPath); err != nil {
		return fmt.Errorf("write test succeeded but cleanup failed: %w", err)
	}

	return nil
}

// ensureFolder creates the folder hierarchy for a given path if it doesn't exist.
func (o *OneDriveAdapter) ensureFolder(ctx context.Context, client *http.Client, dirPath string) error {
	fullPath := dirPath
	if o.config.FolderPath != "" {
		fullPath = strings.TrimRight(o.config.FolderPath, "/") + "/" + strings.TrimLeft(dirPath, "/")
	}

	parts := strings.Split(strings.Trim(fullPath, "/"), "/")
	parentURL := graphBaseURL + "/me/drive/root"

	for _, name := range parts {
		if name == "" {
			continue
		}
		// Try to get the child folder.
		childURL := parentURL + "/children?$filter=name eq '" + url.QueryEscape(name) + "' and folder ne null&$select=id,name"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, childURL, nil)
		if err != nil {
			return fmt.Errorf("create folder lookup request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("folder lookup %q: %w", name, err)
		}

		var result graphListResponse
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			_ = resp.Body.Close()
			return fmt.Errorf("decode folder lookup: %w", decErr)
		}
		_ = resp.Body.Close()

		if len(result.Value) > 0 {
			parentURL = graphBaseURL + "/me/drive/items/" + result.Value[0].ID
			continue
		}

		// Create the folder.
		createBody := fmt.Sprintf(`{"name":%q,"folder":{},"@microsoft.graph.conflictBehavior":"fail"}`, name)
		createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, parentURL+"/children", strings.NewReader(createBody))
		if err != nil {
			return fmt.Errorf("create folder request: %w", err)
		}
		createReq.Header.Set("Content-Type", "application/json")

		createResp, err := client.Do(createReq)
		if err != nil {
			return fmt.Errorf("create folder %q: %w", name, err)
		}

		if createResp.StatusCode != http.StatusCreated && createResp.StatusCode != http.StatusOK && createResp.StatusCode != http.StatusConflict {
			body, _ := io.ReadAll(io.LimitReader(createResp.Body, 1024))
			_ = createResp.Body.Close()
			return fmt.Errorf("create folder %q: HTTP %d: %s", name, createResp.StatusCode, string(body))
		}

		var created graphDriveItem
		if decErr := json.NewDecoder(createResp.Body).Decode(&created); decErr != nil {
			_ = createResp.Body.Close()
			// Conflict (folder already exists) — look it up again.
			if createResp.StatusCode == http.StatusConflict {
				continue
			}
			return fmt.Errorf("decode create folder response: %w", decErr)
		}
		_ = createResp.Body.Close()
		parentURL = graphBaseURL + "/me/drive/items/" + created.ID
	}
	return nil
}

// Microsoft Graph API response types.

type graphListResponse struct {
	Value    []graphDriveItem `json:"value"`
	NextLink string           `json:"@odata.nextLink"`
}

type graphDriveItem struct {
	ID                   string       `json:"id"`
	Name                 string       `json:"name"`
	Size                 int64        `json:"size"`
	LastModifiedDateTime string       `json:"lastModifiedDateTime"`
	Folder               *graphFolder `json:"folder,omitempty"`
}

type graphFolder struct {
	ChildCount int `json:"childCount"`
}
