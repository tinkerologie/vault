package storage

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// GDriveConfig holds the configuration for Google Drive storage.
type GDriveConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
	FolderID     string `json:"folder_id"` // Target folder ID; empty for root ("root").
}

// GDriveAdapter implements the Adapter interface for Google Drive.
type GDriveAdapter struct {
	config GDriveConfig
}

// NewGDriveAdapter creates a new Google Drive adapter.
func NewGDriveAdapter(cfg GDriveConfig) (*GDriveAdapter, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RefreshToken == "" {
		return nil, fmt.Errorf("client_id, client_secret, and refresh_token are required")
	}
	return &GDriveAdapter{config: cfg}, nil
}

// driveService creates a new Drive API service using the stored OAuth2
// credentials. A fresh service is created per-operation following the
// SFTP/SMB pattern.
func (g *GDriveAdapter) driveService(ctx context.Context) (*drive.Service, error) {
	oauthCfg := &oauth2.Config{
		ClientID:     g.config.ClientID,
		ClientSecret: g.config.ClientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{drive.DriveFileScope},
	}
	token := &oauth2.Token{RefreshToken: g.config.RefreshToken}
	client := oauthCfg.Client(ctx, token)
	svc, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("create drive service: %w", err)
	}
	return svc, nil
}

// baseFolderID returns the configured folder ID or "root" as default.
func (g *GDriveAdapter) baseFolderID() string {
	if g.config.FolderID == "" {
		return "root"
	}
	return g.config.FolderID
}

// resolveFolder navigates or creates the folder hierarchy for a given
// path relative to the base folder. Returns the final folder ID.
func (g *GDriveAdapter) resolveFolder(ctx context.Context, svc *drive.Service, dirPath string, create bool) (string, error) {
	parentID := g.baseFolderID()
	if dirPath == "" || dirPath == "." || dirPath == "/" {
		return parentID, nil
	}

	parts := strings.Split(strings.Trim(dirPath, "/"), "/")
	for _, name := range parts {
		if name == "" {
			continue
		}
		id, err := g.findChild(ctx, svc, parentID, name, true)
		if err != nil {
			return "", err
		}
		if id != "" {
			parentID = id
			continue
		}
		if !create {
			return "", fmt.Errorf("folder not found: %s", dirPath)
		}
		newID, err := g.createFolder(ctx, svc, parentID, name)
		if err != nil {
			return "", err
		}
		parentID = newID
	}
	return parentID, nil
}

// findChild finds a child file or folder by name inside parentID.
// If folderOnly is true, only folders are matched.
func (g *GDriveAdapter) findChild(ctx context.Context, svc *drive.Service, parentID, name string, folderOnly bool) (string, error) {
	q := fmt.Sprintf("'%s' in parents and name = '%s' and trashed = false",
		escapeQuery(parentID), escapeQuery(name))
	if folderOnly {
		q += " and mimeType = 'application/vnd.google-apps.folder'"
	}
	list, err := svc.Files.List().Context(ctx).Q(q).Fields("files(id)").PageSize(1).Do()
	if err != nil {
		return "", fmt.Errorf("find child %q: %w", name, err)
	}
	if len(list.Files) == 0 {
		return "", nil
	}
	return list.Files[0].Id, nil
}

// findFile finds a file (not folder) by name inside parentID.
func (g *GDriveAdapter) findFile(ctx context.Context, svc *drive.Service, parentID, name string) (string, error) {
	q := fmt.Sprintf("'%s' in parents and name = '%s' and trashed = false and mimeType != 'application/vnd.google-apps.folder'",
		escapeQuery(parentID), escapeQuery(name))
	list, err := svc.Files.List().Context(ctx).Q(q).Fields("files(id)").PageSize(1).Do()
	if err != nil {
		return "", fmt.Errorf("find file %q: %w", name, err)
	}
	if len(list.Files) == 0 {
		return "", nil
	}
	return list.Files[0].Id, nil
}

// createFolder creates a folder inside parentID with the given name.
func (g *GDriveAdapter) createFolder(ctx context.Context, svc *drive.Service, parentID, name string) (string, error) {
	f := &drive.File{
		Name:     name,
		MimeType: "application/vnd.google-apps.folder",
		Parents:  []string{parentID},
	}
	created, err := svc.Files.Create(f).Context(ctx).Fields("id").Do()
	if err != nil {
		return "", fmt.Errorf("create folder %q: %w", name, err)
	}
	return created.Id, nil
}

func (g *GDriveAdapter) Write(filePath string, reader io.Reader) error {
	ctx := context.Background()
	svc, err := g.driveService(ctx)
	if err != nil {
		return err
	}

	dir := path.Dir(filePath)
	name := path.Base(filePath)

	folderID, err := g.resolveFolder(ctx, svc, dir, true)
	if err != nil {
		return fmt.Errorf("resolve folder: %w", err)
	}

	// Check if file already exists — update instead of creating a duplicate.
	existingID, err := g.findFile(ctx, svc, folderID, name)
	if err != nil {
		return fmt.Errorf("check existing file: %w", err)
	}

	if existingID != "" {
		_, err = svc.Files.Update(existingID, &drive.File{}).
			Context(ctx).Media(reader).Do()
	} else {
		f := &drive.File{
			Name:    name,
			Parents: []string{folderID},
		}
		_, err = svc.Files.Create(f).Context(ctx).Media(reader).Do()
	}
	if err != nil {
		return fmt.Errorf("upload %q: %w", filePath, err)
	}
	return nil
}

func (g *GDriveAdapter) Read(filePath string) (io.ReadCloser, error) {
	ctx := context.Background()
	svc, err := g.driveService(ctx)
	if err != nil {
		return nil, err
	}

	dir := path.Dir(filePath)
	name := path.Base(filePath)

	folderID, err := g.resolveFolder(ctx, svc, dir, false)
	if err != nil {
		return nil, fmt.Errorf("resolve folder: %w", err)
	}

	fileID, err := g.findFile(ctx, svc, folderID, name)
	if err != nil {
		return nil, err
	}
	if fileID == "" {
		return nil, fmt.Errorf("file not found: %s", filePath)
	}

	resp, err := svc.Files.Get(fileID).Context(ctx).Download()
	if err != nil {
		return nil, fmt.Errorf("download %q: %w", filePath, err)
	}
	return resp.Body, nil
}

func (g *GDriveAdapter) Delete(filePath string) error {
	ctx := context.Background()
	svc, err := g.driveService(ctx)
	if err != nil {
		return err
	}

	dir := path.Dir(filePath)
	name := path.Base(filePath)

	folderID, err := g.resolveFolder(ctx, svc, dir, false)
	if err != nil {
		return fmt.Errorf("resolve folder: %w", err)
	}

	fileID, err := g.findFile(ctx, svc, folderID, name)
	if err != nil {
		return err
	}
	if fileID == "" {
		return fmt.Errorf("file not found: %s", filePath)
	}

	if err := svc.Files.Delete(fileID).Context(ctx).Do(); err != nil {
		return fmt.Errorf("delete %q: %w", filePath, err)
	}
	return nil
}

func (g *GDriveAdapter) List(prefix string) ([]FileInfo, error) {
	ctx := context.Background()
	svc, err := g.driveService(ctx)
	if err != nil {
		return nil, err
	}

	folderID, err := g.resolveFolder(ctx, svc, prefix, false)
	if err != nil {
		return nil, fmt.Errorf("resolve folder: %w", err)
	}

	q := fmt.Sprintf("'%s' in parents and trashed = false", escapeQuery(folderID))
	var files []FileInfo
	pageToken := ""
	for {
		call := svc.Files.List().Context(ctx).Q(q).
			Fields("nextPageToken, files(id, name, size, modifiedTime, mimeType)").
			PageSize(1000)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		list, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("list %q: %w", prefix, err)
		}
		for _, f := range list.Files {
			modTime, _ := time.Parse(time.RFC3339, f.ModifiedTime)
			isDir := f.MimeType == "application/vnd.google-apps.folder"
			entryPath := f.Name
			if prefix != "" && prefix != "." && prefix != "/" {
				entryPath = strings.TrimRight(prefix, "/") + "/" + f.Name
			}
			files = append(files, FileInfo{
				Path:    entryPath,
				Size:    f.Size,
				ModTime: modTime,
				IsDir:   isDir,
			})
		}
		if list.NextPageToken == "" {
			break
		}
		pageToken = list.NextPageToken
	}
	return files, nil
}

func (g *GDriveAdapter) Stat(filePath string) (FileInfo, error) {
	ctx := context.Background()
	svc, err := g.driveService(ctx)
	if err != nil {
		return FileInfo{}, err
	}

	dir := path.Dir(filePath)
	name := path.Base(filePath)

	folderID, err := g.resolveFolder(ctx, svc, dir, false)
	if err != nil {
		return FileInfo{}, fmt.Errorf("resolve folder: %w", err)
	}

	// Search for both files and folders.
	q := fmt.Sprintf("'%s' in parents and name = '%s' and trashed = false",
		escapeQuery(folderID), escapeQuery(name))
	list, err := svc.Files.List().Context(ctx).Q(q).
		Fields("files(id, name, size, modifiedTime, mimeType)").PageSize(1).Do()
	if err != nil {
		return FileInfo{}, fmt.Errorf("stat %q: %w", filePath, err)
	}
	if len(list.Files) == 0 {
		return FileInfo{}, fmt.Errorf("not found: %s", filePath)
	}

	f := list.Files[0]
	modTime, _ := time.Parse(time.RFC3339, f.ModifiedTime)
	return FileInfo{
		Path:    filePath,
		Size:    f.Size,
		ModTime: modTime,
		IsDir:   f.MimeType == "application/vnd.google-apps.folder",
	}, nil
}

func (g *GDriveAdapter) TestConnection() error {
	ctx := context.Background()
	svc, err := g.driveService(ctx)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	// Verify we can access the target folder.
	folderID := g.baseFolderID()
	_, err = svc.Files.Get(folderID).Context(ctx).Fields("id, name").Do()
	if err != nil {
		return fmt.Errorf("cannot access folder: %w", err)
	}

	// Verify write permission by creating and deleting a test file.
	testFile := &drive.File{
		Name:    ".vault-connection-test",
		Parents: []string{folderID},
	}
	created, err := svc.Files.Create(testFile).Context(ctx).Fields("id").Do()
	if err != nil {
		return fmt.Errorf("write permission test failed: %w", err)
	}
	if delErr := svc.Files.Delete(created.Id).Context(ctx).Do(); delErr != nil {
		return fmt.Errorf("write test succeeded but cleanup failed: %w", delErr)
	}

	return nil
}

// escapeQuery escapes backslashes and single quotes in Google Drive query strings.
func escapeQuery(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	return strings.ReplaceAll(s, "'", "\\'")
}

var _ Adapter = (*GDriveAdapter)(nil)
