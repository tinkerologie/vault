package engine

import (
	"context"
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
func (h *FolderHandler) Backup(ctx context.Context, item BackupItem, destDir string, progress ProgressFunc) (*BackupResult, error) {
	result := &BackupResult{ItemName: item.Name}

	srcPath, _ := item.Settings["path"].(string)
	if srcPath == "" {
		return nil, fmt.Errorf("folder path not specified in settings")
	}

	srcPath = filepath.Clean(srcPath)
	if _, err := os.Stat(srcPath); err != nil {
		return nil, fmt.Errorf("source path not accessible: %w", err)
	}

	if err := os.MkdirAll(destDir, 0750); err != nil {
		return nil, fmt.Errorf("creating dest dir: %w", err)
	}

	progress(item.Name, 10, "archiving "+srcPath)

	// Skip wasted CPU on media-heavy folders by downgrading to no-op
	// compression when most of the source is already-compressed bytes.
	effectiveCompression := MaybeDowngradeCompression(srcPath, item.Compression)
	archiveName := "data.tar" + archiveExt(effectiveCompression)
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
		if err := tarDirectoryFiltered(ctx, srcPath, archivePath, changedSince, nil, effectiveCompression); err != nil {
			return nil, fmt.Errorf("archiving changed files in %s: %w", srcPath, err)
		}
	} else {
		if err := tarDirectory(ctx, srcPath, archivePath, nil, effectiveCompression); err != nil {
			return nil, fmt.Errorf("archiving %s: %w", srcPath, err)
		}
	}
	result.Files = append(result.Files, backupFileInfo(archivePath))

	// Best-effort sidecar index for partial restore. Failures here are
	// logged via the returned error path but never abort the backup —
	// without the index, restore simply falls back to whole-archive
	// extraction (its prior behaviour).
	if err := WriteTarIndex(archivePath); err != nil {
		// Treat indexing as advisory: log and continue.
		_ = err
	} else {
		result.Files = append(result.Files, backupFileInfo(archivePath+IndexSuffix))
	}

	// Store source path metadata so restore knows the original location.
	metaPath := filepath.Join(destDir, "folder_meta.json")
	metaJSON := fmt.Sprintf(`{"path":%q,"name":%q}`, srcPath, item.Name)
	if err := os.WriteFile(metaPath, []byte(metaJSON), 0600); err != nil {
		return nil, fmt.Errorf("writing folder metadata: %w", err)
	}
	result.Files = append(result.Files, backupFileInfo(metaPath))

	progress(item.Name, 100, "backup complete")
	result.Success = true
	return result, nil
}

// Restore extracts the backup archive to its original path or an override destination.
func (h *FolderHandler) Restore(ctx context.Context, item BackupItem, sourceDir string, progress ProgressFunc) error {
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
		if data, err := os.ReadFile(metaPath); err == nil { // #nosec G304 — metaPath is sourceDir + fixed filename
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

	archivePath, err := findArchive(sourceDir, "data.tar")
	if err != nil {
		return fmt.Errorf("backup archive not found: %w", err)
	}

	if err := os.MkdirAll(destPath, 0750); err != nil {
		return fmt.Errorf("creating restore dir %s: %w", destPath, err)
	}

	// Partial-restore filter from the file-picker. nil = extract everything
	// (the legacy whole-archive path).
	include := extractRestoreFilePaths(item.Settings)

	if err := untarDirectoryFiltered(ctx, archivePath, destPath, include); err != nil {
		return fmt.Errorf("extracting to %s: %w", destPath, err)
	}

	progress(item.Name, 100, "restore complete")
	return nil
}

// extractRestoreFilePaths reads the "restore_file_paths" setting injected
// by the runner from the API and returns it as a []string. The setting can
// arrive as []string (direct call), []any (decoded from JSON), or be
// absent — all three cases produce a sensible result. Returns nil for the
// "extract everything" case so callers can use the result directly.
func extractRestoreFilePaths(settings map[string]any) []string {
	raw, ok := settings["restore_file_paths"]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
