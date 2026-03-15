package engine

import (
	"fmt"
	"path/filepath"

	"github.com/ruaandeysel/vault/internal/safepath"
)

var restoreAllowedRoots = []string{"/mnt", "/boot", "/tmp", "/etc", "/opt", "/usr/local", "/var", "/home"}

var _ = normalizeVMRestorePlan

func normalizeRestorePath(path string) (string, error) {
	normalizedPath, err := safepath.NormalizeAbsoluteUnderRoots(path, restoreAllowedRoots)
	if err != nil {
		return "", fmt.Errorf("invalid restore path %q: %w", path, err)
	}
	return normalizedPath, nil
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
