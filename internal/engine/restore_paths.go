package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ruaan-deysel/vault/internal/safepath"
)

var restoreAllowedRoots = []string{"/mnt", "/boot", "/tmp", "/etc", "/opt", "/usr/local", "/var", "/home"}

var _ = normalizeVMRestorePlan

func normalizeRestorePath(path string) (string, error) {
	normalizedPath, err := safepath.NormalizeAbsoluteUnderRoots(path, restoreAllowedRoots)
	if err != nil {
		return "", fmt.Errorf("invalid restore path %q: %w", path, err)
	}

	resolvedPath, err := resolveRestorePath(normalizedPath)
	if err != nil {
		return "", fmt.Errorf("invalid restore path %q: %w", path, err)
	}

	allowed, err := restorePathWithinAllowedRoots(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("invalid restore path %q: %w", path, err)
	}
	if !allowed {
		return "", fmt.Errorf("invalid restore path %q: path must stay within approved roots", path)
	}

	return resolvedPath, nil
}

func normalizeRestoreComponent(name string) (string, error) {
	normalizedName, err := safepath.NormalizeComponent(name)
	if err != nil {
		return "", fmt.Errorf("invalid restore name %q: %w", name, err)
	}
	return normalizedName, nil
}

func joinArchiveTarget(destDir, entryName string) (string, error) {
	targetPath, err := safepath.JoinUnderBase(destDir, filepath.FromSlash(entryName), false)
	if err != nil {
		return "", fmt.Errorf("invalid archive entry %q: %w", entryName, err)
	}
	return targetPath, nil
}

// resolveWithinBase resolves symlinks in the parent directory of target and
// verifies the resulting path stays within destDir. This prevents
// previously-extracted symlinks from redirecting later entries outside the
// extraction root (CWE-22 / go/unsafe-unzip-symlink).
func resolveWithinBase(destDir, target string) error {
	// Resolve the base directory itself to handle platform symlinks (e.g.
	// /var -> /private/var on macOS).
	resolvedDest, err := evalExistingPrefix(destDir)
	if err != nil {
		return fmt.Errorf("resolving dest dir: %w", err)
	}
	cleanDest := filepath.Clean(resolvedDest)

	// Resolve symlinks in the parent directory (which should already exist
	// or be about to be created). Walk up to find the deepest existing
	// ancestor so EvalSymlinks succeeds.
	parent := filepath.Dir(target)
	resolvedParent, err := evalExistingPrefix(parent)
	if err != nil {
		return fmt.Errorf("resolving parent of %s: %w", target, err)
	}

	// Reattach any trailing components that didn't exist yet.
	suffix, err := filepath.Rel(parent, target)
	if err != nil {
		return fmt.Errorf("computing relative suffix: %w", err)
	}
	resolved := filepath.Join(resolvedParent, suffix)

	if !strings.HasPrefix(resolved, cleanDest+string(filepath.Separator)) && resolved != cleanDest {
		return fmt.Errorf("path %s resolves to %s which is outside %s", target, resolved, cleanDest)
	}
	return nil
}

// resolveSymlinkTarget validates that a symlink's effective destination stays
// within destDir after resolving any previously-extracted symlinks.
func resolveSymlinkTarget(destDir, symlinkPath, linkTarget string) error {
	resolvedDest, err := evalExistingPrefix(destDir)
	if err != nil {
		return fmt.Errorf("resolving dest dir: %w", err)
	}
	cleanDest := filepath.Clean(resolvedDest)

	if filepath.IsAbs(linkTarget) {
		return fmt.Errorf("symlink %s has absolute target %q: rejecting", symlinkPath, linkTarget)
	}

	// Resolve the symlink's parent directory (which must exist on disk)
	// and then walk linkTarget component-by-component so that ".." is
	// applied after resolving intermediate symlinks, not before.
	resolvedParent, err := evalExistingPrefix(filepath.Dir(symlinkPath))
	if err != nil {
		return fmt.Errorf("resolving symlink parent of %s: %w", symlinkPath, err)
	}

	resolved, err := walkPathComponents(resolvedParent, linkTarget)
	if err != nil {
		return fmt.Errorf("resolving symlink target of %s: %w", symlinkPath, err)
	}

	if !strings.HasPrefix(resolved, cleanDest+string(filepath.Separator)) && resolved != cleanDest {
		return fmt.Errorf("symlink %s target %q resolves to %s which escapes %s", symlinkPath, linkTarget, resolved, cleanDest)
	}
	return nil
}

// walkPathComponents resolves a relative path against a base directory
// component by component. Unlike filepath.Join (which calls Clean and
// collapses ".." syntactically), this function resolves symlinks at each
// step before handling ".." so that the real filesystem structure is followed.
func walkPathComponents(base, relPath string) (string, error) {
	current := base
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			// Resolve symlinks in current before going to parent, so
			// that symlink → ".." chains are handled correctly.
			resolved, err := evalExistingPrefix(current)
			if err != nil {
				return "", err
			}
			current = filepath.Dir(resolved)
		default:
			current = filepath.Join(current, part)
			// If the component exists and is a symlink, resolve it.
			resolved, err := evalExistingPrefix(current)
			if err != nil {
				return "", err
			}
			current = resolved
		}
	}
	return filepath.Clean(current), nil
}

// evalExistingPrefix resolves symlinks for the longest existing prefix of a
// path and appends any remaining non-existent components.
func evalExistingPrefix(path string) (string, error) {
	clean := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(clean)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	parent := filepath.Dir(clean)
	if parent == clean {
		return "", err
	}

	resolvedParent, err := evalExistingPrefix(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(clean)), nil
}

func normalizeVMRestorePlan(plan *vmRestorePlan) error {
	for i := range plan.Disks {
		backupFile, err := normalizeRestoreComponent(plan.Disks[i].BackupFile)
		if err != nil {
			return err
		}
		plan.Disks[i].BackupFile = backupFile

		targetPath, err := normalizeRestorePath(plan.Disks[i].TargetPath)
		if err != nil {
			return err
		}
		plan.Disks[i].TargetPath = targetPath
	}

	if plan.NVRAMBackupFile != "" {
		backupFile, err := normalizeRestoreComponent(plan.NVRAMBackupFile)
		if err != nil {
			return err
		}
		plan.NVRAMBackupFile = backupFile
	}
	if plan.NVRAMTargetPath != "" {
		targetPath, err := normalizeRestorePath(plan.NVRAMTargetPath)
		if err != nil {
			return err
		}
		plan.NVRAMTargetPath = targetPath
	}

	return nil
}

func resolveRestorePath(path string) (string, error) {
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolving absolute path: %w", err)
	}

	resolvedPath, err := evalRestoreSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("resolving symlinks: %w", err)
	}

	return filepath.Clean(resolvedPath), nil
}

func evalRestoreSymlinks(path string) (string, error) {
	cleanPath := filepath.Clean(path)
	resolvedPath, err := filepath.EvalSymlinks(cleanPath)
	if err == nil {
		return resolvedPath, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	parent := filepath.Dir(cleanPath)
	if parent == cleanPath {
		return "", err
	}

	resolvedParent, err := evalRestoreSymlinks(parent)
	if err != nil {
		return "", err
	}

	return filepath.Join(resolvedParent, filepath.Base(cleanPath)), nil
}

func restorePathWithinAllowedRoots(path string) (bool, error) {
	cleanPath := filepath.Clean(path)
	for _, root := range restoreAllowedRoots {
		resolvedRoot, err := resolveRestorePath(filepath.Clean(root))
		if err != nil {
			return false, fmt.Errorf("resolving restore root %q: %w", root, err)
		}

		if cleanPath == resolvedRoot || strings.HasPrefix(cleanPath, resolvedRoot+string(filepath.Separator)) {
			return true, nil
		}
	}

	return false, nil
}
