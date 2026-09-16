package runner

import (
	"fmt"
	"log"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// maxRunPathAttempts bounds the disambiguation search. Reaching it means
// something other than a same-second collision is going on, and silently
// looping forever would be worse than falling back to the plain name.
const maxRunPathAttempts = 50

// uniqueRunPath returns basePath, or the first "<basePath>-N" that does not
// already exist at dest.
//
// The run ID used to lead the run folder name and was the only thing
// guaranteeing it was unique. Dropping it for issue #319 leaves a
// second-resolution timestamp, which two back-to-back manual runs of a fast
// job can share — and the storage adapters overwrite silently, so a collision
// would destroy the earlier run's data with no error anywhere.
//
// When the destination cannot answer whether a path is free, the run is not
// refused — but neither is the plain name assumed safe. It falls back to
// "<basePath>-r<runID>", which is unique by construction because run IDs are,
// so an unreachable destination can never cost an earlier run its data.
func uniqueRunPath(dest db.StorageDestination, basePath string, runID int64) string {
	// Unique without asking the destination anything: run IDs are unique.
	fallback := fmt.Sprintf("%s-r%d", basePath, runID)

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		log.Printf("runner: cannot check %s for an existing run folder: %v (using %s)", dest.Name, err, fallback)
		return fallback
	}
	defer storage.CloseAdapter(adapter)

	candidate := basePath
	for attempt := 2; attempt <= maxRunPathAttempts; attempt++ {
		entries, listErr := adapter.List(candidate)
		if listErr == nil {
			if len(entries) == 0 {
				return candidate
			}
			candidate = fmt.Sprintf("%s-%d", basePath, attempt)
			continue
		}
		if !storage.IsNotExist(listErr) {
			log.Printf("runner: %s cannot list candidate %s (%v) — using %s rather than risk overwriting a run", dest.Name, candidate, listErr, fallback)
			return fallback
		}
		// Candidate does not exist. Verify the destination root is listable so
		// a completely missing/unmounted destination root (which also produces NotExist)
		// falls back to the run ID instead.
		if _, probeErr := adapter.List(""); probeErr != nil {
			log.Printf("runner: %s destination root cannot be listed (%v) — using %s rather than risk overwriting a run", dest.Name, probeErr, fallback)
			return fallback
		}
		return candidate
	}

	log.Printf("runner: %s already has %d run folders for this timestamp — using %s", dest.Name, maxRunPathAttempts, fallback)
	return fallback
}
