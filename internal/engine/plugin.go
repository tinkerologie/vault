//go:build linux

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// pluginsDir is the base directory where Unraid plugins are installed.
const pluginsDir = "/boot/config/plugins"

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

	var items []BackupItem
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
	configDir := filepath.Join(pluginsDir, pluginName)
	if info, err := os.Stat(configDir); err == nil && info.IsDir() {
		archivePath := filepath.Join(destDir, "config.tar.gz")
		if err := tarDirectory(ctx, configDir, archivePath, nil); err != nil {
			return nil, fmt.Errorf("archiving plugin config: %w", err)
		}
		result.Files = append(result.Files, backupFileInfo(archivePath))
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
	configArchive := filepath.Join(sourceDir, "config.tar.gz")
	if _, err := os.Stat(configArchive); err == nil {
		configDir := filepath.Join(pluginsDir, pluginName)
		if err := os.MkdirAll(configDir, 0755); err != nil {
			return fmt.Errorf("creating config dir: %w", err)
		}
		if err := untarDirectory(ctx, configArchive, configDir); err != nil { // untarDirectory has Zip Slip (CWE-22) protection via joinArchiveTarget + resolveWithinBase
			return fmt.Errorf("restoring plugin config: %w", err)
		}
	}

	progress(item.Name, 100, "restore complete")
	return nil
}
