package engine

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/safepath"
)

// Restoring a backup has to put back what the backup captured, and on Unraid
// that includes who owns the files. Container appdata is almost always owned
// by an unprivileged account (`nobody:users`, 99:100) while Vault's daemon
// runs as root, so anything a restore creates is root-owned unless it is
// explicitly chowned back. A container that then starts as its own user
// cannot read its own config — the failure mode that made a container
// restored into a fresh directory refuse to start at all.
//
// Ownership is best-effort by design: a restore that put the bytes back but
// could not set an owner is still a restore, and the daemon may legitimately
// be running unprivileged or writing to a filesystem with no ownership model
// at all (exFAT, a mounted share). Failures are logged once rather than once
// per entry, so such a destination cannot flood the log.

var chownWarnOnce sync.Once

// restoredMeta is the metadata to put back on one restored path. A nil mode
// leaves the mode alone, negative ids follow chown(2) ("leave unchanged"), and a
// zero time leaves the mtime alone.
type restoredMeta struct {
	mode    *os.FileMode
	uid     int
	gid     int
	modTime time.Time
}

// applyRestoredMeta applies m to path through a handle pinned to its parent, so
// a directory swapped for a symlink after validation cannot redirect the update
// outside the restore. base is the restore tree path must stay in; empty means
// the approved restore roots. Symlinks are chowned themselves and otherwise left
// alone. Failures are logged, never fatal: the bytes are already back.
func applyRestoredMeta(base, path string, m restoredMeta) {
	if !restorePathSafe(path) {
		log.Printf("engine: restore: refusing to set metadata on suspicious path %q", path)
		return
	}
	root, name, err := pinRestoredParent(base, path)
	if err != nil {
		log.Printf("engine: restore: refusing to set metadata on %s: %v", path, err)
		return
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil {
		log.Printf("engine: restore: could not inspect %s: %v", path, err)
		return
	}
	isLink := info.Mode()&os.ModeSymlink != 0
	if m.mode != nil && !isLink {
		if err := root.Chmod(name, m.mode.Perm()); err != nil {
			log.Printf("engine: restore: could not set mode on %s: %v", path, err)
		}
	}
	if m.uid >= 0 || m.gid >= 0 {
		if err := root.Lchown(name, m.uid, m.gid); err != nil {
			chownWarnOnce.Do(func() {
				log.Printf("engine: restore: could not set ownership of %s to %d:%d: %v "+
					"(restored files keep the daemon's own owner; further occurrences are not logged)", path, m.uid, m.gid, err)
			})
		}
	}
	if !m.modTime.IsZero() {
		if isLink {
			log.Printf("engine: restore: refusing to set mtime on symlink %q", path)
		} else if err := root.Chtimes(name, m.modTime, m.modTime); err != nil {
			log.Printf("engine: restore: setting mtime on %s: %v", path, err)
		}
	}
}

// pinRestoredParent resolves path's parent, checks it stays inside base (or the
// approved roots), and returns a pinned handle to it with path's final component.
func pinRestoredParent(base, path string) (*os.Root, string, error) {
	cleaned := filepath.Clean(path)
	parent, err := evalExistingPrefix(filepath.Dir(cleaned))
	if err != nil {
		return nil, "", err
	}
	name := filepath.Base(cleaned)
	if base != "" {
		resolvedBase, err := evalExistingPrefix(base)
		if err != nil {
			return nil, "", err
		}
		if !pathWithin(resolvedBase, filepath.Join(parent, name)) {
			return nil, "", fmt.Errorf("resolves outside %s", base)
		}
	} else {
		allowed, err := restorePathWithinAllowedRoots(parent)
		if err != nil {
			return nil, "", err
		}
		if !allowed {
			return nil, "", fmt.Errorf("resolves outside the approved restore roots")
		}
	}
	root, err := safepath.OpenPinnedDir(parent)
	if err != nil {
		return nil, "", err
	}
	return root, name, nil
}

func pathWithin(base, p string) bool {
	return p == base || strings.HasPrefix(p, base+string(filepath.Separator))
}

// applyOwner chowns path to uid/gid, following the same "negative means leave
// alone" convention as chown(2), so an unknown owner (-1, recorded by backups
// taken before ownership was captured) is a no-op rather than a reset to root.
// Symlinks are chowned themselves, never their target.
func applyOwner(path string, uid, gid int) {
	if uid < 0 && gid < 0 {
		return
	}
	applyRestoredMeta("", path, restoredMeta{uid: uid, gid: gid})
}

// applyVolumeRootMeta restores the mode and ownership of a container volume's
// own root directory from what the backup manifest recorded. A zero mode means
// the backup predates this metadata: the directory is then left exactly as the
// restore created it, which is the behaviour those backups already had.
//
// It runs AFTER the volume's contents are extracted, for the same reason the
// tar extractor defers directory modes — a mode with no write bit for the
// daemon would otherwise block the extraction it precedes.
func applyVolumeRootMeta(path string, mode uint32, uid, gid int) {
	if mode == 0 {
		return
	}
	applyMode(path, os.FileMode(mode))
	applyOwner(path, uid, gid)
}

// setManifestOwner records a path's numeric owner on a dedup manifest entry.
// Nothing is recorded when the platform has no notion of one, which keeps the
// manifest honest: a missing owner means "leave it alone on restore" rather
// than "set it to root".
func setManifestOwner(entry *dedup.ManifestEntry, info os.FileInfo) {
	uid, gid := fileOwner(info)
	if uid < 0 || gid < 0 {
		return
	}
	entry.UID = &uid
	entry.GID = &gid
}

// volumePointerEntry builds the synthetic `__vol__<destination>` manifest entry
// that points at a volume's own sub-manifest, carrying the mount root's mode
// and ownership. The sub-manifest describes the volume's contents but never
// the directory holding them — the walk that builds it starts at the root's
// children — so without this a volume restored into a new destination would
// get a freshly invented root directory instead of the one it was backed up
// from.
func volumePointerEntry(source string, sub dedup.ID) dedup.ManifestEntry {
	entry := dedup.ManifestEntry{Size: 0, Chunks: []dedup.ID{sub}}
	info, err := os.Stat(source)
	if err != nil {
		log.Printf("engine: chunked: could not record root metadata for volume %s: %v", source, err)
		return entry
	}
	entry.Mode = uint32(info.Mode().Perm())
	setManifestOwner(&entry, info)
	return entry
}

// restorePathSafe reports whether p is an absolute path with no ".." element.
// Every caller below has already resolved its path against the restore root —
// through safepath.JoinUnderBase or joinArchiveTarget plus resolveWithinBase —
// so this is defence in depth rather than the primary control. It is also the
// sanitiser barrier CodeQL's go/path-injection query recognises, matching the
// element-wise check used by validateSnapshotPath and validateKeyFilePath (a
// substring test would reject a legitimate name like "backups..2026").
func restorePathSafe(p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	if strings.Contains(p, "../") || strings.Contains(p, "..\\") {
		return false
	}
	return !slices.Contains(strings.Split(filepath.ToSlash(p), "/"), "..")
}

// applyMode sets a restored path's permission bits. Restores go through this
// rather than os.Chmod directly: OpenFile only applies its mode when it
// creates the file and MkdirAll leaves an existing directory alone, so
// restoring over an existing tree would otherwise keep the stale permissions.
func applyMode(path string, mode os.FileMode) {
	applyRestoredMeta("", path, restoredMeta{mode: &mode, uid: -1, gid: -1})
}

// applyModTimeFromTime sets a restored path's modification time from a time.Time,
// through the same pinned handle as applyMode.
func applyModTimeFromTime(path string, t time.Time) {
	if t.IsZero() {
		return
	}
	applyRestoredMeta("", path, restoredMeta{uid: -1, gid: -1, modTime: t})
}

// applyModTime sets a restored path's modification time. Restores go through
// this rather than os.Chtimes directly: like applyMode and applyOwner, it
// validates the path through restorePathSafe and swallows parse/chtimes errors
// so time-setting never fails a restore whose bytes are already back.
func applyModTime(path string, mtime string) {
	if t, err := time.Parse(time.RFC3339, mtime); err == nil {
		applyModTimeFromTime(path, t)
	}
}

// mkdirRestored creates a restored directory, forcing the daemon's own
// rwx bits on regardless of the recorded mode: the entries beneath it still
// have to be written, and a directory restored read-only would lock the
// extractor out of its own children. applyMode puts the recorded mode back
// once nothing more will be written inside.
//
// The tree is created through a handle pinned to the deepest existing ancestor,
// so a swapped ancestor cannot redirect the creation.
func mkdirRestored(path string, mode os.FileMode) error {
	if !restorePathSafe(path) || strings.Contains(path, "../") || strings.Contains(path, "..\\") {
		return fmt.Errorf("refusing to create suspicious path %q", path)
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to restore directory through symlink at %s", path)
	}
	resolved, err := evalExistingPrefix(path)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", path, err)
	}
	allowed, err := restorePathWithinAllowedRoots(resolved)
	if err != nil {
		return fmt.Errorf("checking %s: %w", path, err)
	}
	if !allowed {
		return fmt.Errorf("refusing to create %s: it resolves outside the approved restore roots", path)
	}
	existing, rest := resolved, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspecting %s: %w", existing, err)
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = filepath.Dir(existing)
	}
	root, err := safepath.OpenPinnedDir(existing)
	if err != nil {
		return fmt.Errorf("refusing to create %s: %w", path, err)
	}
	defer root.Close()
	if rest == "" {
		return nil
	}
	return root.MkdirAll(rest, mode.Perm()|0o700)
}
