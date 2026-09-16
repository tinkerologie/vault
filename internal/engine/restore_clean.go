package engine

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// SettingCleanDestination is the BackupItem.Settings key that asks a restore to
// replace the target's contents rather than merge on top of them (issue #321).
// The runner sets it; handlers that extract a directory tree honour it.
const SettingCleanDestination = "clean_destination"

// errUnsafeToClean is returned when a target is too shallow to clear. It is a
// sentinel rather than a hard failure so callers can fall back to the merge
// restore instead of refusing to restore at all — a flash restore targets
// /boot, and losing the restore entirely would be worse than merging into it.
var errUnsafeToClean = errors.New("refusing to clear this path")

// minCleanDepth is how many path components a target must have before its
// contents may be wiped. Three keeps /mnt/user/appdata and /mnt/disk1/media
// clearable while refusing /mnt, /mnt/user, /boot, and every other path whose
// contents are not the restore's to own.
const minCleanDepth = 3

// pathDepth counts the components of an absolute path: /mnt/user/appdata is 3.
func pathDepth(path string) int {
	trimmed := strings.Trim(filepath.Clean(path), string(filepath.Separator))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, string(filepath.Separator)))
}

// clearRestoreTarget removes everything inside target, keeping target itself.
//
// The restore wizard warns "This will overwrite existing data", but the
// extraction only ever wrote the archive's own entries over whatever was
// already there, so unrelated leftovers survived and the restored tree was a
// mix of two points in time (issue #321).
//
// Children are removed with RemoveAll without being walked first, so a symlink
// child is unlinked and its target is never touched. The removal goes through
// os.Root, whose kernel-enforced boundary means no entry name — however
// crafted, however raced — can reach outside the target (CWE-22).
//
// A target that does not exist yet is not an error: there is nothing to clear.
func clearRestoreTarget(target string) error {
	// resolveRestorePath + restorePathWithinAllowedRoots rather than
	// normalizeRestorePath: every caller has already normalised its target,
	// and normalizeRestorePath is not idempotent — it compares the literal
	// path against the literal roots before resolving symlinks, so feeding it
	// its own output rejects any root that is itself a symlink.
	normalized, err := resolveRestorePath(filepath.Clean(target))
	if err != nil {
		return fmt.Errorf("resolving restore target %q: %w", target, err)
	}
	allowed, err := restorePathWithinAllowedRoots(normalized)
	if err != nil {
		return fmt.Errorf("checking restore target %q: %w", target, err)
	}
	if !allowed {
		return fmt.Errorf("refusing to clear %q: path must stay within approved roots", target)
	}
	if depth := pathDepth(normalized); depth < minCleanDepth {
		return fmt.Errorf("%w: %s is only %d level(s) below the filesystem root", errUnsafeToClean, normalized, depth)
	}

	root, err := os.OpenRoot(normalized)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("opening restore target %s: %w", normalized, err)
	}
	defer root.Close()

	dir, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("opening restore target %s: %w", normalized, err)
	}
	names, err := dir.Readdirnames(-1)
	closeErr := dir.Close()
	if err != nil {
		return fmt.Errorf("reading restore target %s: %w", normalized, err)
	}
	if closeErr != nil {
		return fmt.Errorf("reading restore target %s: %w", normalized, closeErr)
	}

	for _, name := range names {
		if err := root.RemoveAll(name); err != nil {
			return fmt.Errorf("clearing %s from %s: %w", name, normalized, err)
		}
	}
	return nil
}

// cleanRestoreDestination clears target when the item asked for it, and
// reports whether anything was cleared.
//
// It declines in the two cases where clearing would destroy data the restore
// is not replacing: when the caller did not ask, and when this is a partial
// file-picker restore, where wiping the target would delete every file the
// user did not select. A target too shallow to be safely cleared degrades to
// the merge restore with a warning rather than failing the run.
func cleanRestoreDestination(item BackupItem, target string, include []string) error {
	clean, _ := item.Settings[SettingCleanDestination].(bool)
	if !clean {
		return nil
	}
	if len(include) > 0 {
		log.Printf("engine: %s is a partial restore — leaving %s as it is, because clearing it would delete the files that were not selected", item.Name, target)
		return nil
	}
	if err := clearRestoreTarget(target); err != nil {
		if errors.Is(err, errUnsafeToClean) {
			log.Printf("engine: not clearing %s before restoring %s (%v) — restoring over the existing contents instead", target, item.Name, err)
			return nil
		}
		return err
	}
	log.Printf("engine: cleared %s before restoring %s", target, item.Name)
	return nil
}
