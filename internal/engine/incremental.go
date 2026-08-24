package engine

import (
	"context"
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

func pathChangedSince(ctx context.Context, path string, changedSince time.Time) (bool, error) {
	return pathChangedSinceWithPrev(ctx, path, changedSince, nil)
}

// pathChangedSinceWithPrev is pathChangedSince plus a prevPaths set of paths
// recorded in the parent restore point's effective listing for this tree. A
// regular file whose mtime predates changedSince but is ABSENT from prevPaths
// is a NEW file copied in with a stale timestamp, so the path is reported
// changed instead of skipped (issue #320). A nil prevPaths preserves the
// mtime-only behaviour of pathChangedSince. Non-directory roots and symlinks
// keep their existing mtime-only handling; prevPaths only affects the
// directory walk.
func pathChangedSinceWithPrev(ctx context.Context, path string, changedSince time.Time, prevPaths map[string]struct{}) (bool, error) {
	if changedSince.IsZero() {
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
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
		// Honour cancellation between files so a large unchanged tree does
		// not block backup cancellation (issue #251).
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkInfo.ModTime().After(changedSince) {
			return errPathChanged
		}
		// A file that predates changedSince is normally unchanged — unless it
		// is absent from the parent's listing, in which case it is NEW with a
		// stale timestamp (issue #320). Directories keep their mtime as the
		// signal, matching pathChangedSince.
		if !walkInfo.IsDir() && prevPaths != nil {
			if rel, relErr := filepath.Rel(path, current); relErr == nil && rel != "." {
				if _, exists := prevPaths[rel]; !exists {
					return errPathChanged
				}
			}
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

func filterChangedDomainDisks(ctx context.Context, disks []domainDisk, changedSince time.Time) ([]domainDisk, error) {
	if changedSince.IsZero() {
		copied := make([]domainDisk, len(disks))
		copy(copied, disks)
		return copied, nil
	}

	changed := make([]domainDisk, 0, len(disks))
	for _, disk := range disks {
		diskChanged, err := pathChangedSince(ctx, disk.Path, changedSince)
		if err != nil {
			return nil, fmt.Errorf("checking disk %s changes: %w", disk.Path, err)
		}
		if diskChanged {
			changed = append(changed, disk)
		}
	}

	return changed, nil
}
