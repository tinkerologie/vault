//go:build linux

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// pluginsDir is the base directory where Unraid plugins are installed. It is a
// var (not a const) only so tests can redirect plugin reads/writes to a
// temporary directory; production never reassigns it.
var pluginsDir = "/boot/config/plugins"

// pluginPath returns the canonical config directory for a plugin under
// /boot/config/plugins/. Shared by Backup (tar) and BackupChunked (dedup) so
// both code paths agree on what the plugin's "data" lives in.
func pluginPath(name string) string {
	if name == "" {
		return ""
	}
	return filepath.Join(pluginsDir, name)
}

// PluginHandler implements Handler for Unraid plugin backup/restore.
// Each plugin consists of a .plg installer file and an optional per-plugin
// configuration directory under /boot/config/plugins/<name>/.
type PluginHandler struct{}

// NewPluginHandler creates a new PluginHandler.
func NewPluginHandler() (*PluginHandler, error) {
	if _, err := os.Stat(pluginsDir); err != nil {
		return nil, fmt.Errorf("plugins directory not accessible: %w", err)
	}
	return &PluginHandler{}, nil
}

// ListItems scans /boot/config/plugins/ for .plg files and returns each
// as a BackupItem. The item name is the plugin name (filename without .plg).
func (h *PluginHandler) ListItems() ([]BackupItem, error) {
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		return nil, fmt.Errorf("reading plugins directory: %w", err)
	}

	items := make([]BackupItem, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".plg") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".plg")
		plgPath := filepath.Join(pluginsDir, entry.Name())

		// Check if a per-plugin config directory exists.
		configDir := filepath.Join(pluginsDir, name)
		hasConfig := false
		if info, statErr := os.Stat(configDir); statErr == nil && info.IsDir() {
			hasConfig = true
		}

		items = append(items, BackupItem{
			Name: name,
			Type: "plugin",
			Settings: map[string]any{
				"id":         name,
				"plg_path":   plgPath,
				"config_dir": configDir,
				"has_config": hasConfig,
			},
		})
	}
	return items, nil
}

// Backup creates a tar.gz archive containing the plugin's .plg file and its
// configuration directory (if it exists).
func (h *PluginHandler) Backup(ctx context.Context, item BackupItem, destDir string, progress ProgressFunc) (*BackupResult, error) {
	result := &BackupResult{ItemName: item.Name}

	pluginName, _ := item.Settings["id"].(string)
	if pluginName == "" {
		pluginName = item.Name
	}

	// Validate plugin name to prevent path traversal (CWE-22).
	safePluginName, err := normalizeRestoreComponent(pluginName)
	if err != nil {
		return nil, fmt.Errorf("invalid plugin name: %w", err)
	}
	pluginName = safePluginName

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("creating dest dir: %w", err)
	}

	// Step 1: Copy the .plg file.
	progress(item.Name, 10, "copying plugin file")
	plgSrc := filepath.Join(pluginsDir, pluginName+".plg")
	plgDst := filepath.Join(destDir, pluginName+".plg")
	if _, err := os.Stat(plgSrc); err != nil {
		return nil, fmt.Errorf("plugin file not found: %w", err)
	}
	data, err := os.ReadFile(plgSrc) // #nosec G304 — pluginName validated by normalizeRestoreComponent above
	if err != nil {
		return nil, fmt.Errorf("reading plugin file: %w", err)
	}
	if err := os.WriteFile(plgDst, data, 0644); err != nil {
		return nil, fmt.Errorf("writing plugin file: %w", err)
	}
	result.Files = append(result.Files, backupFileInfo(plgDst))

	// Step 2: Archive the config directory if it exists.
	progress(item.Name, 40, "archiving config")
	configDir := pluginPath(pluginName)
	if info, err := os.Stat(configDir); err == nil && info.IsDir() {
		effectiveCompression := MaybeDowngradeCompression(configDir, item.Compression)
		archivePath := filepath.Join(destDir, "config.tar"+archiveExt(effectiveCompression))
		if err := tarDirectory(ctx, configDir, archivePath, nil, effectiveCompression); err != nil {
			return nil, fmt.Errorf("archiving plugin config: %w", err)
		}
		result.Files = append(result.Files, backupFileInfo(archivePath))
		if err := WriteTarIndex(archivePath); err == nil {
			result.Files = append(result.Files, backupFileInfo(archivePath+IndexSuffix))
		}
	}

	// Step 3: Save metadata.
	progress(item.Name, 80, "saving metadata")
	meta := map[string]string{
		"name":       pluginName,
		"plg_file":   pluginName + ".plg",
		"config_dir": configDir,
	}
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
	metaPath := filepath.Join(destDir, "plugin_meta.json")
	if err := os.WriteFile(metaPath, metaJSON, 0644); err != nil {
		return nil, fmt.Errorf("writing plugin metadata: %w", err)
	}
	result.Files = append(result.Files, backupFileInfo(metaPath))

	progress(item.Name, 100, "backup complete")
	result.Success = true
	return result, nil
}

// Restore extracts the plugin's .plg file and config directory back to
// /boot/config/plugins/. The plugin will be recognized on next Unraid boot
// or when the Plugins page is refreshed.
func (h *PluginHandler) Restore(ctx context.Context, item BackupItem, sourceDir string, progress ProgressFunc) error {
	progress(item.Name, 10, "reading metadata")

	pluginName := item.Name

	// Try to read metadata for plugin name.
	metaPath := filepath.Join(sourceDir, "plugin_meta.json")
	if data, err := os.ReadFile(metaPath); err == nil { // #nosec G304 — metaPath is sourceDir (caller-controlled temp dir) + fixed filename
		var meta struct {
			Name string `json:"name"`
		}
		if jsonErr := json.Unmarshal(data, &meta); jsonErr == nil && meta.Name != "" {
			pluginName = meta.Name
		}
	}

	safePluginName, err := normalizeRestoreComponent(pluginName)
	if err != nil {
		return err
	}
	pluginName = safePluginName

	// Step 1: Restore the .plg file.
	progress(item.Name, 30, "restoring plugin file")
	plgSrc := filepath.Join(sourceDir, pluginName+".plg")
	if data, err := os.ReadFile(plgSrc); err == nil { // #nosec G304 — pluginName validated by normalizeRestoreComponent above
		plgDst := filepath.Join(pluginsDir, pluginName+".plg")
		if err := os.WriteFile(plgDst, data, 0644); err != nil {
			return fmt.Errorf("writing plugin file: %w", err)
		}
	} else {
		return fmt.Errorf("plugin file not found in backup: %w", err)
	}

	// Step 2: Restore config directory.
	progress(item.Name, 60, "restoring config")
	if configArchive, err := findArchive(sourceDir, "config.tar"); err == nil {
		configDir := pluginPath(pluginName)
		if err := os.MkdirAll(configDir, 0755); err != nil {
			return fmt.Errorf("creating config dir: %w", err)
		}
		include := extractRestoreFilePaths(item.Settings)
		if err := untarDirectoryFiltered(ctx, configArchive, configDir, include); err != nil { // untarDirectoryFiltered inherits Zip Slip (CWE-22) protection via joinArchiveTarget + resolveWithinBase
			return fmt.Errorf("restoring plugin config: %w", err)
		}
	}

	progress(item.Name, 100, "restore complete")
	return nil
}

// pluginPlgFilePath resolves where the plugin's .plg installer lives:
// <pluginsDir>/<name>.plg. Tests redirect it by overriding the pluginsDir var.
func pluginPlgFilePath(name string) string {
	if name == "" {
		return ""
	}
	return filepath.Join(pluginsDir, name+".plg")
}

// BackupChunked walks the plugin's config directory tree and chunks every
// regular file into the dedup repo, then records the plugin's .plg installer
// file in the manifest's Installer field so a dedup-only restore can reinstate
// it. Delegates the config directory to FolderHandler's manifest builder —
// plugins are folder-shaped under /boot/config/plugins/<name>/.
//
// item.Settings["path"] overrides the default config path lookup (used by
// tests so they can point at a t.TempDir() instead of /boot/config/plugins/);
// in production the runner does not set it and pluginPath(item.Name) applies.
//
// When no .plg file is present (a plugin without an installer, or tests) the
// manifest holds only the config files, exactly as the folder backup produced
// before this change, so existing restore points are unaffected.
func (h *PluginHandler) BackupChunked(ctx context.Context, item BackupItem, repo *dedup.Repo, progress ProgressFunc) (dedup.ID, error) {
	src, _ := item.Settings["path"].(string)
	if src == "" {
		src = pluginPath(item.Name)
	}
	if src == "" {
		return dedup.ID{}, fmt.Errorf("plugin: cannot resolve directory for %q", item.Name)
	}
	proxy := BackupItem{Name: item.Name, Type: "folder", Settings: map[string]any{"path": src}}
	fh := &FolderHandler{}
	m, _, _, err := fh.buildChunkedManifest(ctx, proxy, repo, progress)
	if err != nil {
		return dedup.ID{}, fmt.Errorf("plugin: chunking config for %q: %w", item.Name, err)
	}

	// Record the .plg installer out-of-tree in the manifest (best-effort).
	// Attached before PutManifest because the config manifest is not yet
	// flushed and so cannot be read back mid-run.
	if plgData, rErr := os.ReadFile(pluginPlgFilePath(item.Name)); rErr == nil { // #nosec G304 — path is pluginsDir + item name, no request-controlled component
		chunkID, pErr := repo.Put(plgData)
		if pErr != nil {
			return dedup.ID{}, fmt.Errorf("plugin: storing .plg for %q: %w", item.Name, pErr)
		}
		m.Installer = &dedup.ManifestEntry{Size: int64(len(plgData)), Chunks: []dedup.ID{chunkID}}
	}
	manifestID, err := repo.PutManifest(item.Name, m)
	if err != nil {
		return dedup.ID{}, fmt.Errorf("plugin: writing manifest for %q: %w", item.Name, err)
	}
	return manifestID, nil
}

// RestoreChunked reconstructs the plugin's config directory tree from the
// manifest and, when the backup captured one, restores the .plg installer
// file. destPath selects the config restore directory: the runner passes it
// explicitly, but when empty it falls back to the item's restore_destination
// setting and then to the plugin's well-known directory
// (pluginPath(item.Name)) so production restore lands in
// /boot/config/plugins/<name>/. Tests pass a t.TempDir().
func (h *PluginHandler) RestoreChunked(ctx context.Context, item BackupItem, repo *dedup.Repo, manifestID dedup.ID, destPath string, progress ProgressFunc) error {
	if destPath == "" {
		// Mirror ContainerHandler.RestoreChunked: honour restore_destination as
		// a fallback before the well-known directory, so the parameter and the
		// setting cannot drift apart.
		if rd, _ := item.Settings["restore_destination"].(string); rd != "" {
			destPath = rd
		} else {
			destPath = pluginPath(item.Name)
		}
	}
	if destPath == "" {
		return fmt.Errorf("plugin: cannot resolve restore directory for %q", item.Name)
	}
	// Propagate the partial-restore file picker through the proxy item so a
	// selection made on a dedup plugin restore reaches FolderHandler.
	// RestoreChunked, which honours restore_file_paths. Without this the
	// setting is dropped and the whole config directory is restored.
	proxySettings := map[string]any{"path": destPath}
	if rfp := extractRestoreFilePaths(item.Settings); rfp != nil {
		proxySettings["restore_file_paths"] = rfp
	}
	proxy := BackupItem{Name: item.Name, Type: "folder", Settings: proxySettings}
	fh := &FolderHandler{}
	if err := fh.RestoreChunked(ctx, proxy, repo, manifestID, destPath, progress); err != nil {
		return fmt.Errorf("plugin: restoring config for %q: %w", item.Name, err)
	}
	return h.restorePluginInstaller(ctx, item, repo, manifestID)
}

// restorePluginInstaller writes the plugin's .plg installer back to its
// well-known location (<pluginsDir>/<name>.plg) when the manifest carries one.
// Manifests written before the installer was captured have no Installer entry
// and this is a no-op.
func (h *PluginHandler) restorePluginInstaller(ctx context.Context, item BackupItem, repo *dedup.Repo, manifestID dedup.ID) error {
	m, err := repo.GetManifest(manifestID)
	if err != nil {
		return fmt.Errorf("plugin: reading manifest for %q: %w", item.Name, err)
	}
	if m.Installer == nil || len(m.Installer.Chunks) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := repo.Get(m.Installer.Chunks[0])
	if err != nil {
		return fmt.Errorf("plugin: reading stored .plg for %q: %w", item.Name, err)
	}

	// The destination base is a package var and the name is sanitised, so no
	// request-controlled value reaches the path.
	safeName, err := normalizeRestoreComponent(item.Name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dst := filepath.Join(pluginsDir, safeName+".plg")
	if err := os.WriteFile(dst, data, 0644); err != nil { // #nosec G306 — .plg is installed world-readable, matching classic Restore
		return fmt.Errorf("plugin: writing .plg for %q: %w", item.Name, err)
	}
	return nil
}
