package safepath

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Destination policies. Vault runs as root and restore metadata (mount sources,
// domain XML, folder_meta.json) comes from backup storage, so each kind of
// target gets its own narrow allowlist instead of sharing one broad list. Every
// check runs on the path as written and on its symlink-resolved form.
//
// The OS temp directory is always allowed: /tmp holds nothing that runs
// automatically on Unraid, and it is where a trial restore usually goes.
const (
	dataRoot         = "/mnt"
	pluginConfigRoot = "/boot/config/plugins"
	flashRoot        = "/boot"
	libvirtImageRoot = "/var/lib/libvirt/images"
	dockerVolumeRoot = "/var/lib/docker/volumes"
)

var nvramRoots = []string{"/etc/libvirt/qemu/nvram", "/var/lib/libvirt/qemu/nvram"}

// tempRoots returns the temp directories, as written and resolved.
func tempRoots() []string {
	roots := []string{"/tmp", "/private/tmp", filepath.Clean(os.TempDir())}
	if resolved, err := resolveExistingPrefix(filepath.Clean(os.TempDir())); err == nil {
		roots = append(roots, resolved)
	}
	return roots
}

// RejectControlChars refuses a path holding any ASCII control character. A
// newline in a persisted path can forge further vault.cfg records.
func RejectControlChars(p string) error {
	if strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return errors.New("path must not contain control characters")
	}
	return nil
}

// CheckCustomRestoreDestination authorizes a destination the operator typed for
// a restore. It allows the data root, temp directories, and a plugin's own
// config directory; system trees and the rest of the flash drive are refused.
func CheckCustomRestoreDestination(p string) (string, error) {
	return checkPolicy(p, "restore destination", "choose a location under /mnt", func(c string) bool {
		return strictlyUnderAny(c, dataRoot, pluginConfigRoot) || underTemp(c)
	})
}

// CheckVolumeTarget authorizes a container mount source read from a backup as
// an in-place restore target: the data root, a Docker named volume, or a
// plugin's config directory.
func CheckVolumeTarget(p string) (string, error) {
	return checkPolicy(p, "container volume target", "choose a restore destination under /mnt", func(c string) bool {
		return strictlyUnderAny(c, dataRoot, pluginConfigRoot) || underTemp(c) || isDockerVolumeData(c)
	})
}

// CheckVMDiskTarget authorizes a disk path read from backed-up domain XML.
func CheckVMDiskTarget(p string) (string, error) {
	return checkPolicy(p, "VM disk target", "choose a restore destination under /mnt", func(c string) bool {
		return strictlyUnderAny(c, dataRoot, libvirtImageRoot) || underTemp(c)
	})
}

// CheckVMNVRAMTarget authorizes an NVRAM path read from backed-up domain XML:
// directly inside libvirt's NVRAM directory, or in a directory a disk may use.
func CheckVMNVRAMTarget(p string) (string, error) {
	return checkPolicy(p, "VM NVRAM target", "choose a restore destination under /mnt", func(c string) bool {
		return slices.Contains(nvramRoots, filepath.Dir(c)) || strictlyUnderAny(c, dataRoot) || underTemp(c)
	})
}

// CheckFolderLocation authorizes a folder item's configured path, as a backup
// source or as its original restore location. /boot is allowed for flash
// backups; every other protected system tree, and any ancestor of one, is not.
func CheckFolderLocation(p string) (string, error) {
	return checkPolicy(p, "folder path", "choose a location under /mnt or /boot", func(c string) bool {
		for _, tree := range protectedStorageTrees {
			if tree == flashRoot {
				continue
			}
			if c == tree || isStrictDescendantPath(tree, c) || isStrictDescendantPath(c, tree) {
				return false
			}
		}
		return true
	})
}

// CheckSnapshotLocation authorizes a database snapshot file path: under the
// data root, the flash drive or a temp directory, reached without symlinks
// leaving those roots. The file itself must not be a symlink or a non-regular
// file, because the snapshot is replaced and read back as root.
func CheckSnapshotLocation(p string) (string, error) {
	cleaned, err := checkPolicy(p, "snapshot path", "choose a location under /mnt or /boot", func(c string) bool {
		return strictlyUnderAny(c, dataRoot, flashRoot) || underTemp(c)
	})
	if err != nil {
		return "", err
	}
	// Snapshot writes clean up "<path>.tmp" through a guard that refuses any
	// path holding "..". A name like "backups..2026.db" would leave that file
	// in place for a share writer to swap, so such names are refused up front.
	if strings.Contains(cleaned, "..") {
		return "", fmt.Errorf("snapshot path %q must not contain \"..\"", cleaned)
	}
	// Inspected through a handle pinned to the resolved parent rather than by
	// pathname, so the answer is about the file in the checked directory.
	info, err := lstatPinned(cleaned)
	if errors.Is(err, fs.ErrNotExist) {
		return cleaned, nil
	}
	if err != nil {
		return "", fmt.Errorf("snapshot path %q: %w", cleaned, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("snapshot path %q must be a regular file, not a symlink or directory", cleaned)
	}
	return cleaned, nil
}

// ResolvesUnderRoots reports whether p's symlink-resolved form is one of roots
// or lies beneath one. Failure to resolve counts as outside.
func ResolvesUnderRoots(p string, roots []string) bool {
	resolved, err := resolveExistingPrefix(filepath.Clean(p))
	if err != nil {
		return false
	}
	resolved = canonicalAlias(resolved)
	for _, root := range append(slices.Clone(roots), tempRoots()...) {
		root = canonicalAlias(filepath.Clean(root))
		if resolved == root || isStrictDescendantPath(root, resolved) {
			return true
		}
	}
	return false
}

// lstatPinned returns Lstat of p's final component, read through a pinned handle
// to p's resolved parent directory.
func lstatPinned(p string) (os.FileInfo, error) {
	parent, err := resolveExistingPrefix(filepath.Dir(p))
	if err != nil {
		return nil, err
	}
	root, err := OpenPinnedDir(parent)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Lstat(filepath.Base(p))
}

// checkPolicy applies allowed to p as written and to its resolved form, and
// returns p cleaned.
func checkPolicy(p, what, hint string, allowed func(string) bool) (string, error) {
	if err := RejectControlChars(p); err != nil {
		return "", fmt.Errorf("%s: %w", what, err)
	}
	trimmed := strings.TrimSpace(p)
	if !filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("%s %q must be an absolute path", what, p)
	}
	if slices.Contains(strings.Split(filepath.ToSlash(trimmed), "/"), "..") {
		return "", fmt.Errorf("%s %q must not contain traversal components", what, p)
	}
	cleaned := filepath.Clean(trimmed)
	resolved, err := resolveExistingPrefix(cleaned)
	if err != nil {
		return "", fmt.Errorf("%s %q cannot be resolved: %w", what, cleaned, err)
	}
	if !allowed(canonicalAlias(cleaned)) || !allowed(canonicalAlias(resolved)) {
		return "", fmt.Errorf("%s %q is outside the approved locations; %s", what, cleaned, hint)
	}
	return cleaned, nil
}

func strictlyUnderAny(p string, roots ...string) bool {
	return slices.ContainsFunc(roots, func(root string) bool { return isStrictDescendantPath(root, p) })
}

func underTemp(p string) bool {
	return slices.ContainsFunc(tempRoots(), func(root string) bool {
		return isStrictDescendantPath(canonicalAlias(root), p)
	})
}

// isDockerVolumeData reports whether p is a named volume's data directory
// (/var/lib/docker/volumes/<name>/_data) or lies inside one.
func isDockerVolumeData(p string) bool {
	rest, ok := strings.CutPrefix(p, dockerVolumeRoot+"/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	return len(parts) >= 2 && parts[1] == "_data"
}

// canonicalAlias maps macOS's /private spelling of /var, /etc and /tmp onto the
// short form, so protected-tree checks see one representation of each path.
func canonicalAlias(p string) string {
	for _, alias := range []string{"/var", "/etc", "/tmp"} {
		if rest, ok := strings.CutPrefix(p, "/private"+alias); ok && (rest == "" || rest[0] == '/') {
			return alias + rest
		}
	}
	return p
}

// ErrSymlinkInPath reports that OpenPinnedDir met a symlink.
var ErrSymlinkInPath = errors.New("path contains a symlink")

// OpenPinnedDir opens the directory at the absolute, already-resolved path p and
// returns an os.Root pinned to it. It walks one component at a time, each opened
// beneath the previous one, and fails on any symlink, so swapping an ancestor
// after validation cannot redirect the handle. Callers resolve p first: a
// symlink met here means the tree changed underneath them. A component replaced
// between its Lstat and open is caught by comparing identities, and os.Root
// confines even that attempt to the parent directory.
func OpenPinnedDir(p string) (*os.Root, error) {
	cleaned := filepath.Clean(p)
	if !filepath.IsAbs(cleaned) {
		return nil, fmt.Errorf("pinned directory %q must be absolute", p)
	}
	volume := filepath.VolumeName(cleaned)
	cur, err := os.OpenRoot(volume + string(filepath.Separator))
	if err != nil {
		return nil, fmt.Errorf("opening filesystem root: %w", err)
	}
	rest := strings.TrimPrefix(cleaned[len(volume):], string(filepath.Separator))
	for comp := range strings.SplitSeq(rest, string(filepath.Separator)) {
		if comp == "" {
			continue
		}
		next, err := pinChild(cur, comp)
		_ = cur.Close()
		if err != nil {
			return nil, fmt.Errorf("opening %s at %q: %w", cleaned, comp, err)
		}
		cur = next
	}
	return cur, nil
}

func pinChild(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSymlinkInPath
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := child.Stat(".")
	if err != nil {
		_ = child.Close()
		return nil, err
	}
	if !os.SameFile(before, opened) {
		_ = child.Close()
		return nil, ErrSymlinkInPath
	}
	return child, nil
}
