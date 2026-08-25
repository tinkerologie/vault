package runner

import (
	"context"
	"encoding/json"
	"fmt"
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

// healthCheckActivity builds the activity-log fields for one destination
// health check so the wording is unit-testable and shared between the daily
// sweep and single-destination "Check now" path (issue #328).
func healthCheckActivity(dest db.StorageDestination, status, errMsg string, duration time.Duration) (level, message, details string) {
	level = "info"
	message = fmt.Sprintf("Storage health check, destination=%s, status=%s", dest.Name, status)
	if status != "ok" {
		level = "warn"
		if errMsg != "" {
			message = fmt.Sprintf("Storage health check, destination=%s, status=%s, error=%s", dest.Name, status, errMsg)
		} else {
			message = fmt.Sprintf("Storage health check, destination=%s, status=%s", dest.Name, status)
		}
	}
	raw, err := json.Marshal(map[string]any{
		"storage_dest_id": dest.ID,
		"status":          status,
		"error":           errMsg,
		"duration_ms":     duration.Milliseconds(),
	})
	if err != nil {
		return level, message, "{}"
	}
	return level, message, string(raw)
}

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
	started := time.Now()
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		msg := err.Error()
		_ = r.db.UpdateStorageDestinationHealth(dest.ID, "failed", msg)
		r.recordBreakerOutcome(dest.ID, false)
		r.broadcastStorageHealth(dest.ID, "failed", msg)
		lv, ms, dt := healthCheckActivity(dest, "failed", msg, time.Since(started))
		r.logActivity(lv, "health", ms, dt)
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
			r.recordBreakerOutcome(dest.ID, false)
			r.broadcastStorageHealth(dest.ID, "failed", msg)
			lv, ms, dt := healthCheckActivity(dest, "failed", msg, time.Since(started))
			r.logActivity(lv, "health", ms, dt)
			log.Printf("runner: health check FAILED for %q (id=%d): %v", dest.Name, dest.ID, checkErr)
			return "failed", msg
		}
	case <-time.After(healthCheckTimeout):
		msg := "health check timed out after " + healthCheckTimeout.String()
		_ = r.db.UpdateStorageDestinationHealth(dest.ID, "failed", msg)
		r.recordBreakerOutcome(dest.ID, false)
		r.broadcastStorageHealth(dest.ID, "failed", msg)
		lv, ms, dt := healthCheckActivity(dest, "failed", msg, time.Since(started))
		r.logActivity(lv, "health", ms, dt)
		log.Printf("runner: health check TIMEOUT for %q (id=%d)", dest.Name, dest.ID)
		return "failed", msg
	}

	if err := r.db.UpdateStorageDestinationHealth(dest.ID, "ok", ""); err != nil {
		log.Printf("runner: health check: persisting ok result for %q (id=%d): %v", dest.Name, dest.ID, err)
	}
	r.recordBreakerOutcome(dest.ID, true)
	r.broadcastStorageHealth(dest.ID, "ok", "")
	lv, ms, dt := healthCheckActivity(dest, "ok", "", time.Since(started))
	r.logActivity(lv, "health", ms, dt)

	// Capacity probe runs ONLY when TestConnection succeeded. Failures
	// here NEVER flip the health verdict — capacity is informational.
	// Uses its own 60s ceiling so a slow probe doesn't extend the sweep.
	capacity, capErr := r.probeCapacity(context.Background(), dest, adapter)

	// Sample free/total for capacity trajectory detection, reusing the
	// capacity the probe above already fetched rather than re-probing the
	// backend a second time.
	if free, total, ok := capacitySampleFor(capacity, capErr); ok {
		_ = r.db.InsertCapacitySample(db.CapacitySample{
			DestID:     dest.ID,
			SampledAt:  time.Now().UTC(),
			FreeBytes:  free,
			TotalBytes: total,
		})
	}

	return "ok", ""
}

// recordBreakerOutcome re-fetches the destination so the breaker sees the
// latest persisted counters, then records a success or failure against the
// circuit breaker. Health-check failures count against the breaker just
// like job failures so a sick destination crosses the threshold from
// either trigger source.
func (r *Runner) recordBreakerOutcome(destID int64, ok bool) {
	if r.breaker == nil {
		return
	}
	freshDest, ferr := r.db.GetStorageDestination(destID)
	if ferr != nil {
		log.Printf("runner: recordBreakerOutcome: failed to re-fetch dest %d: %v", destID, ferr)
		return
	}
	if ok {
		r.breaker.RecordSuccess(r.db, freshDest)
	} else {
		r.breaker.RecordFailure(r.db, freshDest)
	}
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

// capacitySampleFor derives a capacity-trajectory sample from the result of
// a GetCapacity probe. ok is false when no sample should be recorded.
//
// A zero TotalBytes is the adapters' shared "no quota reported" signal: S3
// has no per-bucket quota API, and SFTP/SMB/WebDAV report it when the server
// lacks the statvfs/quota extension. That is a silent skip, not an error —
// the trajectory detector needs a real total to extrapolate against. Probe
// failures are likewise skipped; probeCapacity has already logged them.
//
// free is clamped to total so the free <= total invariant the sample table
// relies on holds even if a backend reports inconsistent block counts.
func capacitySampleFor(capacity storage.Capacity, err error) (free, total int64, ok bool) {
	if err != nil || capacity.TotalBytes <= 0 {
		return 0, 0, false
	}
	free = capacity.FreeBytes
	if free < 0 {
		free = 0
	}
	if free > capacity.TotalBytes {
		free = capacity.TotalBytes
	}
	return free, capacity.TotalBytes, true
}
