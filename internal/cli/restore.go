package cli

import (
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/docsmeta"
)

// restoreWithFallback attempts to restore the database from the freshest
// integrity-passing snapshot source and returns information about which
// source was used (or that a fresh database was started).
//
// Candidate sources:
//   - Configured snapshot path (from vault.cfg SNAPSHOT_PATH)  → "primary"
//   - Default cache path (/mnt/cache/.vault/vault.db)          → "default_cache"
//   - Newest rotated copies next to the snapshots              → "rotated"
//   - USB-direct live DB (/boot/.../vault.db)                  → "usb_direct"
//   - USB backup (/boot/.../vault.db.backup)                   → "usb_backup"
//   - Fresh database (no restoration)                          → "fresh"
//
// Candidates are ordered by modification time (newest first), with the tier
// order above as tie-break, instead of a strict tier order. A strict order
// restored a stale-but-valid cache snapshot over newer state when boots
// alternated between hybrid and USB-direct modes — a USB-direct boot writes
// the newest configuration to the live USB DB, which the old chain never
// even considered, so every later hybrid boot silently reverted the
// configuration (issue #241).
func restoreWithFallback(sm *db.SnapshotManager, configuredPath, defaultCachePath, usbLiveDBPath, usbBackupPath string) *db.RestorationInfo {
	type candidate struct {
		label  string
		path   string
		reason string
		tier   int
		mod    int64
	}

	var candidates []candidate
	addCandidate := func(label, path, reason string, tier int) {
		if path == "" {
			return
		}
		fi, err := os.Stat(path)
		if err != nil || fi.Size() == 0 {
			return
		}
		candidates = append(candidates, candidate{
			label: label, path: path, reason: reason,
			tier: tier, mod: newestDBMtime(path),
		})
	}

	addCandidate("primary", configuredPath, "restored from configured snapshot path", 0)
	if defaultCachePath != configuredPath {
		addCandidate("default_cache", defaultCachePath, "restored from default cache snapshot", 1)
	}
	// Rotated copies (written by rotateSnapshot after every successful save)
	// sit next to the snapshot (issue #182).
	for _, rotated := range newestRotatedSnapshots(configuredPath, defaultCachePath) {
		addCandidate("rotated", rotated, "restored from rotated snapshot copy", 2)
	}
	// The live USB DB is written by USB-direct boots (pool unavailable) and
	// is then the freshest state on the system — see issue #241 above.
	addCandidate("usb_direct", usbLiveDBPath, "restored from USB-direct database (newest available state)", 3)
	addCandidate("usb_backup", usbBackupPath, "restored from USB flash backup", 4)

	// Newest first on raw mtime; tier order breaks exact ties only. Exact
	// ties are the normal case for same-flush copies: rotated copies are
	// hard-linked from the primary, and FlushToUSB aligns the USB shadow's
	// mtime with the primary snapshot it was written alongside (see
	// SnapshotManager.alignUSBTwinMtime). A USB shadow written while the
	// primary save FAILED keeps its own newer mtime and correctly outranks
	// the stale primary — freshness is never traded away for tier order.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].mod != candidates[j].mod {
			return candidates[i].mod > candidates[j].mod
		}
		return candidates[i].tier < candidates[j].tier
	})

	for _, c := range candidates {
		if err := sm.RestoreFromPath(c.path); err != nil {
			log.Printf("Warning: failed to restore from %s path %s: %v", c.label, c.path, err)
			continue
		}
		if err := sm.IntegrityCheck(); err != nil {
			log.Printf("Warning: %s snapshot %s failed integrity check: %v", c.label, c.path, err)
			continue
		}
		return &db.RestorationInfo{
			Source: c.label,
			Path:   c.path,
			Reason: c.reason + " (freshest valid source)",
		}
	}
	log.Println("Warning: no valid snapshot source available — starting with fresh database")
	return &db.RestorationInfo{
		Source: "fresh",
		Path:   "",
		Reason: "no snapshot files passed integrity check; configuration will need to be reconfigured",
	}
}

// newestDBMtime returns the newest modification time (UnixNano) across a
// database file and its -wal sidecar. A live WAL-mode DB can hold committed
// transactions only in the sidecar after an abrupt stop, leaving the main
// file's mtime stale; freshness must reflect the newest committed state.
// SQLite applies the WAL when the file is opened/restored. Snapshot
// artifacts have no sidecars, so for them this is just the file mtime.
func newestDBMtime(path string) int64 {
	var mod int64
	if fi, err := os.Stat(path); err == nil {
		mod = fi.ModTime().UnixNano()
	}
	if wfi, err := os.Stat(path + "-wal"); err == nil && wfi.ModTime().UnixNano() > mod {
		mod = wfi.ModTime().UnixNano()
	}
	return mod
}

// maxRotatedCandidates caps how many rotated snapshot copies the fallback
// tier will attempt, newest first.
const maxRotatedCandidates = 3

// newestRotatedSnapshots returns rotated snapshot copies (newest first,
// max maxRotatedCandidates total) from the rotated/ directories next to the given snapshot
// paths. Ordering uses each file's modification time — basenames only sort
// chronologically within one directory sharing a prefix, so a lexical sort
// is not a valid global order across configured/default rotated dirs.
func newestRotatedSnapshots(paths ...string) []string {
	type candidate struct {
		path string
		mod  int64
	}
	seen := map[string]bool{}
	var candidates []candidate
	for _, p := range paths {
		if p == "" {
			continue
		}
		dir := filepath.Join(filepath.Dir(p), "rotated")
		if seen[dir] {
			continue
		}
		seen[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			candidates = append(candidates, candidate{
				path: filepath.Join(dir, e.Name()),
				mod:  info.ModTime().UnixNano(),
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mod > candidates[j].mod })
	out := make([]string, 0, maxRotatedCandidates)
	for i, c := range candidates {
		if i >= maxRotatedCandidates {
			break
		}
		out = append(out, c.path)
	}
	return out
}

// validateConfiguredPaths checks that user-configured paths (snapshot override,
// staging override) are accessible and logs warnings for any that are not.
func validateConfiguredPaths(database *db.DB) {
	if snapOverride, err := database.GetSetting("snapshot_path_override", docsmeta.DefaultFor("snapshot_path_override")); err == nil && snapOverride != "" {
		if _, err := os.Stat(snapOverride); err != nil {
			log.Printf("Warning: configured snapshot_path_override is not accessible: %s (%v)", snapOverride, err)
		}
	}

	if stagingOverride, err := database.GetSetting("staging_dir_override", docsmeta.DefaultFor("staging_dir_override")); err == nil && stagingOverride != "" {
		if fi, err := os.Stat(stagingOverride); err != nil || !fi.IsDir() {
			log.Printf("Warning: configured staging_dir_override is not accessible: %s", stagingOverride)
		}
	}
}
