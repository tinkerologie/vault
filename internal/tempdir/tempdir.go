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
	"sync"
	"syscall"

	"github.com/ruaan-deysel/vault/internal/unraid"
)

// StageDirName is the hidden directory name used for staging.
const StageDirName = ".vault-stage"

// cachePaths returns pool paths to try (in order) for fast SSD/NVMe-backed
// temporary staging. Uses dynamic pool discovery so mirrored or
// non-standard pool names are included.
//
// Tests can override via SetCachePathsForTest.
var (
	cachePathsMu   sync.Mutex
	cachePathsVal  []string
	cachePathsDone bool
	cachePathsTest []string // non-nil when overridden by tests
)

func cachePaths() []string {
	cachePathsMu.Lock()
	defer cachePathsMu.Unlock()

	if cachePathsTest != nil {
		return cachePathsTest
	}

	// Don't permanently cache an empty result — pools may not be
	// mounted yet at early startup. Retry discovery on next call.
	if !cachePathsDone {
		cachePathsVal = unraid.DiscoverPools()
		if len(cachePathsVal) > 0 {
			cachePathsDone = true
		}
	}
	return cachePathsVal
}

// SetCachePathsForTest overrides the cache paths used by this package.
// Returns a cleanup function that restores the original behaviour.
// This is intended for use in tests only.
func SetCachePathsForTest(paths []string) func() {
	cachePathsMu.Lock()
	cachePathsTest = paths
	cachePathsMu.Unlock()
	return func() {
		cachePathsMu.Lock()
		cachePathsTest = nil
		cachePathsMu.Unlock()
	}
}

// GetCachePaths returns the current pool/cache paths used for staging.
// This is intended for external callers that need the resolved list.
func GetCachePaths() []string {
	return cachePaths()
}

// StorageConfig is the minimal config subset needed to extract a local path.
type StorageConfig struct {
	Type   string
	Config string // JSON blob from storage_destinations.config
}

// StagingInfo contains information about the resolved staging directory.
type StagingInfo struct {
	ResolvedPath   string        `json:"resolved_path"`
	Source         string        `json:"source"`
	Override       string        `json:"override"`
	DiskFreeBytes  uint64        `json:"disk_free_bytes"`
	DiskTotalBytes uint64        `json:"disk_total_bytes"`
	Cascade        []CascadeItem `json:"cascade"`
}

// CascadeItem represents one level of the staging cascade.
type CascadeItem struct {
	Path      string `json:"path"`
	Available bool   `json:"available"`
	Source    string `json:"source"`
}

// CreateBackupDir creates a temporary staging directory for a backup operation.
// It returns the path and a cleanup function that removes the temp dir and
// prunes its empty parent .vault-stage directory.
func CreateBackupDir(dest StorageConfig, override string) (string, func(), error) {
	return createDir(dest, "backup-*", override)
}

// CreateRestoreDir creates a temporary staging directory for a restore operation.
// Same cascade strategy as CreateBackupDir.
func CreateRestoreDir(dest StorageConfig, override string) (string, func(), error) {
	return createDir(dest, "restore-*", override)
}

// createDir implements the cascade strategy for both backup and restore dirs.
func createDir(dest StorageConfig, pattern string, override string) (string, func(), error) {
	// Try override first.
	if override != "" {
		if info, err := os.Stat(override); err == nil && info.IsDir() {
			stageBase := filepath.Join(override, StageDirName)
			if err := os.MkdirAll(stageBase, 0750); err == nil {
				dir, err := os.MkdirTemp(stageBase, pattern)
				if err == nil {
					return dir, cleanupFunc(dir, stageBase), nil
				}
			}
		}
		log.Printf("tempdir: override path %s unusable, falling back to cascade", override)
	}

	// Try fast cache/SSD paths first.
	for _, base := range cachePaths() {
		info, err := os.Stat(base)
		if err != nil || !info.IsDir() {
			continue
		}
		stageBase := filepath.Join(base, StageDirName)
		if err := os.MkdirAll(stageBase, 0750); err == nil {
			dir, err := os.MkdirTemp(stageBase, pattern)
			if err == nil {
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
	for _, base := range cachePaths() {
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
	for _, base := range cachePaths() {
		legacyBase := filepath.Join(base, ".vault-tmp")
		if seen[legacyBase] {
			continue
		}
		seen[legacyBase] = true
		cleanStageDir(legacyBase)
	}
}

// ResolveInfo returns information about which staging directory would be used,
// without actually creating any directories.
func ResolveInfo(destinations []StorageConfig, override string) StagingInfo {
	info := StagingInfo{Override: override}

	// Build cascade list.
	for _, base := range cachePaths() {
		stagePath := filepath.Join(base, StageDirName)
		ci := CascadeItem{Path: stagePath, Source: "cache"}
		if fi, err := os.Stat(base); err == nil && fi.IsDir() {
			ci.Available = true
		}
		info.Cascade = append(info.Cascade, ci)
	}
	for _, dest := range destinations {
		if dest.Type != "local" {
			continue
		}
		var cfg struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(dest.Config), &cfg); err == nil && cfg.Path != "" {
			stagePath := filepath.Join(cfg.Path, StageDirName)
			ci := CascadeItem{Path: stagePath, Source: "local-storage"}
			if fi, err := os.Stat(cfg.Path); err == nil && fi.IsDir() {
				ci.Available = true
			}
			info.Cascade = append(info.Cascade, ci)
		}
	}
	info.Cascade = append(info.Cascade, CascadeItem{Path: os.TempDir(), Available: true, Source: "system"})

	// Resolve which path wins.
	if override != "" {
		if fi, err := os.Stat(override); err == nil && fi.IsDir() {
			info.ResolvedPath = override
			info.Source = "override"
		}
	}
	if info.ResolvedPath == "" {
		for _, ci := range info.Cascade {
			if ci.Available {
				info.ResolvedPath = ci.Path
				info.Source = ci.Source
				break
			}
		}
	}

	// Get disk space.
	if info.ResolvedPath != "" {
		info.DiskFreeBytes, info.DiskTotalBytes = diskSpace(info.ResolvedPath)
	}

	return info
}

func diskSpace(path string) (free, total uint64) {
	// Walk up to an existing directory if the path doesn't exist yet.
	p := path
	for p != "" && p != "/" {
		if _, err := os.Stat(p); err == nil {
			break
		}
		p = filepath.Dir(p)
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(p, &stat); err != nil {
		return 0, 0
	}
	total = stat.Blocks * uint64(stat.Bsize)
	free = stat.Bavail * uint64(stat.Bsize)
	return free, total
}
