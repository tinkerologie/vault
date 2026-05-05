package cli

import (
	"os"

	"github.com/ruaan-deysel/vault/internal/unraid"
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
// It distinguishes between the directory not existing, existing but
// unmounted, and mounted. Mount status is verified against
// /proc/self/mountinfo so empty-but-mounted pools are correctly detected.
func checkCacheMount(path string) cacheStatus {
	return checkCacheMountWith(path, unraid.IsMountedPool)
}

// checkCacheMountWith is the testable variant that accepts an injectable
// mount-check function.
func checkCacheMountWith(path string, isMounted func(string) bool) cacheStatus {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return cacheNotExist
	}
	if isMounted(path) {
		return cacheMounted
	}
	return cacheEmptyNotMounted
}
