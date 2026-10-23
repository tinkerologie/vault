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
	// An imported AppData Backup restore point is the archive file itself,
	// which a recursive directory delete cannot remove. Delete it directly,
	// but only when it has the same shape the import accepted.
	if isAppdataArchivePath(storagePath) {
		if fi, err := adapter.Stat(storagePath); err == nil && !fi.IsDir {
			if err := adapter.Delete(storagePath); err != nil && !storage.IsNotExist(err) {
				return fmt.Errorf("delete %s: %w", storagePath, err)
			}
			return nil
		}
	}
	return r.DeleteStorageDir(adapter, storagePath)
}

// isAppdataArchivePath reports whether p names an AppData Backup archive:
// a .tar.gz or .zip file directly inside an ab_* folder.
func isAppdataArchivePath(p string) bool {
	name := path.Base(p)
	isArchive := strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".zip")
	return isArchive && strings.HasPrefix(path.Base(path.Dir(p)), "ab_")
}

// hasManifest reports whether dir holds a Vault manifest.json. A file with
// that name alone is not enough evidence (web apps and browser extensions
// use it too), so the file must be an encrypted Vault manifest envelope or
// a JSON object with the key set every Vault manifest has (isVaultManifest)
// whose job_name is the run's parent directory (jobNameMatchesDir).
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
	return isVaultManifest(m) && jobNameMatchesDir(m["job_name"].(string), path.Dir(path.Clean(dir)))
}

// jobNameMatchesDir reports whether a manifest's job_name is the directory
// its run sits in. Vault always writes <job name>/<run>/manifest.json, so a
// manifest under any other parent is not Vault's. S3 destinations store the
// name with sanitised segments ("My Job" as "My_Job"), which also matches.
func jobNameMatchesDir(jobName, parent string) bool {
	if jobName == parent {
		return true
	}
	js := strings.Split(jobName, "/")
	ps := strings.Split(parent, "/")
	if len(js) != len(ps) {
		return false
	}
	for i := range js {
		if storage.SanitizeKeySegment(js[i]) != ps[i] {
			return false
		}
	}
	return true
}

// isVaultManifest reports whether a decoded plaintext manifest has the keys
// every Vault manifest writer has emitted since the first release: job_name
// and backup_type strings plus numeric run_id and items_done. A single
// common key such as job_name is not enough evidence on its own, because
// other software can write it (#514).
func isVaultManifest(m map[string]any) bool {
	_, jobName := m["job_name"].(string)
	_, backupType := m["backup_type"].(string)
	_, runID := m["run_id"].(float64)
	_, itemsDone := m["items_done"].(float64)
	return jobName && backupType && runID && itemsDone
}

// sweepManagedRunDirs deletes every child directory of dir that contains a
// Vault manifest.json. Files and directories without one survive. dir itself
// is removed only when a deleted run left it empty (DeleteStorageDir sweeps
// the parents it emptied), so an empty folder a user created is kept. dir
// must be a relative, non-root path; a missing dir is already clean.
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
		if !isAppdataArchivePath(storagePath) {
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
