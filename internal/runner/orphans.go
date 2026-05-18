package runner

import (
	"fmt"
	"log"
	"strings"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// ScanStorageOrphans walks a storage destination and returns every regular
// file whose path is not under any active restore point's storage_path for
// jobs targeting this destination. Used by the orphan-GC API to surface
// "files Vault doesn't recognise" before the user clicks delete.
//
// Returns (paths, totalBytes, error). paths are storage-relative, ordered
// lexicographically for stable UI rendering.
//
// What counts as an orphan:
//   - A file at the storage destination whose path does NOT begin with any
//     restore_point.storage_path of any job pointing at this destination.
//   - Vault-internal sidecars (e.g. "_vault/vault.db") are explicitly
//     excluded so the operator-installed safety-net isn't proposed for
//     deletion.
//   - Directory entries are not listed as orphans (only regular files).
func (r *Runner) ScanStorageOrphans(dest db.StorageDestination) ([]string, int64, error) {
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		return nil, 0, fmt.Errorf("adapter: %w", err)
	}
	defer storage.CloseAdapter(adapter)

	// Collect every active restore-point storage_path for jobs that
	// target this destination. Each path is treated as a prefix; any
	// file underneath belongs to a known backup.
	jobs, err := r.db.ListJobs()
	if err != nil {
		return nil, 0, fmt.Errorf("list jobs: %w", err)
	}
	known := make([]string, 0, 32)
	for _, job := range jobs {
		if job.StorageDestID != dest.ID {
			continue
		}
		rps, err := r.db.ListRestorePoints(job.ID)
		if err != nil {
			log.Printf("runner: orphan scan: skipping job %d due to RP load error: %v", job.ID, err)
			continue
		}
		for _, rp := range rps {
			if rp.StoragePath != "" {
				known = append(known, strings.Trim(rp.StoragePath, "/"))
			}
		}
	}

	orphans := make([]string, 0, 32)
	var total int64
	if err := walkStorage(adapter, "", func(p string, size int64) {
		clean := strings.Trim(p, "/")
		if clean == "" {
			return
		}
		// Vault-internal artifacts are never orphans.
		if strings.HasPrefix(clean, "_vault/") || clean == "_vault" {
			return
		}
		// Belongs to a known restore point if its path falls under
		// any known prefix.
		for _, k := range known {
			if clean == k || strings.HasPrefix(clean, k+"/") {
				return
			}
		}
		orphans = append(orphans, p)
		total += size
	}); err != nil {
		return nil, 0, err
	}
	return orphans, total, nil
}

// DeleteStorageOrphans deletes the requested paths after re-scanning to
// ensure each requested path is still genuinely orphaned. This guards
// against the case where a fresh backup ran between the user's scan and
// their delete click \xe2\x80\x94 we never delete a file that the current restore
// points reference.
//
// Returns (deletedCount, errorMessages). Per-path errors are collected
// and returned so the UI can surface them; one failure does not abort
// the rest of the deletion list.
func (r *Runner) DeleteStorageOrphans(dest db.StorageDestination, paths []string) (int, []string) {
	currentOrphans, _, err := r.ScanStorageOrphans(dest)
	if err != nil {
		return 0, []string{fmt.Sprintf("rescan failed: %v", err)}
	}
	allowed := make(map[string]struct{}, len(currentOrphans))
	for _, p := range currentOrphans {
		allowed[p] = struct{}{}
	}

	adapter, adapterErr := storage.NewAdapter(dest.Type, dest.Config)
	if adapterErr != nil {
		return 0, []string{fmt.Sprintf("adapter: %v", adapterErr)}
	}
	defer storage.CloseAdapter(adapter)

	var (
		deleted int
		errors  []string
	)
	for _, p := range paths {
		if _, ok := allowed[p]; !ok {
			errors = append(errors, fmt.Sprintf("%s: no longer orphaned (skipped)", p))
			continue
		}
		if err := adapter.Delete(p); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", p, err))
			continue
		}
		deleted++
	}
	log.Printf("runner: orphan GC for %q: requested=%d deleted=%d errors=%d", dest.Name, len(paths), deleted, len(errors))
	return deleted, errors
}

// walkStorage recursively walks a storage adapter's tree, invoking cb for
// every regular file (not directories). path is storage-relative. Iterates
// breadth-first via a stack so deeply nested adapters don't blow Go's
// recursion budget on, say, S3 prefixes with millions of objects.
func walkStorage(adapter storage.Adapter, prefix string, cb func(string, int64)) error {
	stack := []string{prefix}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := adapter.List(cur)
		if err != nil {
			return fmt.Errorf("list %q: %w", cur, err)
		}
		for _, e := range entries {
			if e.IsDir {
				stack = append(stack, e.Path)
				continue
			}
			cb(e.Path, e.Size)
		}
	}
	return nil
}
