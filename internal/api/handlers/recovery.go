package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// RecoveryHandler serves the disaster recovery plan.
type RecoveryHandler struct {
	db      *db.DB
	version string
}

// NewRecoveryHandler creates a RecoveryHandler.
func NewRecoveryHandler(database *db.DB, version string) *RecoveryHandler {
	return &RecoveryHandler{db: database, version: version}
}

// GetPlan compiles a disaster recovery plan from existing data.
//
//	GET /api/v1/recovery/plan
func (h *RecoveryHandler) GetPlan(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.db.ListJobs()
	if err != nil {
		respondInternalError(w, err)
		return
	}

	storageDests, err := h.db.ListStorageDestinations()
	if err != nil {
		respondInternalError(w, err)
		return
	}

	// Build storage name lookup.
	storageNames := make(map[int64]string)
	for _, s := range storageDests {
		storageNames[s.ID] = s.Name
	}

	// Collect all items, latest restore points, and warnings.
	type stepItem struct {
		Name            string     `json:"name"`
		LastBackup      *time.Time `json:"last_backup"`
		StorageName     string     `json:"storage_name"`
		SizeBytes       int64      `json:"size_bytes"`
		HasRestorePoint bool       `json:"has_restore_point"`
	}

	type step struct {
		Step        int        `json:"step"`
		Title       string     `json:"title"`
		Description string     `json:"description"`
		Status      string     `json:"status"` // "ready", "warning", "not_available"
		Items       []stepItem `json:"items,omitempty"`
		TotalSize   int64      `json:"total_size,omitempty"`
	}

	var warnings []string
	var steps []step
	var totalProtected int

	containerItems := []stepItem{}
	vmItems := []stepItem{}
	folderItems := []stepItem{}

	for _, job := range jobs {
		if !job.Enabled {
			continue
		}
		items, err := h.db.GetJobItems(job.ID)
		if err != nil {
			continue
		}
		rps, err := h.db.ListRestorePoints(job.ID)
		if err != nil {
			rps = nil
		}

		var latestRP *db.RestorePoint
		if len(rps) > 0 {
			latestRP = &rps[0]
		}

		for _, item := range items {
			totalProtected++
			si := stepItem{
				Name:            item.ItemName,
				StorageName:     storageNames[job.StorageDestID],
				HasRestorePoint: latestRP != nil,
			}
			if latestRP != nil {
				si.LastBackup = &latestRP.CreatedAt
				if len(items) > 0 {
					si.SizeBytes = latestRP.SizeBytes / int64(len(items)) // approximate per-item
				}
			}

			// Warn if last backup is older than 7 days.
			if latestRP == nil {
				warnings = append(warnings, item.ItemName+" has no restore points")
			} else if time.Since(latestRP.CreatedAt) > 7*24*time.Hour {
				warnings = append(warnings, item.ItemName+" last backed up "+latestRP.CreatedAt.Format("Jan 2")+" (>7 days ago)")
			}

			switch item.ItemType {
			case "container":
				containerItems = append(containerItems, si)
			case "vm":
				vmItems = append(vmItems, si)
			case "folder":
				folderItems = append(folderItems, si)
			}
		}
	}

	stepNum := 1

	// Step 1: Install Vault
	steps = append(steps, step{
		Step:        stepNum,
		Title:       "Install Vault Plugin",
		Description: "Install the Vault plugin from Community Applications and restore the database from your backup storage.",
		Status:      "ready",
	})
	stepNum++

	// Step 2: Restore Containers
	if len(containerItems) > 0 {
		var totalSize int64
		for _, c := range containerItems {
			totalSize += c.SizeBytes
		}
		status := "ready"
		if !containerItems[0].HasRestorePoint {
			status = "warning"
		}
		steps = append(steps, step{
			Step:        stepNum,
			Title:       fmt.Sprintf("Restore Containers (%d)", len(containerItems)),
			Description: "Restore all Docker container appdata from backup.",
			Status:      status,
			Items:       containerItems,
			TotalSize:   totalSize,
		})
		stepNum++
	}

	// Step 3: Restore VMs
	if len(vmItems) > 0 {
		var totalSize int64
		for _, v := range vmItems {
			totalSize += v.SizeBytes
		}
		status := "ready"
		if !vmItems[0].HasRestorePoint {
			status = "warning"
		}
		steps = append(steps, step{
			Step:        stepNum,
			Title:       fmt.Sprintf("Restore Virtual Machines (%d)", len(vmItems)),
			Description: "Restore VM disk images and configurations from backup.",
			Status:      status,
			Items:       vmItems,
			TotalSize:   totalSize,
		})
		stepNum++
	}

	// Step 4: Restore Folders
	if len(folderItems) > 0 {
		var totalSize int64
		for _, f := range folderItems {
			totalSize += f.SizeBytes
		}
		steps = append(steps, step{
			Step:        stepNum,
			Title:       fmt.Sprintf("Restore Folders (%d)", len(folderItems)),
			Description: "Restore custom folder backups (Flash Drive, shares, etc.).",
			Status:      "ready",
			Items:       folderItems,
			TotalSize:   totalSize,
		})
	}

	result := map[string]any{
		"server_info": map[string]any{
			"vault_version":           h.version,
			"total_protected_items":   totalProtected,
			"total_unprotected_items": 0,
		},
		"steps":        steps,
		"warnings":     warnings,
		"last_updated": time.Now().UTC(),
	}
	respondJSON(w, http.StatusOK, result)
}
