// Package tempdir manages temporary staging directories used during
// backup and restore operations. It provides a smart cascade strategy:
//
//  1. /mnt/cache/.vault-stage — SSD/NVMe cache (fastest)
//  2. <local-storage>/.vault-stage — array storage (avoids /tmp)
//  3. os.MkdirTemp fallback
//
// It also exposes CleanupStale for startup garbage-collection of
// directories left behind by crashed or interrupted runs.
package tempdir

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// StageDirName is the hidden directory name used for staging.
const StageDirName = ".vault-stage"

// CachePaths lists paths to try (in order) for fast SSD/NVMe-backed
// temporary staging. The first writable path wins.
var CachePaths = []string{
	"/mnt/cache",
}

// StorageConfig is the minimal config subset needed to extract a local path.
type StorageConfig struct {
	Type   string
	Config string // JSON blob from storage_destinations.config
}

// CreateBackupDir creates a temporary staging directory for a backup operation.
// It returns the path and a cleanup function that removes the temp dir and
// prunes its empty parent .vault-stage directory.
func CreateBackupDir(dest StorageConfig) (string, func(), error) {
	return createDir(dest, "backup-*")
}

// CreateRestoreDir creates a temporary staging directory for a restore operation.
// Same cascade strategy as CreateBackupDir.
func CreateRestoreDir(dest StorageConfig) (string, func(), error) {
	return createDir(dest, "restore-*")
}

// createDir implements the cascade strategy for both backup and restore dirs.
func createDir(dest StorageConfig, pattern string) (string, func(), error) {
	// Try fast cache/SSD paths first.
	for _, base := range CachePaths {
		info, err := os.Stat(base)
		if err != nil || !info.IsDir() {
			continue
		}
		stageBase := filepath.Join(base, StageDirName)
		if err := os.MkdirAll(stageBase, 0750); err == nil {
			dir, err := os.MkdirTemp(stageBase, pattern)
			if err == nil {
				log.Printf("tempdir: using cache staging dir %s", dir)
				return dir, cleanupFunc(dir, stageBase), nil
			}
		}
	}

	// Fall back to local storage path adjacent to the backup destination.
	if dest.Type == "local" {
		var cfg struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(dest.Config), &cfg); err == nil && cfg.Path != "" {
			stageBase := filepath.Join(cfg.Path, StageDirName)
			if err := os.MkdirAll(stageBase, 0750); err == nil {
				dir, err := os.MkdirTemp(stageBase, pattern)
				if err == nil {
					log.Printf("tempdir: using local storage staging dir %s", dir)
					return dir, cleanupFunc(dir, stageBase), nil
				}
			}
		}
	}

	// System temp dir fallback.
	dir, err := os.MkdirTemp("", "vault-"+strings.TrimSuffix(pattern, "*")+"*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp dir: %w", err)
	}
	log.Printf("tempdir: using system temp dir %s", dir)
	return dir, func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("tempdir: warning: failed to remove %s: %v", dir, err)
		}
	}, nil
}

// cleanupFunc returns a function that removes the temp directory and prunes
// its parent .vault-stage directory if it is empty afterwards.
func cleanupFunc(dir, stageBase string) func() {
	return func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("tempdir: warning: failed to remove %s: %v", dir, err)
		}
		pruneEmpty(stageBase)
	}
}

// pruneEmpty removes a directory only if it is empty.
func pruneEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	if len(entries) == 0 {
		if err := os.Remove(dir); err != nil {
			log.Printf("tempdir: warning: failed to prune empty dir %s: %v", dir, err)
		}
	}
}

// CleanupStale removes leftover staging directories from all known locations.
// Call this at daemon startup to garbage-collect dirs from crashed runs.
func CleanupStale(destinations []StorageConfig) {
	seen := make(map[string]bool)

	// Scan cache paths.
	for _, base := range CachePaths {
		stageBase := filepath.Join(base, StageDirName)
		if seen[stageBase] {
			continue
		}
		seen[stageBase] = true
		cleanStageDir(stageBase)
	}

	// Scan local storage paths.
	for _, dest := range destinations {
		if dest.Type != "local" {
			continue
		}
		var cfg struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(dest.Config), &cfg); err != nil || cfg.Path == "" {
			continue
		}
		stageBase := filepath.Join(cfg.Path, StageDirName)
		if seen[stageBase] {
			continue
		}
		seen[stageBase] = true
		cleanStageDir(stageBase)
	}

	// Also clean up legacy .vault-tmp directories.
	cleanLegacyDirs(seen)
}

// cleanStageDir removes all subdirectories inside a .vault-stage directory,
// then prunes the directory itself if empty.
func cleanStageDir(stageBase string) {
	entries, err := os.ReadDir(stageBase)
	if err != nil {
		return // directory doesn't exist or unreadable — nothing to clean
	}

	for _, entry := range entries {
		p := filepath.Join(stageBase, entry.Name())
		if err := os.RemoveAll(p); err != nil {
			log.Printf("tempdir: warning: failed to clean stale dir %s: %v", p, err)
		} else {
			log.Printf("tempdir: cleaned stale staging dir %s", p)
		}
	}

	pruneEmpty(stageBase)
}

// cleanLegacyDirs removes leftover .vault-tmp directories from the old naming
// scheme. Uses the same cache paths as the current implementation.
func cleanLegacyDirs(seen map[string]bool) {
	for _, base := range CachePaths {
		legacyBase := filepath.Join(base, ".vault-tmp")
		if seen[legacyBase] {
			continue
		}
		seen[legacyBase] = true
		cleanStageDir(legacyBase)
	}
}
