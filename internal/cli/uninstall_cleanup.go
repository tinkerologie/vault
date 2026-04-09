package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/tempdir"
	"github.com/ruaan-deysel/vault/internal/unraid"
	"github.com/spf13/cobra"
)

const legacyStageDirName = ".vault-tmp"

type uninstallCleanupConfig struct {
	DBPath            string
	ConfigDir         string
	BinaryPath        string
	RCScriptPath      string
	PluginDir         string
	LogPath           string
	PIDFile           string
	HybridWorkingDir  string
	DefaultSnapshotDB string
	CachePaths        []string
}

type uninstallCleanupState struct {
	Confident         bool
	PreserveRoots     []string
	SnapshotOverride  string
	StagingOverride   string
	DiscoverySourceDB string
}

var cleanupUninstallCmd = &cobra.Command{
	Use:    "cleanup-uninstall",
	Short:  "Remove Vault-managed uninstall traces while preserving backup data",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := defaultUninstallCleanupConfig()

		cfg.DBPath, _ = cmd.Flags().GetString("db")
		cfg.ConfigDir, _ = cmd.Flags().GetString("config-dir")
		cfg.BinaryPath, _ = cmd.Flags().GetString("binary-path")
		cfg.RCScriptPath, _ = cmd.Flags().GetString("rc-script")
		cfg.PluginDir, _ = cmd.Flags().GetString("plugin-dir")
		cfg.LogPath, _ = cmd.Flags().GetString("log-path")
		cfg.PIDFile, _ = cmd.Flags().GetString("pid-file")
		cfg.HybridWorkingDir, _ = cmd.Flags().GetString("hybrid-working-dir")
		if cmd.Flags().Changed("snapshot-path") {
			cfg.DefaultSnapshotDB, _ = cmd.Flags().GetString("snapshot-path")
		}

		return runUninstallCleanup(cfg)
	},
}

func init() {
	cleanupUninstallCmd.Flags().String("db", "/boot/config/plugins/vault/vault.db", "Persistent Vault database path")
	cleanupUninstallCmd.Flags().String("config-dir", "/boot/config/plugins/vault", "Vault config directory")
	cleanupUninstallCmd.Flags().String("binary-path", "/usr/local/sbin/vault", "Vault binary path")
	cleanupUninstallCmd.Flags().String("rc-script", "/etc/rc.d/rc.vault", "Vault RC script path")
	cleanupUninstallCmd.Flags().String("plugin-dir", "/usr/local/emhttp/plugins/vault", "Vault Unraid plugin directory")
	cleanupUninstallCmd.Flags().String("log-path", "/var/log/vault.log", "Vault log path")
	cleanupUninstallCmd.Flags().String("pid-file", "/var/run/vault.pid", "Vault PID file path")
	cleanupUninstallCmd.Flags().String("hybrid-working-dir", "/var/local/vault", "Hybrid-mode working database directory")
	cleanupUninstallCmd.Flags().String("snapshot-path", "/mnt/cache/.vault/vault.db", "Default hybrid-mode snapshot database path")
	rootCmd.AddCommand(cleanupUninstallCmd)
}

func defaultUninstallCleanupConfig() uninstallCleanupConfig {
	paths := tempdir.GetCachePaths()

	snapshotDB := "/mnt/cache/.vault/vault.db"
	if pool := unraid.PreferredPool(); pool != "" {
		snapshotDB = filepath.Join(pool, ".vault", "vault.db")
	}

	return uninstallCleanupConfig{
		DBPath:            "/boot/config/plugins/vault/vault.db",
		ConfigDir:         "/boot/config/plugins/vault",
		BinaryPath:        "/usr/local/sbin/vault",
		RCScriptPath:      "/etc/rc.d/rc.vault",
		PluginDir:         "/usr/local/emhttp/plugins/vault",
		LogPath:           "/var/log/vault.log",
		PIDFile:           "/var/run/vault.pid",
		HybridWorkingDir:  "/var/local/vault",
		DefaultSnapshotDB: snapshotDB,
		CachePaths:        paths,
	}
}

func runUninstallCleanup(cfg uninstallCleanupConfig) error {
	state, err := discoverUninstallCleanupState(cfg)
	if err != nil {
		log.Printf("cleanup-uninstall: proceeding with safe fallback after discovery warning: %v", err)
	} else if state.Confident {
		log.Printf("cleanup-uninstall: preserving configured backup roots discovered from %s", state.DiscoverySourceDB)
		for _, root := range state.PreserveRoots {
			log.Printf("cleanup-uninstall: preserving backup root %s", root)
		}
	}

	for _, path := range managedStagePaths(cfg, state) {
		if err := removeAll(path); err != nil {
			return err
		}
	}

	for _, path := range []string{cfg.PluginDir, cfg.LogPath, cfg.PIDFile, cfg.RCScriptPath, cfg.BinaryPath} {
		if err := removeAll(path); err != nil {
			return err
		}
	}

	if err := removeAll(cfg.HybridWorkingDir); err != nil {
		return err
	}

	for _, path := range managedSnapshotPaths(cfg, state) {
		if err := removeDatabaseArtifacts(path); err != nil {
			return err
		}
	}

	if err := removeConfigArtifacts(cfg); err != nil {
		return err
	}

	if err := cleanupConfigDir(cfg, state); err != nil {
		return err
	}

	return nil
}

func discoverUninstallCleanupState(cfg uninstallCleanupConfig) (uninstallCleanupState, error) {
	state := uninstallCleanupState{}
	var warnings []string

	for _, candidate := range cleanupDiscoveryCandidates(cfg) {
		if candidate == "" {
			continue
		}

		info, statErr := os.Stat(candidate)
		if statErr != nil {
			if !errors.Is(statErr, os.ErrNotExist) {
				warnings = append(warnings, fmt.Sprintf("stat %s: %v", candidate, statErr))
			}
			continue
		}
		if info.IsDir() {
			continue
		}

		database, openErr := db.Open(candidate)
		if openErr != nil {
			warnings = append(warnings, fmt.Sprintf("open %s: %v", candidate, openErr))
			continue
		}

		dests, listErr := database.ListStorageDestinations()
		if listErr != nil {
			_ = database.Close()
			warnings = append(warnings, fmt.Sprintf("list storage destinations from %s: %v", candidate, listErr))
			continue
		}

		snapshotOverride, _ := database.GetSetting("snapshot_path_override", "")
		stagingOverride, _ := database.GetSetting("staging_dir_override", "")
		_ = database.Close()

		state.Confident = true
		state.PreserveRoots = normalizeUniquePaths(localStorageRoots(dests))
		state.SnapshotOverride = normalizePath(snapshotOverride)
		state.StagingOverride = normalizePath(stagingOverride)
		state.DiscoverySourceDB = candidate
		return state, nil
	}

	if len(warnings) > 0 {
		return state, errors.New(strings.Join(warnings, "; "))
	}

	return state, nil
}

func cleanupDiscoveryCandidates(cfg uninstallCleanupConfig) []string {
	return orderedUniquePaths([]string{
		cfg.DBPath,
		filepath.Join(cfg.HybridWorkingDir, "vault.db"),
		cfg.DefaultSnapshotDB,
	})
}

func managedStagePaths(cfg uninstallCleanupConfig, state uninstallCleanupState) []string {
	paths := make([]string, 0, len(cfg.CachePaths)+len(state.PreserveRoots)+2)
	for _, cachePath := range cfg.CachePaths {
		cachePath = normalizePath(cachePath)
		if cachePath == "" {
			continue
		}
		paths = append(paths,
			filepath.Join(cachePath, tempdir.StageDirName),
			filepath.Join(cachePath, legacyStageDirName),
		)
	}
	for _, root := range state.PreserveRoots {
		paths = append(paths, filepath.Join(root, tempdir.StageDirName))
	}
	if state.StagingOverride != "" {
		paths = append(paths, filepath.Join(state.StagingOverride, tempdir.StageDirName))
	}
	return normalizeUniquePaths(paths)
}

func managedSnapshotPaths(cfg uninstallCleanupConfig, state uninstallCleanupState) []string {
	paths := []string{cfg.DefaultSnapshotDB}
	if state.SnapshotOverride != "" {
		paths = append(paths, state.SnapshotOverride)
	}
	return orderedUniquePaths(paths)
}

func removeConfigArtifacts(cfg uninstallCleanupConfig) error {
	paths := []string{
		filepath.Join(cfg.ConfigDir, "vault.cfg"),
		filepath.Join(cfg.ConfigDir, "vault.key"),
		cfg.DBPath,
	}
	for _, path := range paths {
		if err := removeDatabaseArtifacts(path); err != nil {
			return err
		}
	}

	tgzMatches, err := filepath.Glob(filepath.Join(cfg.ConfigDir, "*.tgz"))
	if err != nil {
		return fmt.Errorf("glob package artifacts: %w", err)
	}
	for _, match := range tgzMatches {
		if err := removeAll(match); err != nil {
			return err
		}
	}

	txzMatches, err := filepath.Glob(filepath.Join(cfg.ConfigDir, "*.txz"))
	if err != nil {
		return fmt.Errorf("glob package artifacts: %w", err)
	}
	for _, match := range txzMatches {
		if err := removeAll(match); err != nil {
			return err
		}
	}

	return nil
}

func cleanupConfigDir(cfg uninstallCleanupConfig, state uninstallCleanupState) error {
	configDir := normalizePath(cfg.ConfigDir)
	if configDir == "" {
		return nil
	}

	if !state.Confident {
		pruneEmpty(configDir)
		return nil
	}

	if isExactPreserveRoot(configDir, state.PreserveRoots) {
		pruneEmpty(configDir)
		return nil
	}

	if !hasPreservedDescendant(configDir, state.PreserveRoots) {
		return removeAll(configDir)
	}

	if err := removeDirContentsExceptPreserved(configDir, state.PreserveRoots); err != nil {
		return err
	}
	pruneEmpty(configDir)
	return nil
}

func removeDirContentsExceptPreserved(dir string, preserveRoots []string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read directory %s: %w", dir, err)
	}

	for _, entry := range entries {
		child := filepath.Join(dir, entry.Name())
		normalizedChild := normalizePath(child)
		if normalizedChild == "" {
			continue
		}

		if isExactPreserveRoot(normalizedChild, preserveRoots) {
			continue
		}
		if hasPreservedDescendant(normalizedChild, preserveRoots) {
			if err := removeDirContentsExceptPreserved(normalizedChild, preserveRoots); err != nil {
				return err
			}
			pruneEmpty(normalizedChild)
			continue
		}

		if err := removeAll(normalizedChild); err != nil {
			return err
		}
	}

	return nil
}

func removeDatabaseArtifacts(path string) error {
	path = normalizePath(path)
	if path == "" {
		return nil
	}

	for _, candidate := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if err := removeAll(candidate); err != nil {
			return err
		}
	}

	pruneEmpty(filepath.Dir(path))
	return nil
}

func removeAll(path string) error {
	path = normalizePath(path)
	if path == "" {
		return nil
	}
	if err := os.RemoveAll(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func pruneEmpty(path string) {
	path = normalizePath(path)
	if path == "" {
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}
	if len(entries) != 0 {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("cleanup-uninstall: warning: failed to prune %s: %v", path, err)
	}
}

func localStorageRoots(dests []db.StorageDestination) []string {
	roots := make([]string, 0, len(dests))
	for _, dest := range dests {
		if dest.Type != "local" {
			continue
		}
		var cfg struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(dest.Config), &cfg); err != nil {
			continue
		}
		if cfg.Path != "" {
			roots = append(roots, cfg.Path)
		}
	}
	return roots
}

func normalizeUniquePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		path = normalizePath(path)
		if path == "" {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		normalized = append(normalized, path)
	}
	sort.Strings(normalized)
	return normalized
}

func orderedUniquePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		path = normalizePath(path)
		if path == "" {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		normalized = append(normalized, path)
	}
	return normalized
}

func normalizePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(absPath)
}

func isExactPreserveRoot(path string, preserveRoots []string) bool {
	path = normalizePath(path)
	for _, root := range preserveRoots {
		if path == root {
			return true
		}
	}
	return false
}

func hasPreservedDescendant(path string, preserveRoots []string) bool {
	path = normalizePath(path)
	if path == "" {
		return false
	}
	prefix := path + string(os.PathSeparator)
	for _, root := range preserveRoots {
		if strings.HasPrefix(root, prefix) {
			return true
		}
	}
	return false
}
