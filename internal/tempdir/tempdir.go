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
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/tinkerologie/vault/internal/fsstat"
	"github.com/tinkerologie/vault/internal/safepath"
	"github.com/tinkerologie/vault/internal/unraid"
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

// PrependCachePaths adds paths to the front of the cache paths list so they
// are tried first in the staging cascade. Duplicate paths are skipped.
// Paths are normalized with filepath.Clean; "/" is rejected.
// Discovery is triggered first (if not already completed) so the normal
// pool cascade is preserved rather than being shadowed.
// If test overrides are active (via SetCachePathsForTest), the prepend
// targets the test slice so callers see the prepended paths.
func PrependCachePaths(paths []string) {
	if len(paths) == 0 {
		return
	}
	cachePathsMu.Lock()
	defer cachePathsMu.Unlock()

	// Determine the target slice: test override or production.
	target := &cachePathsVal
	if cachePathsTest != nil {
		target = &cachePathsTest
	} else if !cachePathsDone {
		// Run discovery first so we merge with (not replace) the
		// conventional Unraid pool cascade.
		discovered := unraid.DiscoverPools()
		if len(discovered) > 0 {
			cachePathsVal = discovered
			cachePathsDone = true
		}
	}

	existing := make(map[string]bool, len(*target))
	for _, p := range *target {
		existing[filepath.Clean(p)] = true
	}

	var toAdd []string
	for _, p := range paths {
		cleaned := filepath.Clean(p)
		if cleaned == "/" {
			continue
		}
		if !existing[cleaned] {
			toAdd = append(toAdd, cleaned)
			existing[cleaned] = true
		}
	}

	if len(toAdd) > 0 {
		*target = append(toAdd, *target...)
	}
}

// StorageConfig is the minimal config subset needed to extract a local path.
type StorageConfig struct {
	Type        string
	Config      string // JSON blob from storage_destinations.config
	StageBeside bool   // Prefer local-destination-adjacent path over cache pools (issue #366)
}

func localDestPath(dest StorageConfig) string {
	if dest.Type != "local" {
		return ""
	}
	var cfg struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(dest.Config), &cfg); err == nil && cfg.Path != "" {
		return cfg.Path
	}
	return ""
}

type candidatePath struct {
	path  string
	free  uint64
	order int
}

func rankCandidatesByFreeSpace(paths []string) []string {
	var candidates []candidatePath
	for i, p := range paths {
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			continue
		}
		free, _ := diskSpace(p)
		candidates = append(candidates, candidatePath{
			path:  p,
			free:  free,
			order: i,
		})
	}
	// Sort by free descending, tie-breaking by original order ascending.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].free != candidates[j].free {
			return candidates[i].free > candidates[j].free
		}
		return candidates[i].order < candidates[j].order
	})
	result := make([]string, len(candidates))
	for i, c := range candidates {
		result[i] = c.path
	}
	return result
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
			if err := ensureStageDir(stageBase); err == nil {
				dir, err := makeStageTemp(stageBase, pattern)
				if err == nil {
					return dir, cleanupFunc(dir, stageBase), nil
				}
			} else {
				log.Printf("tempdir: %v", err)
			}
		}
		log.Printf("tempdir: override path %s unusable, falling back to cascade", override)
	}

	tryPath := func(base string) (string, func(), bool) {
		info, err := os.Stat(base)
		if err != nil || !info.IsDir() {
			return "", nil, false
		}
		stageBase := filepath.Join(base, StageDirName)
		if err := ensureStageDir(stageBase); err != nil {
			log.Printf("tempdir: %v", err)
			return "", nil, false
		}
		dir, err := makeStageTemp(stageBase, pattern)
		if err != nil {
			return "", nil, false
		}
		return dir, cleanupFunc(dir, stageBase), true
	}

	localPath := localDestPath(dest)

	// When StageBeside is active on a local destination, try destination-adjacent
	// staging before cache pools (issue #366).
	if dest.StageBeside && localPath != "" {
		if dir, cleanup, ok := tryPath(localPath); ok {
			return dir, cleanup, nil
		}
		log.Printf("tempdir: stage beside path %s unusable, falling back to cache cascade", localPath)
	}

	// Try fast cache/SSD paths, ranked by available free space (issue #366).
	for _, base := range rankCandidatesByFreeSpace(cachePaths()) {
		if dir, cleanup, ok := tryPath(base); ok {
			return dir, cleanup, nil
		}
	}

	// Fall back to local storage path adjacent to the backup destination (if not already tried).
	if !dest.StageBeside && localPath != "" {
		if dir, cleanup, ok := tryPath(localPath); ok {
			return dir, cleanup, nil
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

// ensureStageDir creates stageBase, or accepts it if it already exists as a real
// directory. The parent of a staging directory can be a user share or a backup
// destination, so a symlink at this name is treated as hostile.
func ensureStageDir(stageBase string) error {
	// A symlinked parent would otherwise carry staging into a system tree.
	if _, err := safepath.CheckFolderLocation(stageBase); err != nil {
		return fmt.Errorf("refusing to stage at %s: %w", stageBase, err)
	}
	if err := os.Mkdir(stageBase, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("creating %s: %w", stageBase, err)
	}
	info, err := os.Lstat(stageBase)
	if err != nil {
		return fmt.Errorf("inspecting %s: %w", stageBase, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("refusing to use %s: it is not a real directory", stageBase)
	}
	return nil
}

// makeStageTemp creates a private run directory inside stageBase. It resolves and
// re-checks stageBase, then creates through a handle pinned to that real directory,
// so swapping stageBase or a parent for a symlink after ensureStageDir cannot
// redirect the creation. The lexical path is returned, as callers and cleanup
// work relative to the location they configured.
func makeStageTemp(stageBase, pattern string) (string, error) {
	resolved, err := filepath.EvalSymlinks(stageBase)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", stageBase, err)
	}
	if _, err := safepath.CheckFolderLocation(resolved); err != nil {
		return "", fmt.Errorf("refusing to stage at %s: %w", stageBase, err)
	}
	name, err := mkdirPinnedTemp(resolved, pattern)
	if err != nil {
		return "", fmt.Errorf("creating staging directory in %s: %w", stageBase, err)
	}
	return filepath.Join(stageBase, name), nil
}

// mkdirPinnedTemp creates a mode-0700 directory named from pattern in the already
// resolved dir, failing if dir or any ancestor is a symlink. It returns the name.
func mkdirPinnedTemp(resolved, pattern string) (string, error) {
	root, err := safepath.OpenPinnedDir(resolved)
	if err != nil {
		return "", err
	}
	defer root.Close()
	for range 100 {
		name := strings.Replace(pattern, "*", rand.Text(), 1)
		err := root.Mkdir(name, 0o700)
		if err == nil {
			return name, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("could not find an unused name")
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
//
// The stage directory's parent can be writable by share users, so a symlink here
// is never followed: without this check a link to /etc would have every entry of
// /etc removed at the next daemon start. The handle is opened once and compared
// with the Lstat result, then used for all deletion.
func cleanStageDir(stageBase string) {
	info, err := os.Lstat(stageBase)
	if err != nil {
		return // directory doesn't exist or unreadable — nothing to clean
	}
	if !info.IsDir() {
		log.Printf("tempdir: not cleaning %s: it is not a real directory", stageBase)
		return
	}
	root, err := os.OpenRoot(stageBase)
	if err != nil {
		log.Printf("tempdir: not cleaning %s: %v", stageBase, err)
		return
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		log.Printf("tempdir: not cleaning %s: it changed while being opened", stageBase)
		return
	}
	dir, err := root.Open(".")
	if err != nil {
		return
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return
	}

	for _, entry := range entries {
		p := filepath.Join(stageBase, entry.Name())
		if err := root.RemoveAll(entry.Name()); err != nil {
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

	// Separate StageBeside destinations from normal local destinations.
	var besideDests []string
	var normalDests []string
	for _, dest := range destinations {
		if p := localDestPath(dest); p != "" {
			if dest.StageBeside {
				besideDests = append(besideDests, p)
			} else {
				normalDests = append(normalDests, p)
			}
		}
	}

	// Build cascade list for display:
	// StageBeside destinations first (highest priority after override),
	// followed by cache paths, then remaining local destinations, then system.
	for _, p := range besideDests {
		stagePath := filepath.Join(p, StageDirName)
		ci := CascadeItem{Path: stagePath, Source: "destination"}
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			ci.Available = true
		}
		info.Cascade = append(info.Cascade, ci)
	}
	for _, base := range cachePaths() {
		stagePath := filepath.Join(base, StageDirName)
		ci := CascadeItem{Path: stagePath, Source: "cache"}
		if fi, err := os.Stat(base); err == nil && fi.IsDir() {
			ci.Available = true
		}
		info.Cascade = append(info.Cascade, ci)
	}
	for _, p := range normalDests {
		stagePath := filepath.Join(p, StageDirName)
		ci := CascadeItem{Path: stagePath, Source: "local-storage"}
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			ci.Available = true
		}
		info.Cascade = append(info.Cascade, ci)
	}
	info.Cascade = append(info.Cascade, CascadeItem{Path: os.TempDir(), Available: true, Source: "system"})

	// Resolve which path wins:
	if override != "" {
		if fi, err := os.Stat(override); err == nil && fi.IsDir() {
			info.ResolvedPath = override
			info.Source = "override"
		}
	}

	if info.ResolvedPath == "" {
		if ranked := rankCandidatesByFreeSpace(besideDests); len(ranked) > 0 {
			info.ResolvedPath = filepath.Join(ranked[0], StageDirName)
			info.Source = "destination"
		}
	}

	if info.ResolvedPath == "" {
		if ranked := rankCandidatesByFreeSpace(cachePaths()); len(ranked) > 0 {
			info.ResolvedPath = filepath.Join(ranked[0], StageDirName)
			info.Source = "cache"
		}
	}

	if info.ResolvedPath == "" {
		if ranked := rankCandidatesByFreeSpace(normalDests); len(ranked) > 0 {
			info.ResolvedPath = filepath.Join(ranked[0], StageDirName)
			info.Source = "local-storage"
		}
	}

	if info.ResolvedPath == "" {
		info.ResolvedPath = os.TempDir()
		info.Source = "system"
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
	u, err := fsstat.Stat(p)
	if err != nil {
		return 0, 0
	}
	return u.Free, u.Total
}
