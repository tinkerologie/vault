package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var (
	_ = parseChangedSince
	_ = pathChangedSince
	_ = filterChangedDomainDisks
)

var errPathChanged = errors.New("path changed since reference")

func parseChangedSince(settings map[string]any) (time.Time, bool) {
	if settings == nil {
		return time.Time{}, false
	}

	value, ok := settings["changed_since"].(string)
	if !ok || value == "" {
		return time.Time{}, false
	}

	changedSince, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}

	return changedSince, true
}

func pathChangedSince(path string, changedSince time.Time) (bool, error) {
	if changedSince.IsZero() {
		return true, nil
	}

	info, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}

	if !info.IsDir() {
		return info.ModTime().After(changedSince), nil
	}

	if info.ModTime().After(changedSince) {
		return true, nil
	}

	err = filepath.Walk(path, func(current string, walkInfo os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if walkInfo.ModTime().After(changedSince) {
			return errPathChanged
		}
		return nil
	})
	if errors.Is(err, errPathChanged) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("walk %s: %w", path, err)
	}

	return false, nil
}

func filterChangedDomainDisks(disks []domainDisk, changedSince time.Time) ([]domainDisk, error) {
	if changedSince.IsZero() {
		copied := make([]domainDisk, len(disks))
		copy(copied, disks)
		return copied, nil
	}

	changed := make([]domainDisk, 0, len(disks))
	for _, disk := range disks {
		diskChanged, err := pathChangedSince(disk.Path, changedSince)
		if err != nil {
			return nil, fmt.Errorf("checking disk %s changes: %w", disk.Path, err)
		}
		if diskChanged {
			changed = append(changed, disk)
		}
	}

	return changed, nil
}
