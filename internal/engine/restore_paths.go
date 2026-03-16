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
