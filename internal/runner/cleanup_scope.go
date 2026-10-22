package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"strings"

	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/safepath"
	"github.com/tinkerologie/vault/internal/storage"
)

// Destructive cleanup is scoped to data Vault can prove it owns (issue #514).
// A destination root may hold unrelated files, and before #514 a
// destination that resolved to /usr/local/emhttp let "Delete Storage &
// Files" recursively erase the Unraid WebGUI. Every recursive delete now
// targets one of:
//   - a restore point path recorded in the database, which must be a
//     relative <job>/<run> path (never the root or a single top-level dir);
//   - a run directory found by layout that contains manifest.json, the same
//     evidence ScanStorageManifests uses to discover backups;
//   - the dedup repository's own subpaths.
//
// Anything else is left in place, where the orphan scan can show it.

// ErrCleanupRefused marks a delete that was refused because the target is
// not a Vault-managed restore point path.
var ErrCleanupRefused = errors.New("refusing to delete a path outside Vault's managed backup layout")

// ErrCleanupUnavailable marks a destination cleanup that could not start at
// all (for example the adapter could not be built), so nothing was deleted.
var ErrCleanupUnavailable = errors.New("destination cleanup could not run")

// deleteRestorePointDir recursively deletes one restore point's directory
// after checking that storagePath has the managed <job>/<run> shape.
func (r *Runner) deleteRestorePointDir(adapter storage.Adapter, storagePath string) error {
	if _, err := safepath.NormalizeRestorePointPath(storagePath); err != nil {
		log.Printf("runner: refusing to delete restore point path %q: %v", storagePath, err)
		return fmt.Errorf("%w: %q: %w", ErrCleanupRefused, storagePath, err)
	}
	return r.DeleteStorageDir(adapter, storagePath)
}

// hasManifest reports whether dir holds a Vault manifest.json. A file with
// that name alone is not enough evidence (web apps and browser extensions
// use it too), so the file must be an encrypted Vault manifest envelope or
// a JSON object with a string job_name, which writeManifest always writes.
// The whole file is read: manifests carry per-item checksums and have no
// size bound, and a truncated read would misclassify a real backup.
func hasManifest(adapter storage.Adapter, dir string) bool {
	p := path.Join(dir, "manifest.json")
	fi, err := adapter.Stat(p)
	if err != nil || fi.IsDir {
		return false
	}
	rc, err := adapter.Read(p)
	if err != nil {
		return false
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return false
	}
	if isEnv, _ := detectManifestEnvelope(data); isEnv {
		return true
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return false
	}
	_, ok := m["job_name"].(string)
	return ok
}

// removeEmptyDir removes dir when the adapter supports directory removal
// and dir is empty. A non-empty or missing dir is not an error here.
func removeEmptyDir(adapter storage.Adapter, dir string) {
	if dr, ok := adapter.(interface{ RemoveEmptyDir(string) error }); ok {
		_ = dr.RemoveEmptyDir(dir)
	}
}

// sweepManagedRunDirs deletes every child directory of dir that contains a
// manifest.json, then removes dir itself if that left it empty. Files and
// directories without a manifest survive. dir must be a relative, non-root
// path; a missing dir is already clean.
func (r *Runner) sweepManagedRunDirs(adapter storage.Adapter, dir string) error {
	if _, err := safepath.NormalizeRelative(dir, false); err != nil {
		return fmt.Errorf("%w: %q: %w", ErrCleanupRefused, dir, err)
	}
	entries, err := adapter.List(dir)
	if err != nil {
		if storage.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("list %s: %w", dir, err)
	}
	var errs []error
	for _, e := range entries {
		if !e.IsDir || !hasManifest(adapter, e.Path) {
			continue
		}
		if err := r.deleteRestorePointDir(adapter, e.Path); err != nil {
			errs = append(errs, err)
		}
	}
	removeEmptyDir(adapter, dir)
	r.sweepEmptyParents(adapter, dir, "")
	return errors.Join(errs...)
}

// removeDedupRepoFiles deletes the dedup repository's own subpaths and its
// header backups. It never removes the _vault root, which also holds the
// daemon's database backups (issue #183).
func (r *Runner) removeDedupRepoFiles(adapter storage.Adapter) []error {
	var errs []error
	for _, sub := range dedup.RepoSubpaths() {
		var e error
		if fi, statErr := adapter.Stat(sub); statErr == nil && !fi.IsDir {
			e = adapter.Delete(sub) // plain file (repo.json)
		} else {
			e = r.DeleteStorageDir(adapter, sub) // directory (packs/, index/); no-op when absent
		}
		if e != nil && !storage.IsNotExist(e) {
			errs = append(errs, fmt.Errorf("remove dedup repo path %s: %w", sub, e))
		}
	}
	// Header copies kept by a key rewrap (#451) hold a sealed master key too.
	backups, err := dedup.HeaderBackups(adapter)
	if err != nil {
		errs = append(errs, fmt.Errorf("list dedup header backups: %w", err))
	}
	for _, b := range backups {
		if e := adapter.Delete(b); e != nil && !storage.IsNotExist(e) {
			errs = append(errs, fmt.Errorf("remove dedup header backup %s: %w", b, e))
		}
	}
	return errs
}

// checkImportPathOwned verifies that an import entry's storage path holds
// Vault-recognised backup data on the destination before a restore point
// row is created for it. A Vault backup must be a run directory with a
// manifest.json. An appdata.backup entry must be the archive file itself
// inside an ab_* folder, which is how ScanAppdataBackups reports it.
func checkImportPathOwned(adapter storage.Adapter, entry map[string]any, storagePath string) error {
	if source, _ := entry["source"].(string); source == "appdata.backup" {
		name := path.Base(storagePath)
		isArchive := strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".zip")
		if !strings.HasPrefix(path.Base(path.Dir(storagePath)), "ab_") || !isArchive {
			return fmt.Errorf("not an appdata.backup archive path")
		}
		fi, err := adapter.Stat(storagePath)
		if err != nil {
			return fmt.Errorf("appdata.backup archive not found on the destination: %w", err)
		}
		if fi.IsDir {
			return fmt.Errorf("appdata.backup archive path is a directory")
		}
		return nil
	}
	if !hasManifest(adapter, storagePath) {
		return fmt.Errorf("no manifest.json at that path on the destination")
	}
	return nil
}
