package runner

import (
	"log"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// healthCheckTimeout caps how long a single TestConnection may take in the
// daily sweep. Individual adapters already have their own per-operation
// timeouts (5 min metadata, 4 h upload, etc.) so this is mostly a safety
// net for storage backends that hang their own TCP dial.
const healthCheckTimeout = 30 * time.Second

// RunHealthChecks calls TestConnection on every configured storage
// destination, records the outcome via UpdateStorageDestinationHealth, and
// broadcasts a WebSocket event so the UI can refresh the health badge.
// Suitable for scheduling daily; the runner's job mutex is NOT taken, so
// health checks never block scheduled backups.
//
// Failures are logged at WARN level (so they show in syslog without
// drowning successful checks) and persisted on the row. A storage that
// stayed reachable simply gets last_health_check_at updated and
// last_health_check_error cleared.
func (r *Runner) RunHealthChecks() {
	dests, err := r.db.ListStorageDestinations()
	if err != nil {
		log.Printf("runner: health check: failed to list storage destinations: %v", err)
		return
	}
	if len(dests) == 0 {
		return
	}
	log.Printf("runner: running health check across %d storage destination(s)", len(dests))
	for _, dest := range dests {
		r.checkOneStorage(dest)
	}
}

// CheckStorageDestination runs a one-shot health check against a single
// destination. Used by the API "Check now" button on the Storage page.
// Returns (status, errorMessage). Always persists the result.
func (r *Runner) CheckStorageDestination(dest db.StorageDestination) (string, string) {
	return r.checkOneStorage(dest)
}

func (r *Runner) checkOneStorage(dest db.StorageDestination) (string, string) {
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		msg := err.Error()
		_ = r.db.UpdateStorageDestinationHealth(dest.ID, "failed", msg)
		r.broadcastStorageHealth(dest.ID, "failed", msg)
		log.Printf("runner: health check FAILED for %q (id=%d): adapter construction: %v", dest.Name, dest.ID, err)
		return "failed", msg
	}
	defer storage.CloseAdapter(adapter)

	// Run TestConnection in a goroutine + timeout so a hung adapter
	// can't stall the whole sweep.
	resultCh := make(chan error, 1)
	go func() { resultCh <- adapter.TestConnection() }()

	select {
	case checkErr := <-resultCh:
		if checkErr != nil {
			msg := checkErr.Error()
			_ = r.db.UpdateStorageDestinationHealth(dest.ID, "failed", msg)
			r.broadcastStorageHealth(dest.ID, "failed", msg)
			log.Printf("runner: health check FAILED for %q (id=%d): %v", dest.Name, dest.ID, checkErr)
			return "failed", msg
		}
	case <-time.After(healthCheckTimeout):
		msg := "health check timed out after " + healthCheckTimeout.String()
		_ = r.db.UpdateStorageDestinationHealth(dest.ID, "failed", msg)
		r.broadcastStorageHealth(dest.ID, "failed", msg)
		log.Printf("runner: health check TIMEOUT for %q (id=%d)", dest.Name, dest.ID)
		return "failed", msg
	}

	if err := r.db.UpdateStorageDestinationHealth(dest.ID, "ok", ""); err != nil {
		log.Printf("runner: health check: persisting ok result for %q (id=%d): %v", dest.Name, dest.ID, err)
	}
	r.broadcastStorageHealth(dest.ID, "ok", "")
	return "ok", ""
}

func (r *Runner) broadcastStorageHealth(id int64, status, errorMsg string) {
	r.broadcast(map[string]any{
		"type":              "storage_health",
		"storage_dest_id":   id,
		"status":            status,
		"error":             errorMsg,
		"last_health_check": time.Now().UTC().Format(time.RFC3339),
	})
}
