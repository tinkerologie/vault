package runner

import (
	"context"
	"log"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// capacityProbeTimeout caps how long a single GetCapacity call may take
// in the daily sweep. WebDAV PROPFIND on a healthy Nextcloud server is
// sub-second; SFTP statvfs is sub-second; S3 list-sum on a 100k-object
// bucket finishes in single-digit seconds. 60s is comfortable headroom
// without holding up the whole sweep on a single pathological host.
const capacityProbeTimeout = 60 * time.Second

// probeCapacity runs GetCapacity on the given adapter and persists the
// result. Failures are logged and stored in capacity_error but do NOT
// change the health-check verdict — capacity is informational only.
// Returns the persisted Capacity (or zero on failure) and any error.
//
// The probe is fire-and-forget from the daily-sweep caller's
// perspective; we don't return an error that could mistakenly be
// interpreted as a health-check failure.
func (r *Runner) probeCapacity(parent context.Context, dest db.StorageDestination, adapter storage.Adapter) (storage.Capacity, error) {
	ctx, cancel := context.WithTimeout(parent, capacityProbeTimeout)
	defer cancel()
	cap, err := adapter.GetCapacity(ctx)
	if err != nil {
		log.Printf("runner: capacity probe failed for %q (id=%d): %v", dest.Name, dest.ID, err)
		_ = r.db.UpdateStorageDestinationCapacity(dest.ID, db.CapacityRecord{}, err.Error())
		return storage.Capacity{}, err
	}
	if persistErr := r.db.UpdateStorageDestinationCapacity(dest.ID, capacityToRecord(cap), ""); persistErr != nil {
		log.Printf("runner: persist capacity for %q (id=%d): %v", dest.Name, dest.ID, persistErr)
		return cap, persistErr
	}
	r.broadcastStorageCapacity(dest.ID, cap)
	return cap, nil
}

// BroadcastStorageCapacity exposes the storage_capacity_updated WS
// broadcast to API handlers (the manual /capacity-check endpoint
// added in Task 9). Internal callers can use the lowercase variant.
func (r *Runner) BroadcastStorageCapacity(storageID int64, cap storage.Capacity) {
	r.broadcastStorageCapacity(storageID, cap)
}

func (r *Runner) broadcastStorageCapacity(storageID int64, cap storage.Capacity) {
	r.broadcast(map[string]any{
		"type":       "storage_capacity_updated",
		"storage_id": storageID,
		"capacity":   cap,
	})
}

// capacityToRecord maps storage.Capacity to db.CapacityRecord. Both
// structs share the same five fields; the drift guard test in
// internal/db/capacity_drift_test.go enforces they stay aligned.
//
// The mapping exists because internal/db deliberately does NOT import
// internal/storage (which would re-introduce a build cycle if any
// adapter ever fails compilation). See the CapacityRecord comment for
// the full history.
func capacityToRecord(c storage.Capacity) db.CapacityRecord {
	return db.CapacityRecord{
		TotalBytes: c.TotalBytes,
		UsedBytes:  c.UsedBytes,
		FreeBytes:  c.FreeBytes,
		ProbedAt:   c.ProbedAt,
		Source:     c.Source,
	}
}
