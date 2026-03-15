package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FolderHandler implements Handler for arbitrary filesystem directories.
// It backs up directory trees as tar.gz archives (reusing the tarDirectory/
// untarDirectory helpers from container.go).
type FolderHandler struct{}

// NewFolderHandler creates a new FolderHandler.
func NewFolderHandler() (*FolderHandler, error) {
	return &FolderHandler{}, nil
}

// ListItems returns well-known folder presets on Unraid (e.g. Flash Drive).
// Custom folder paths are managed by the frontend and stored in job_items.
func (h *FolderHandler) ListItems() ([]BackupItem, error) {
	items := []BackupItem{}

	// Flash Drive preset — always listed if /boot exists.
	if info, err := os.Stat("/boot"); err == nil && info.IsDir() {
		items = append(items, BackupItem{
			Name: "Flash Drive",
			Type: "folder",
			Settings: map[string]any{
				"path":   "/boot",
				"preset": "flash",
			},
		})
	}

	return items, nil
}

// Backup creates a tar.gz archive of the configured path.
// If item.Settings["changed_since"] is set (RFC3339 timestamp), only files
// modified after that time are included (for incremental/differential backups).
func (h *FolderHandler) Backup(item BackupItem, destDir string, progress ProgressFunc) (*BackupResult, error) {
	result := &BackupResult{ItemName: item.Name}

	srcPath, _ := item.Settings["path"].(string)
	if srcPath == "" {
		return nil, fmt.Errorf("folder path not specified in settings")
	}

	srcPath = filepath.Clean(srcPath)
	if _, err := os.Stat(srcPath); err != nil {
		return nil, fmt.Errorf("source path not accessible: %w", err)
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("creating dest dir: %w", err)
	}

	progress(item.Name, 10, "archiving "+srcPath)

	archiveName := "data.tar.gz"
	archivePath := filepath.Join(destDir, archiveName)

	// Determine if this is an incremental/differential backup.
	var changedSince time.Time
	if cs, ok := item.Settings["changed_since"].(string); ok && cs != "" {
		if t, err := time.Parse(time.RFC3339, cs); err == nil {
			changedSince = t
		}
	}

	if !changedSince.IsZero() {
		// Incremental/differential: only archive files modified since the reference time.
		if err := tarDirectoryFiltered(srcPath, archivePath, changedSince); err != nil {
			return nil, fmt.Errorf("archiving changed files in %s: %w", srcPath, err)
		}
	} else {
		if err := tarDirectory(srcPath, archivePath); err != nil {
			return nil, fmt.Errorf("archiving %s: %w", srcPath, err)
		}
	}
	result.Files = append(result.Files, backupFileInfo(archivePath))

	// Store source path metadata so restore knows the original location.
	metaPath := filepath.Join(destDir, "folder_meta.json")
	metaJSON := fmt.Sprintf(`{"path":%q,"name":%q}`, srcPath, item.Name)
	if err := os.WriteFile(metaPath, []byte(metaJSON), 0644); err != nil {
		return nil, fmt.Errorf("writing folder metadata: %w", err)
	}
	result.Files = append(result.Files, backupFileInfo(metaPath))

	progress(item.Name, 100, "backup complete")
	result.Success = true
	return result, nil
}

// Restore extracts the backup archive to its original path or an override destination.
func (h *FolderHandler) Restore(item BackupItem, sourceDir string, progress ProgressFunc) error {
	progress(item.Name, 10, "reading metadata")

	// Check for restore destination override first.
	destPath, _ := item.Settings["restore_destination"].(string)

	// Fall back to path from settings.
	if destPath == "" {
		destPath, _ = item.Settings["path"].(string)
	}

	// Try reading stored metadata if settings don't have the path.
	if destPath == "" {
		metaPath := filepath.Join(sourceDir, "folder_meta.json")
		if data, err := os.ReadFile(metaPath); err == nil {
			var meta struct {
				Path string `json:"path"`
			}
			if jsonErr := json.Unmarshal(data, &meta); jsonErr == nil && meta.Path != "" {
				destPath = meta.Path
			}
		}
	}

	if destPath == "" {
		return fmt.Errorf("cannot determine restore path: no path in settings or metadata")
	}

	normalizedDestPath, err := normalizeRestorePath(destPath)
	if err != nil {
		return err
	}
	destPath = normalizedDestPath

	progress(item.Name, 30, "restoring to "+destPath)

	archivePath := filepath.Join(sourceDir, "data.tar.gz")
	if _, err := os.Stat(archivePath); err != nil {
		return fmt.Errorf("backup archive not found: %w", err)
	}

	if err := os.MkdirAll(destPath, 0755); err != nil {
		return fmt.Errorf("creating restore dir %s: %w", destPath, err)
	}

	if err := untarDirectory(archivePath, destPath); err != nil {
		return fmt.Errorf("extracting to %s: %w", destPath, err)
	}

	progress(item.Name, 100, "restore complete")
	return nil
}
