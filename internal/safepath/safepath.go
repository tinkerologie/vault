package safepath

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

var restoreAllowedRoots = []string{"/mnt", "/boot", "/tmp", "/etc", "/opt", "/usr/local", "/var", "/home", "/private/var", "/private/tmp"}

// RestoreAllowedRoots returns a defensive copy of the authorized top-level root directories for restore operations.
func RestoreAllowedRoots() []string {
	return append([]string(nil), restoreAllowedRoots...)
}

// NormalizeRelative cleans an untrusted relative path and rejects paths that
// escape their eventual base directory.
func NormalizeRelative(path string, allowRoot bool) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		if allowRoot {
			return ".", nil
		}
		return "", fmt.Errorf("path is required")
	}

	if !filepath.IsLocal(trimmed) {
		return "", fmt.Errorf("path must stay within the configured base directory")
	}

	rel := filepath.Clean(trimmed)
	if rel == "." {
		if allowRoot {
			return rel, nil
		}
		return "", fmt.Errorf("path must not point to the root")
	}
	return rel, nil
}

// JoinUnderBase joins an untrusted relative path to a trusted base directory.
func JoinUnderBase(basePath, path string, allowRoot bool) (string, error) {
	rel, err := NormalizeRelative(path, allowRoot)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return filepath.Clean(basePath), nil
	}
	return filepath.Join(filepath.Clean(basePath), rel), nil
}

// JoinRemote is JoinUnderBase for a POSIX path on a remote server (SFTP).
// It always joins with '/', so a client running on Windows still sends the
// server Unix paths (issue #313). As with JoinUnderBase, a backslash is a
// separator only on Windows, where Vault's own paths may contain one; on
// other platforms it stays a literal filename character. On Unix the result
// is identical to JoinUnderBase.
func JoinRemote(basePath, p string, allowRoot bool) (string, error) {
	base := path.Clean(filepath.ToSlash(basePath))
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		if allowRoot {
			return base, nil
		}
		return "", fmt.Errorf("path is required")
	}
	rel := path.Clean(filepath.ToSlash(trimmed))
	if path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("path must stay within the configured base directory")
	}
	if rel == "." {
		if allowRoot {
			return base, nil
		}
		return "", fmt.Errorf("path must not point to the root")
	}
	return path.Join(base, rel), nil
}

// NormalizeAbsoluteUnderRoots validates an absolute path against a fixed set of
// trusted root directories and returns the normalized path anchored to one of
// those roots.
func NormalizeAbsoluteUnderRoots(path string, roots []string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("path is required")
	}

	cleaned := filepath.Clean(trimmed)
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("path must be absolute")
	}

	for _, root := range roots {
		cleanRoot := filepath.Clean(root)
		rel, err := filepath.Rel(cleanRoot, cleaned)
		if err != nil {
			continue
		}
		if rel == "." {
			return cleanRoot, nil
		}
		if filepath.IsLocal(rel) {
			return filepath.Join(cleanRoot, rel), nil
		}
	}

	return "", fmt.Errorf("path must stay within approved roots")
}

// NormalizeComponent validates a single filename component.
func NormalizeComponent(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("name is required")
	}
	if strings.ContainsAny(trimmed, `/\`) {
		return "", fmt.Errorf("name must not contain path separators")
	}
	if trimmed != filepath.Base(trimmed) || !filepath.IsLocal(trimmed) {
		return "", fmt.Errorf("name must be a single local path component")
	}
	return trimmed, nil
}

// protectedStorageTrees are host directories that must never hold, or sit
// beneath, a local storage destination: the destination and everything
// below it would be exposed to Vault's recursive cleanup. Issue #514: a
// destination that resolved to /usr/local/emhttp let "Delete Storage &
// Files" erase the Unraid WebGUI.
var protectedStorageTrees = []string{
	"/bin",
	"/boot",
	"/dev",
	"/etc",
	"/lib",
	"/lib32",
	"/lib64",
	"/libx32",
	"/proc",
	"/root",
	"/run",
	"/sbin",
	"/sys",
	"/usr",
	"/var/cache",
	"/var/lib",
	"/var/local",
	"/var/log",
	"/var/run",
	"/var/spool",
}

// sharedStorageParents are directories that may contain a destination but
// must not be one: each holds unrelated user data or other mounts side by
// side, so it is rejected only as the destination itself.
var sharedStorageParents = []string{
	"/home",
	"/media",
	"/mnt",
	"/mnt/addons",
	"/mnt/disks",
	"/mnt/remotes",
	"/mnt/rootshare",
	"/mnt/user",
	"/mnt/user0",
	"/opt",
	"/private",
	"/private/tmp",
	"/private/var",
	"/srv",
	"/tmp",
}

// ValidateLocalStorageRoot checks that path is a safe root for a local
// storage destination and returns it cleaned. It rejects empty and relative
// paths (they resolve against the daemon's working directory), protected
// system trees and their ancestors (including "/"), and shared parents such
// as /mnt/user. The check runs on the path as written and again on its
// symlink-resolved form, so a symlink cannot smuggle a protected target in.
func ValidateLocalStorageRoot(p string) (string, error) {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return "", fmt.Errorf("storage path is required")
	}
	if !filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("storage path %q must be an absolute path", trimmed)
	}
	cleaned := filepath.Clean(trimmed)
	if err := checkStorageRoot(p, cleaned); err != nil {
		return "", err
	}
	resolved, err := resolveExistingPrefix(cleaned)
	if err != nil {
		return "", fmt.Errorf("storage path %q cannot be resolved: %w", cleaned, err)
	}
	if resolved != cleaned {
		if err := checkStorageRoot(p, resolved); err != nil {
			return "", err
		}
	}
	return cleaned, nil
}

// checkStorageRoot applies the protected-tree and shared-parent rules to one
// clean absolute path. original is the caller's input, quoted in errors.
func checkStorageRoot(original, p string) error {
	for _, tree := range protectedStorageTrees {
		if p == tree || isStrictDescendantPath(tree, p) || isStrictDescendantPath(p, tree) {
			return fmt.Errorf("storage path %q is a protected system location; choose a dedicated backup directory such as /mnt/user/backups/vault", strings.TrimSpace(original))
		}
	}
	for _, parent := range sharedStorageParents {
		if p == parent {
			return fmt.Errorf("storage path %q is a shared parent directory; choose a dedicated subdirectory such as %s", strings.TrimSpace(original), filepath.Join(parent, "vault-backups"))
		}
	}
	return nil
}

// isStrictDescendantPath reports whether child lies strictly below parent.
// Both must be clean absolute paths.
func isStrictDescendantPath(parent, child string) bool {
	if parent == string(filepath.Separator) {
		return child != parent && strings.HasPrefix(child, parent)
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}

// resolveExistingPrefix resolves symlinks in the deepest existing ancestor of
// p and re-appends the part that does not exist yet. Any error other than
// "does not exist" fails closed.
func resolveExistingPrefix(p string) (string, error) {
	cur := p
	var rest []string
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, rest[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// NormalizeRestorePointPath validates a restore point's storage path: a
// relative, slash-separated path of at least two components
// (<job name>/<run folder>). It rejects the destination root, absolute
// paths, traversal, and single top-level directories, so a recorded or
// imported row can never authorise a recursive delete of the whole
// destination or of a directory Vault did not create.
func NormalizeRestorePointPath(p string) (string, error) {
	trimmed := strings.TrimSpace(filepath.ToSlash(p))
	if trimmed == "" {
		return "", fmt.Errorf("restore point path is required")
	}
	if path.IsAbs(trimmed) {
		return "", fmt.Errorf("restore point path %q must be relative", trimmed)
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("restore point path %q must stay within the destination", trimmed)
	}
	if !strings.Contains(cleaned, "/") {
		return "", fmt.Errorf("restore point path %q must be <job>/<run>", trimmed)
	}
	return cleaned, nil
}

// IsStrictDescendant reports whether child is a slash-separated path strictly
// below parent. Both are cleaned first; absolute or escaping children never
// qualify. Used to confine a peer-supplied listing to the requested prefix.
func IsStrictDescendant(parent, child string) bool {
	parent = path.Clean(filepath.ToSlash(strings.TrimSpace(parent)))
	child = path.Clean(filepath.ToSlash(strings.TrimSpace(child)))
	if path.IsAbs(child) || child == "." || child == ".." || strings.HasPrefix(child, "../") {
		return false
	}
	if parent == "." {
		return true
	}
	return strings.HasPrefix(child, parent+"/")
}
