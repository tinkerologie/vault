package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"strings"
	"sync"

	"github.com/tinkerologie/vault/internal/bounded"
	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/maintlock"
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
//   - a run directory found by layout whose manifest.json is verified to be
//     Vault's and to belong to that directory (hasManifest);
//   - the dedup repository's own subpaths.
//
// Anything else is left in place, where the orphan scan can show it.

// ErrCleanupRefused marks a delete that was refused because the target is
// not a Vault-managed restore point path.
var ErrCleanupRefused = errors.New("refusing to delete a path outside Vault's managed backup layout")

// ErrCleanupUnavailable marks a destination cleanup that could not start at
// all (for example the adapter could not be built), so nothing was deleted.
var ErrCleanupUnavailable = errors.New("destination cleanup could not run")

// deleteJobRestorePointDir is deleteRestorePointDir for a restore point of one
// job among others on a destination. A path another job's restore point also
// covers is left alone: it holds that job's data too, and rows written by older
// versions may overlap. Uncertainty refuses the delete.
func (r *Runner) deleteJobRestorePointDir(adapter storage.Adapter, destID, jobID int64, storagePath string) error {
	if err := r.refuseIfHeldByOtherJob(destID, jobID, storagePath); err != nil {
		log.Printf("runner: refusing to delete restore point path %q: %v", storagePath, err)
		return err
	}
	return r.deleteRestorePointDir(adapter, storagePath)
}

// refuseIfHeldByOtherJob returns an ErrCleanupRefused error when another job
// has a restore point at, above or below p on this destination.
func (r *Runner) refuseIfHeldByOtherJob(destID, jobID int64, p string) error {
	owners, err := r.db.ListRestorePointOwners(destID, jobID)
	if err != nil {
		return fmt.Errorf("%w: %q: checking other jobs' restore points: %w", ErrCleanupRefused, p, err)
	}
	for _, o := range owners {
		if storage.PathsOverlap(p, o.StoragePath) {
			return fmt.Errorf("%w: %q overlaps a restore point of job %d", ErrCleanupRefused, p, o.JobID)
		}
	}
	return nil
}

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

// errDestinationAdopting reports a destructive operation refused because files
// on the destination are still becoming restore points.
var errDestinationAdopting = errors.New("an import or replication sync is running on this destination; try again when it finishes")

// manifestUnsealer decrypts an encrypted manifest envelope, authenticating it.
type manifestUnsealer func(manifestEnvelope) ([]byte, error)

// unsealManifest decrypts an envelope with the destination's dedup repository
// or the backup passphrase, whichever its key scheme names.
func unsealManifest(env manifestEnvelope, openDedup func() (*dedup.Repo, error), passphrase string) ([]byte, error) {
	cipherBytes, err := decodeManifestEnvelope(env)
	if err != nil {
		return nil, err
	}
	switch env.Key {
	case "dedup":
		repo, err := openDedup()
		if err != nil {
			return nil, err
		}
		return repo.DecryptManifest(cipherBytes)
	case "age":
		if passphrase == "" {
			return nil, errors.New("no backup password available")
		}
		rd, err := crypto.DecryptReader(passphrase, bytes.NewReader(cipherBytes))
		if err != nil {
			return nil, err
		}
		defer func() { _ = rd.Close() }()
		return bounded.ReadAll(rd, bounded.ManifestBytes)
	default:
		return nil, fmt.Errorf("unknown manifest key scheme %q", env.Key)
	}
}

// destManifestUnsealer unseals manifests with this server's own keys. The dedup
// repository is opened at most once, on first use.
func (r *Runner) destManifestUnsealer(adapter storage.Adapter, dest db.StorageDestination) manifestUnsealer {
	openDedup := sync.OnceValues(func() (*dedup.Repo, error) {
		return r.openExistingDedupRepo(adapter, dest)
	})
	return func(env manifestEnvelope) ([]byte, error) {
		return unsealManifest(env, openDedup, r.resolvePassphrase())
	}
}

// hasManifest reports whether dir holds a Vault manifest.json. A file with
// that name alone is not enough evidence (web apps and browser extensions
// use it too), so the content must be a JSON object with the key set every
// Vault manifest has (isVaultManifest) whose job_name is the run's parent
// directory (jobNameMatchesDir). An encrypted envelope must decrypt to the same:
// its vault_manifest_enc marker is one unauthenticated JSON field, so a manifest
// this server cannot decrypt proves nothing and the directory is kept.
// The whole file is read, because a truncated read would misclassify a real
// backup, up to bounded.ManifestBytes. A larger file counts as not a manifest,
// which only ever keeps data.
func hasManifest(adapter storage.Adapter, dir string, unseal manifestUnsealer) bool {
	p := path.Join(dir, "manifest.json")
	fi, err := adapter.Stat(p)
	if err != nil || fi.IsDir || fi.Size > bounded.ManifestBytes {
		return false
	}
	rc, err := adapter.Read(p)
	if err != nil {
		return false
	}
	data, err := bounded.ReadAll(rc, bounded.ManifestBytes)
	_ = rc.Close()
	if err != nil {
		return false
	}
	isEnv, env := detectManifestEnvelope(data)
	if isEnv {
		data, err = unseal(env)
		if err != nil {
			log.Printf("runner: not treating %s as a Vault backup: its encrypted manifest could not be verified: %v", dir, err)
			return false
		}
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
//
// A non-zero jobID sweeps on behalf of that job, keeping anything another job
// has a restore point for. Zero means every job on dest is being removed.
func (r *Runner) sweepManagedRunDirs(adapter storage.Adapter, dest db.StorageDestination, jobID int64, dir string) error {
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
	// A run directory found by layout may be one a sync is still downloading.
	// Sweeping for all jobs, the caller already holds the destination.
	if jobID != 0 {
		release, claimed := maintlock.TryExclusiveDestination(r.db, dest.ID)
		if !claimed {
			return fmt.Errorf("sweep of %s left for later: %w", dir, errDestinationAdopting)
		}
		defer release()
	}
	unseal := r.destManifestUnsealer(adapter, dest)
	var errs []error
	for _, e := range entries {
		if !e.IsDir || !hasManifest(adapter, e.Path, unseal) {
			continue
		}
		del := r.deleteRestorePointDir
		if jobID != 0 {
			del = func(a storage.Adapter, p string) error {
				return r.deleteJobRestorePointDir(a, dest.ID, jobID, p)
			}
		}
		if err := del(adapter, e.Path); err != nil {
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
func checkImportPathOwned(adapter storage.Adapter, entry map[string]any, storagePath string, unseal manifestUnsealer) error {
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
	if !hasManifest(adapter, storagePath, unseal) {
		return fmt.Errorf("no verifiable Vault manifest.json at that path on the destination")
	}
	return nil
}
