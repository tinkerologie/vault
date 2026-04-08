package cli

import (
	"os"
)

// cacheStatus describes the state of the /mnt/cache directory.
type cacheStatus int

const (
	// cacheNotExist means the /mnt/cache directory does not exist.
	cacheNotExist cacheStatus = iota
	// cacheEmptyNotMounted means /mnt/cache exists but appears unmounted (empty dir).
	cacheEmptyNotMounted
	// cacheMounted means /mnt/cache exists and contains entries (mounted).
	cacheMounted
)

// checkCacheMount determines the mount status of path (typically /mnt/cache).
// It distinguishes between the directory not existing, existing but empty
// (unmounted), and populated (mounted).
func checkCacheMount(path string) cacheStatus {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return cacheNotExist
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return cacheNotExist
	}

	// Filter out hidden dotfiles — an empty cache dir sometimes has
	// .placeholder or similar. A truly mounted btrfs/xfs will have
	// real directories like .vault, appdata, etc.
	for _, e := range entries {
		name := e.Name()
		if name != "." && name != ".." {
			return cacheMounted
		}
	}
	return cacheEmptyNotMounted
}
