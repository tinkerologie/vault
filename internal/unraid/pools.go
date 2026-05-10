package unraid

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// mntBase is the root directory for Unraid mount points.
// It is a variable so tests can override it.
var mntBase = "/mnt"

// mountInfoPath is the procfs file used to determine active mount points.
// It is a variable so tests can override it.
var mountInfoPath = "/proc/self/mountinfo"

// excludedNames are known non-pool directories under /mnt/.
var excludedNames = map[string]bool{
	"user":    true,
	"user0":   true,
	"disks":   true,
	"remotes": true,
}

// arrayDiskPattern matches array disk entries like disk1, disk2, ..., disk99.
var arrayDiskPattern = regexp.MustCompile(`^disk\d+$`)

// DiscoverPools enumerates Unraid pool mount points under /mnt/ using
// exclusion-based detection. It returns a sorted slice of absolute paths
// with "/mnt/cache" sorted first (if present) for backwards compatibility.
// Returns an empty slice if /mnt/ does not exist or cannot be read.
func DiscoverPools() []string {
	return discoverPoolsIn(mntBase)
}

// discoverPoolsIn is the testable implementation that accepts a custom root.
func discoverPoolsIn(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	// Build a set of mount points whose filesystem is unsuitable for staging
	// (tmpfs, devtmpfs, etc.). On Unraid, /mnt/addons and /mnt/rootshare are
	// 1MB tmpfs mounts that must never be used for backup staging.
	unsuitable := unsuitableMountPoints(mountInfoPath)

	var pools []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()

		// Skip known non-pool directories.
		if excludedNames[name] {
			continue
		}

		// Skip array disks (disk1, disk2, ...).
		if arrayDiskPattern.MatchString(name) {
			continue
		}

		full := filepath.Join(root, name)
		if unsuitable[full] {
			continue
		}

		pools = append(pools, full)
	}

	// Sort with "cache" first for backwards compatibility, then alphabetical.
	sort.Slice(pools, func(i, j int) bool {
		iName := filepath.Base(pools[i])
		jName := filepath.Base(pools[j])
		if iName == "cache" {
			return true
		}
		if jName == "cache" {
			return false
		}
		return iName < jName
	})

	return pools
}

// PreferredPool returns the best pool path for database/staging use.
// It prefers the first pool that is actually mounted (verified via
// /proc/self/mountinfo), with /mnt/cache taking priority among mounted
// pools. If no discovered pool is reported as mounted (e.g. on non-Linux
// hosts or in tests where /proc/self/mountinfo doesn't list /mnt/*),
// it falls back to the first discovered pool so callers can still surface
// a sensible default.
// Returns an empty string if no pools are detected.
func PreferredPool() string {
	return preferredMountedPoolIn(mntBase, IsMountedPool)
}

// preferredMountedPoolIn is the testable implementation that accepts a
// custom root and a mount-check function.
func preferredMountedPoolIn(root string, isMounted func(string) bool) string {
	pools := discoverPoolsIn(root)
	if len(pools) == 0 {
		return ""
	}

	// Prefer the first pool that is actually mounted. discoverPoolsIn
	// already sorts /mnt/cache first, so a mounted cache pool wins.
	for _, p := range pools {
		if isMounted(p) {
			return p
		}
	}

	// No verified mount — fall back to the first discovered pool. The
	// caller (e.g. daemon startup) is expected to do its own mount check
	// before relying on the path.
	return pools[0]
}

// IsMountedPool reports whether poolPath is an active mount point by
// consulting /proc/self/mountinfo. This is more reliable than checking
// directory contents, which can misclassify empty-but-mounted pools.
func IsMountedPool(poolPath string) bool {
	return isMountedPoolFrom(mountInfoPath, poolPath)
}

// isMountedPoolFrom is the testable implementation that accepts a custom
// mountinfo path.
func isMountedPoolFrom(infoPath, poolPath string) bool {
	absPool, err := filepath.Abs(poolPath)
	if err != nil {
		return false
	}

	f, err := os.Open(infoPath) // #nosec G304 — infoPath is "/proc/self/mountinfo" (compile-time constant)
	if err != nil {
		return false
	}
	defer f.Close()

	// Each line in mountinfo has ≥10 fields. Field 5 (index 4) is the mount point.
	// See https://www.kernel.org/doc/Documentation/filesystems/proc.txt
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			continue
		}
		mountPoint := fields[4]
		// Unescape octal sequences (e.g. \040 for space) used in mountinfo.
		mountPoint = unescapeMountInfo(mountPoint)
		if mountPoint == absPool {
			return true
		}
	}
	return false
}

// unescapeMountInfo decodes octal escape sequences (\NNN) found in
// /proc/self/mountinfo mount-point fields.
func unescapeMountInfo(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) &&
			s[i+1] >= '0' && s[i+1] <= '3' &&
			s[i+2] >= '0' && s[i+2] <= '7' &&
			s[i+3] >= '0' && s[i+3] <= '7' {
			val := (s[i+1]-'0')*64 + (s[i+2]-'0')*8 + (s[i+3] - '0')
			b.WriteByte(val)
			i += 3
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// unsuitableFSTypes lists filesystem types that must never be used for
// backup staging (RAM-backed or virtual filesystems).
var unsuitableFSTypes = map[string]bool{
	"tmpfs":    true,
	"devtmpfs": true,
	"ramfs":    true,
	"proc":     true,
	"sysfs":    true,
	"cgroup":   true,
	"cgroup2":  true,
	"overlay":  true,
}

// unsuitableMountPoints returns a set of mount points (under any root) whose
// filesystem type is in unsuitableFSTypes. Returns an empty map if mountinfo
// cannot be read (callers must treat as "no exclusions").
func unsuitableMountPoints(infoPath string) map[string]bool {
	out := make(map[string]bool)
	f, err := os.Open(infoPath) // #nosec G304 — caller passes a known mountinfo path
	if err != nil {
		return out
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		// mountinfo format: ID PARENT MAJ:MIN ROOT MOUNTPOINT OPTS - FSTYPE SOURCE SUPEROPTS
		// The "-" separator splits the optional fields from the FS info.
		line := scanner.Text()
		sepIdx := strings.Index(line, " - ")
		if sepIdx < 0 {
			continue
		}
		left := strings.Fields(line[:sepIdx])
		right := strings.Fields(line[sepIdx+3:])
		if len(left) < 5 || len(right) < 1 {
			continue
		}
		mountPoint := unescapeMountInfo(left[4])
		fsType := right[0]
		if unsuitableFSTypes[fsType] {
			out[mountPoint] = true
		}
	}
	return out
}
