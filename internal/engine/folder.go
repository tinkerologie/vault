package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/safepath"
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

// BackupChunked walks the source tree and writes every regular file's content
// into the dedup repo, accumulating a Manifest that lists per-file chunk IDs.
// Returns the manifest's chunk ID (the runner persists it as
// restore_point.manifest_id). The repo's Flush is NOT called here — the
// runner flushes once per backup run after all items complete, to amortise
// packer flushes across multiple items.
//
// Symlinks, sockets, fifos, and other non-regular files are skipped with a
// log line; the run continues. Directories are recorded with their permission
// bits so Restore can recreate them with the same mode. Empty files produce
// a manifest entry with zero chunks (not an error).
func (h *FolderHandler) BackupChunked(ctx context.Context, item BackupItem, repo *dedup.Repo, progress ProgressFunc) (dedup.ID, error) {
	srcPath, _ := item.Settings["path"].(string)
	if srcPath == "" {
		return dedup.ID{}, fmt.Errorf("folder: missing path setting")
	}
	// Honour user exclusion patterns, matching the classic tar path
	// (tarDirectoryFiltered). For container volumes these arrive already
	// mapped to volume-relative paths via mapExclusionsToVolume.
	exclusions := extractExcludePaths(item.Settings)
	chunker, err := dedup.NewChunker(repo.SplitterSecret())
	if err != nil {
		return dedup.ID{}, err
	}

	// Open srcPath as a rooted handle so file opens during the walk go
	// through root.Open(rel) — race-safe against symlink TOCTOU traversal
	// (gosec G122). Matches the pattern used by tarDirectory in container.go.
	root, err := os.OpenRoot(srcPath)
	if err != nil {
		return dedup.ID{}, fmt.Errorf("folder: open source root: %w", err)
	}
	defer root.Close()

	m := dedup.Manifest{Version: dedup.ManifestVersion, Item: item.Name, Files: map[string]dedup.ManifestEntry{}}
	var totalBytes int64
	err = filepath.Walk(srcPath, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(srcPath, p)
		if rel == "." {
			return nil
		}
		// Skip excluded paths before recording or chunking. A matching
		// directory is pruned entirely (SkipDir) so its subtree is never
		// walked — common backup tools behave the same way (an absolute dir
		// pattern "is not saved and not traversed").
		if shouldExcludePath(rel, exclusions) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		entry := dedup.ManifestEntry{
			Mode:    uint32(info.Mode().Perm()),
			ModTime: info.ModTime().UTC().Format(time.RFC3339),
			Size:    info.Size(),
			IsDir:   info.IsDir(),
		}
		if info.IsDir() {
			m.Files[rel] = entry
			return nil
		}
		if !info.Mode().IsRegular() {
			log.Printf("engine: skipping non-regular file %s (mode %v)", rel, info.Mode())
			return nil
		}
		f, err := root.Open(rel)
		if err != nil {
			return err
		}
		defer f.Close()
		ids := []dedup.ID{}
		if err := chunker.Split(f, func(chunk []byte) error {
			id, err := repo.Put(chunk)
			if err != nil {
				return err
			}
			ids = append(ids, id)
			return nil
		}); err != nil {
			return fmt.Errorf("folder: chunk %s: %w", rel, err)
		}
		entry.Chunks = ids
		m.Files[rel] = entry
		totalBytes += info.Size()
		if progress != nil {
			progress(item.Name, -1, fmt.Sprintf("chunked %s", rel))
		}
		return nil
	})
	if err != nil {
		return dedup.ID{}, err
	}

	manifestID, err := repo.PutManifest(item.Name, m)
	if err != nil {
		return dedup.ID{}, err
	}
	if progress != nil {
		progress(item.Name, 100, fmt.Sprintf("manifest written (%d entries, %s)", len(m.Files), humanizeBytes(float64(totalBytes))))
	}
	return manifestID, nil
}

// RestoreChunked reads the Manifest at manifestID and reconstructs the file
// tree under destPath. Directories are restored first (sorted shallowest-to-
// deepest) with their recorded mode (defaulting to 0o755 when zero), then
// files are written in sorted order for determinism. Each file's chunks are
// fetched in order and concatenated; mtime is preserved via os.Chtimes.
// Empty files (zero chunks) are created as zero-byte files.
func (h *FolderHandler) RestoreChunked(ctx context.Context, item BackupItem, repo *dedup.Repo, manifestID dedup.ID, destPath string, progress ProgressFunc) error {
	m, err := repo.GetManifest(manifestID)
	if err != nil {
		return err
	}

	// Split entries into dirs and files so we can MkdirAll before writing
	// any file content. Manifest paths originated from filepath.Walk at
	// backup time so they should be clean relative paths, but the manifest
	// is stored off-host and could in principle be tampered with — every
	// entry path is therefore re-validated through safepath.JoinUnderBase
	// to guarantee the resulting path stays inside destPath (CWE-22).
	var dirs, files []string
	for p, e := range m.Files {
		if e.IsDir {
			dirs = append(dirs, p)
		} else {
			files = append(files, p)
		}
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		full, err := safepath.JoinUnderBase(destPath, d, true)
		if err != nil {
			return fmt.Errorf("restore mkdir %s: %w", d, err)
		}
		mode := os.FileMode(m.Files[d].Mode)
		if mode == 0 {
			mode = 0o755
		}
		if err := os.MkdirAll(full, mode); err != nil {
			return fmt.Errorf("restore mkdir %s: %w", d, err)
		}
	}

	sort.Strings(files)
	for _, fp := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e := m.Files[fp]
		full, err := safepath.JoinUnderBase(destPath, fp, false)
		if err != nil {
			return fmt.Errorf("restore %s: %w", fp, err)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(e.Mode)
		if mode == 0 {
			mode = 0o644
		}
		out, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) // #nosec G304 — full is validated by safepath.JoinUnderBase
		if err != nil {
			return err
		}
		for _, cid := range e.Chunks {
			body, err := repo.Get(cid)
			if err != nil {
				_ = out.Close()
				return fmt.Errorf("restore %s: %w", fp, err)
			}
			if _, err := out.Write(body); err != nil {
				_ = out.Close()
				return err
			}
		}
		if err := out.Close(); err != nil {
			return err
		}
		if t, err := time.Parse(time.RFC3339, e.ModTime); err == nil {
			_ = os.Chtimes(full, t, t)
		}
		if progress != nil {
			progress(item.Name, -1, fmt.Sprintf("restored %s", fp))
		}
	}
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

// extractExcludePaths reads the "exclude_paths" setting and returns it as a
// []string. The setting can arrive as []string (delegated from
// ContainerHandler.BackupChunked, which maps container-side exclusions to
// volume-relative paths) or []any (decoded straight from a job's JSON
// settings). Returns nil when absent so callers can use the result directly.
func extractExcludePaths(settings map[string]any) []string {
	raw, ok := settings["exclude_paths"]
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
