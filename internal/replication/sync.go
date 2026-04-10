package replication

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// ProgressFunc reports sync progress on a scale of 0.0 to 1.0.
type ProgressFunc func(progress float64, detail string)

// Syncer orchestrates pull-based replication from remote Vault sources.
type Syncer struct {
	db        *db.DB
	hub       *ws.Hub
	serverKey []byte
}

// NewSyncer creates a new replication syncer.
func NewSyncer(database *db.DB, hub *ws.Hub) *Syncer {
	return &Syncer{db: database, hub: hub}
}

// updateSyncStatus is a best-effort status update; errors are logged.
func (s *Syncer) updateSyncStatus(sourceID int64, status, errorMsg string) {
	if err := s.db.UpdateReplicationSyncStatus(sourceID, status, errorMsg); err != nil {
		log.Printf("replication: failed to update sync status for source %d: %v", sourceID, err)
	}
}

// SyncResult contains the outcome of a sync operation.
type SyncResult struct {
	JobsSynced       int    `json:"jobs_synced"`
	RestorePointsNew int    `json:"restore_points_new"`
	BytesTransferred int64  `json:"bytes_transferred"`
	Error            string `json:"error,omitempty"`
}

// SyncSource performs a sync operation for a replication source.
// For remote_vault targets, it pulls backups from the remote Vault instance.
// For gdrive/onedrive targets, it pushes local backups to cloud storage.
func (s *Syncer) SyncSource(sourceID int64, progress ProgressFunc) (*SyncResult, error) {
	src, err := s.db.GetReplicationSource(sourceID)
	if err != nil {
		return nil, fmt.Errorf("get replication source: %w", err)
	}

	// Guard nil progress callback.
	if progress == nil {
		progress = func(float64, string) {}
	}

	// Mark sync as running and broadcast start event.
	s.updateSyncStatus(sourceID, "running", "")
	s.broadcast(map[string]any{
		"type":        "replication_sync_started",
		"source_id":   sourceID,
		"source_name": src.Name,
	})
	s.db.LogActivity("info", "replication",
		fmt.Sprintf("Replication sync started: %s", src.Name),
		fmt.Sprintf(`{"source_id":%d}`, sourceID))

	switch src.Type {
	case "gdrive", "onedrive":
		return s.syncCloudPush(src, progress)
	default:
		return s.syncRemoteVault(src, progress)
	}
}

// syncRemoteVault performs a pull-based sync from a remote Vault instance.
func (s *Syncer) syncRemoteVault(src db.ReplicationSource, progress ProgressFunc) (*SyncResult, error) {
	sourceID := src.ID

	// Build the remote client.
	var client *Client
	var err error
	if src.APIKey != "" {
		client, err = NewClientWithAPIKey(src.URL, src.APIKey)
	} else {
		client, err = NewClient(src.URL)
	}
	if err != nil {
		errMsg := fmt.Sprintf("normalize source url %q: %v", src.Name, err)
		s.updateSyncStatus(sourceID, "failed", err.Error())
		s.logSyncError(sourceID, src.Name, errMsg)
		return nil, fmt.Errorf("normalize source url %q: %w", src.Name, err)
	}

	// Verify connectivity first.
	if _, err := client.TestConnection(); err != nil {
		errMsg := fmt.Sprintf("connect to source %q: %v", src.Name, err)
		s.updateSyncStatus(sourceID, "failed", err.Error())
		s.logSyncError(sourceID, src.Name, errMsg)
		return nil, fmt.Errorf("connect to source %q: %w", src.Name, err)
	}

	progress(0.05, "Connected to source, fetching jobs...")

	// Fetch remote jobs.
	remoteJobs, err := client.ListJobs()
	if err != nil {
		errMsg := fmt.Sprintf("list remote jobs: %v", err)
		s.updateSyncStatus(sourceID, "failed", err.Error())
		s.logSyncError(sourceID, src.Name, errMsg)
		return nil, fmt.Errorf("list remote jobs: %w", err)
	}

	// Resolve local storage adapter. If storage_dest_id is 0, auto-select
	// the first available storage destination.
	storageDestID := src.StorageDestID
	if storageDestID == 0 {
		dests, listErr := s.db.ListStorageDestinations()
		if listErr != nil || len(dests) == 0 {
			errMsg := "no local storage destination available for remote vault sync"
			s.updateSyncStatus(sourceID, "failed", errMsg)
			s.logSyncError(sourceID, src.Name, errMsg)
			return nil, fmt.Errorf("%s", errMsg)
		}
		storageDestID = dests[0].ID
	}

	dest, err := s.db.GetStorageDestination(storageDestID)
	if err != nil {
		errMsg := fmt.Sprintf("get storage destination: %v", err)
		s.updateSyncStatus(sourceID, "failed", err.Error())
		s.logSyncError(sourceID, src.Name, errMsg)
		return nil, fmt.Errorf("get storage destination: %w", err)
	}
	localAdapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		errMsg := fmt.Sprintf("open local storage: %v", err)
		s.updateSyncStatus(sourceID, "failed", err.Error())
		s.logSyncError(sourceID, src.Name, errMsg)
		return nil, fmt.Errorf("open local storage: %w", err)
	}

	result := &SyncResult{}
	totalJobs := len(remoteJobs)
	if totalJobs == 0 {
		progress(1.0, "No jobs found on source")
		s.updateSyncStatus(sourceID, "success", "")
		return result, nil
	}

	// Process each remote job.
	for i, rj := range remoteJobs {
		jobProgress := 0.1 + (0.85 * float64(i) / float64(totalJobs))
		detail := fmt.Sprintf("Syncing job %d/%d: %s", i+1, totalJobs, rj.Name)
		progress(jobProgress, detail)

		s.broadcast(map[string]any{
			"type":        "replication_sync_progress",
			"source_id":   sourceID,
			"source_name": src.Name,
			"progress":    jobProgress,
			"detail":      detail,
		})

		newRPs, bytes, syncErr := s.syncJob(client, src, rj, localAdapter)
		if syncErr != nil {
			log.Printf("replication: failed to sync job %q from %q: %v", rj.Name, src.Name, syncErr)
			continue
		}
		if newRPs > 0 {
			result.JobsSynced++
		}
		result.RestorePointsNew += newRPs
		result.BytesTransferred += bytes
	}

	s.completeSyncStatus(sourceID, src.Name, result, progress)
	return result, nil
}

// syncCloudPush pushes local backups to a cloud storage target (GDrive/OneDrive).
func (s *Syncer) syncCloudPush(src db.ReplicationSource, progress ProgressFunc) (*SyncResult, error) {
	sourceID := src.ID

	// Build the cloud storage adapter from the target config.
	cloudAdapter, err := storage.NewAdapter(src.Type, src.Config)
	if err != nil {
		errMsg := fmt.Sprintf("open cloud storage: %v", err)
		s.updateSyncStatus(sourceID, "failed", err.Error())
		s.logSyncError(sourceID, src.Name, errMsg)
		return nil, fmt.Errorf("open cloud storage: %w", err)
	}

	progress(0.05, "Connected to cloud storage, listing local jobs...")

	// List all local non-replicated jobs (source_id == 0 means local).
	localJobs, err := s.db.ListJobs()
	if err != nil {
		errMsg := fmt.Sprintf("list local jobs: %v", err)
		s.updateSyncStatus(sourceID, "failed", err.Error())
		s.logSyncError(sourceID, src.Name, errMsg)
		return nil, fmt.Errorf("list local jobs: %w", err)
	}

	// Filter to only local (non-replicated) jobs.
	var jobs []db.Job
	for _, j := range localJobs {
		if j.SourceID == 0 && j.Enabled {
			jobs = append(jobs, j)
		}
	}

	result := &SyncResult{}
	totalJobs := len(jobs)
	if totalJobs == 0 {
		progress(1.0, "No local jobs to replicate")
		s.updateSyncStatus(sourceID, "success", "")
		return result, nil
	}

	for i, job := range jobs {
		jobProgress := 0.1 + (0.85 * float64(i) / float64(totalJobs))
		detail := fmt.Sprintf("Pushing job %d/%d: %s", i+1, totalJobs, job.Name)
		progress(jobProgress, detail)

		s.broadcast(map[string]any{
			"type":        "replication_sync_progress",
			"source_id":   sourceID,
			"source_name": src.Name,
			"progress":    jobProgress,
			"detail":      detail,
		})

		newRPs, bytes, pushErr := s.pushJobToCloud(job, src, cloudAdapter)
		if pushErr != nil {
			log.Printf("replication: failed to push job %q to %q: %v", job.Name, src.Name, pushErr)
			continue
		}
		if newRPs > 0 {
			result.JobsSynced++
		}
		result.RestorePointsNew += newRPs
		result.BytesTransferred += bytes
	}

	s.completeSyncStatus(sourceID, src.Name, result, progress)
	return result, nil
}

// pushJobToCloud uploads restore points from a local job to a cloud storage adapter.
func (s *Syncer) pushJobToCloud(
	job db.Job,
	src db.ReplicationSource,
	cloudAdapter storage.Adapter,
) (int, int64, error) {
	// Get local restore points for this job.
	localRPs, err := s.db.ListRestorePoints(job.ID)
	if err != nil {
		return 0, 0, fmt.Errorf("list restore points: %w", err)
	}

	if len(localRPs) == 0 {
		return 0, 0, nil
	}

	// Get the local storage adapter to read backup files.
	localDest, err := s.db.GetStorageDestination(job.StorageDestID)
	if err != nil {
		return 0, 0, fmt.Errorf("get local storage destination: %w", err)
	}
	localAdapter, err := storage.NewAdapter(localDest.Type, localDest.Config)
	if err != nil {
		return 0, 0, fmt.Errorf("open local storage: %w", err)
	}

	var newCount int
	var totalBytes int64

	for _, rp := range localRPs {
		// Check if already uploaded by trying to stat the storage path on cloud.
		if _, statErr := cloudAdapter.Stat(rp.StoragePath); statErr == nil {
			continue // Already replicated.
		}

		// Upload files from local storage to cloud storage.
		bytes, uploadErr := s.uploadDir(localAdapter, cloudAdapter, rp.StoragePath)
		if uploadErr != nil {
			log.Printf("replication: skipping restore point %q: %v", rp.StoragePath, uploadErr)
			continue
		}
		newCount++
		totalBytes += bytes
	}

	return newCount, totalBytes, nil
}

// uploadDir recursively copies files from a local storage adapter to a cloud adapter.
func (s *Syncer) uploadDir(localAdapter, cloudAdapter storage.Adapter, dirPath string) (int64, error) {
	files, err := localAdapter.List(dirPath)
	if err != nil {
		return 0, fmt.Errorf("list local files at %q: %w", dirPath, err)
	}

	var totalBytes int64
	for _, f := range files {
		if f.IsDir {
			bytes, recurseErr := s.uploadDir(localAdapter, cloudAdapter, f.Path)
			if recurseErr != nil {
				return totalBytes, recurseErr
			}
			totalBytes += bytes
			continue
		}

		rc, readErr := localAdapter.Read(f.Path)
		if readErr != nil {
			return totalBytes, fmt.Errorf("read %q: %w", f.Path, readErr)
		}

		if writeErr := cloudAdapter.Write(f.Path, rc); writeErr != nil {
			_ = rc.Close()
			return totalBytes, fmt.Errorf("write %q to cloud: %w", f.Path, writeErr)
		}
		if closeErr := rc.Close(); closeErr != nil {
			return totalBytes, fmt.Errorf("closing read stream for %q: %w", f.Path, closeErr)
		}
		totalBytes += f.Size
	}
	return totalBytes, nil
}

// completeSyncStatus broadcasts sync completion and updates the DB status.
func (s *Syncer) completeSyncStatus(sourceID int64, sourceName string, result *SyncResult, progress ProgressFunc) {
	progress(0.98, "Sync complete, updating status...")

	s.broadcast(map[string]any{
		"type":              "replication_sync_completed",
		"source_id":         sourceID,
		"source_name":       sourceName,
		"jobs_synced":       result.JobsSynced,
		"restore_points":    result.RestorePointsNew,
		"bytes_transferred": result.BytesTransferred,
	})

	s.updateSyncStatus(sourceID, "success", "")
	s.db.LogActivity("info", "replication",
		fmt.Sprintf("Replication sync completed: %s — %d jobs, %d restore points, %d bytes",
			sourceName, result.JobsSynced, result.RestorePointsNew, result.BytesTransferred),
		fmt.Sprintf(`{"source_id":%d,"jobs_synced":%d,"restore_points":%d,"bytes":%d}`,
			sourceID, result.JobsSynced, result.RestorePointsNew, result.BytesTransferred))

	progress(1.0, "Done")
}

// syncJob synchronizes a single remote job to local storage.
// Returns the number of new restore points and bytes transferred.
func (s *Syncer) syncJob(
	client *Client,
	src db.ReplicationSource,
	rj RemoteJob,
	localAdapter storage.Adapter,
) (int, int64, error) {
	// Find or create the local replicated job.
	localJobID, err := s.ensureLocalJob(src, rj)
	if err != nil {
		return 0, 0, fmt.Errorf("ensure local job: %w", err)
	}

	// Fetch remote restore points.
	remoteRPs, err := client.ListRestorePoints(rj.ID)
	if err != nil {
		return 0, 0, fmt.Errorf("list remote restore points: %w", err)
	}

	// Get local restore points for comparison.
	localRPs, err := s.db.ListRestorePoints(localJobID)
	if err != nil {
		return 0, 0, fmt.Errorf("list local restore points: %w", err)
	}

	// Build set of local storage paths for dedup.
	localPaths := make(map[string]bool, len(localRPs))
	for _, lrp := range localRPs {
		localPaths[lrp.StoragePath] = true
	}

	var newCount int
	var totalBytes int64

	for _, rrp := range remoteRPs {
		if localPaths[rrp.StoragePath] {
			continue // Already synced.
		}

		bytes, err := s.syncRestorePoint(client, src, rj, rrp, localJobID, localAdapter)
		if err != nil {
			log.Printf("replication: skipping restore point %q: %v", rrp.StoragePath, err)
			continue
		}
		newCount++
		totalBytes += bytes
	}

	return newCount, totalBytes, nil
}

// ensureLocalJob finds or creates a local job record for a replicated remote job.
func (s *Syncer) ensureLocalJob(src db.ReplicationSource, rj RemoteJob) (int64, error) {
	// Use a prefixed name to distinguish replicated jobs.
	localName := fmt.Sprintf("[%s] %s", src.Name, rj.Name)

	existing, err := s.db.GetJobByName(localName)
	if err == nil {
		return existing.ID, nil
	}
	if err != db.ErrNotFound {
		return 0, err
	}

	// Create a new replicated job. Disabled so it won't run backups locally.
	job := db.Job{
		Name:            localName,
		Description:     fmt.Sprintf("Replicated from %s", src.Name),
		Enabled:         false,
		BackupTypeChain: rj.BackupTypeChain,
		Compression:     rj.Compression,
		Encryption:      rj.Encryption,
		ContainerMode:   rj.ContainerMode,
		VMMode:          rj.VMMode,
		RetentionCount:  rj.RetentionCount,
		RetentionDays:   rj.RetentionDays,
		NotifyOn:        "failure",
		StorageDestID:   src.StorageDestID,
		SourceID:        src.ID,
	}
	return s.db.CreateReplicatedJob(job)
}

// syncRestorePoint downloads backup files for a single restore point.
func (s *Syncer) syncRestorePoint(
	client *Client,
	src db.ReplicationSource,
	rj RemoteJob,
	rrp RemoteRestorePoint,
	localJobID int64,
	localAdapter storage.Adapter,
) (int64, error) {
	// Recursively list and download all files in the remote storage path.
	totalBytes, err := s.downloadDir(client, rj.StorageDestID, rrp.StoragePath, localAdapter)
	if err != nil {
		return totalBytes, err
	}

	// Create a local restore point record. We use a stub job_run_id of 0
	// since this wasn't run locally.
	rp := db.RestorePoint{
		JobRunID:             0,
		JobID:                localJobID,
		BackupType:           rrp.BackupType,
		StoragePath:          rrp.StoragePath,
		Metadata:             s.rewriteMetadata(rrp.Metadata, src.Name),
		SizeBytes:            totalBytes,
		ParentRestorePointID: 0,
		SourceID:             src.ID,
	}
	if _, err := s.db.CreateRestorePoint(rp); err != nil {
		return totalBytes, fmt.Errorf("create restore point: %w", err)
	}

	return totalBytes, nil
}

// rewriteMetadata adds replication origin info to the metadata.
func (s *Syncer) rewriteMetadata(original, sourceName string) string {
	if original == "" || original == "{}" {
		return fmt.Sprintf(`{"replicated_from":"%s"}`, sourceName)
	}
	// Insert replicated_from into existing JSON.
	trimmed := strings.TrimSpace(original)
	if strings.HasPrefix(trimmed, "{") && len(trimmed) > 2 {
		return fmt.Sprintf(`{"replicated_from":"%s",%s`, sourceName, trimmed[1:])
	}
	return original
}

// SetHub sets the WebSocket hub for broadcasting sync events.
func (s *Syncer) SetHub(hub *ws.Hub) {
	s.hub = hub
}

// SetServerKey sets the AES-256 key used to unseal storage credentials.
func (s *Syncer) SetServerKey(key []byte) {
	s.serverKey = key
}

// broadcast sends a JSON message to all connected WebSocket clients.
func (s *Syncer) broadcast(data map[string]any) {
	if s.hub == nil {
		return
	}
	msg, err := json.Marshal(data)
	if err != nil {
		log.Printf("replication: failed to marshal broadcast: %v", err)
		return
	}
	s.hub.Broadcast(msg)
}

// downloadDir recursively lists and downloads all files from a remote storage path.
func (s *Syncer) downloadDir(client *Client, storageID int64, dirPath string, localAdapter storage.Adapter) (int64, error) {
	files, err := client.ListStorageFiles(storageID, dirPath)
	if err != nil {
		return 0, fmt.Errorf("list remote files at %q: %w", dirPath, err)
	}

	var totalBytes int64
	for _, f := range files {
		if f.IsDir {
			// Recurse into subdirectories.
			bytes, err := s.downloadDir(client, storageID, f.Path, localAdapter)
			if err != nil {
				return totalBytes, err
			}
			totalBytes += bytes
			continue
		}

		rc, err := client.DownloadFile(storageID, f.Path)
		if err != nil {
			return totalBytes, fmt.Errorf("download %q: %w", f.Path, err)
		}

		if err := localAdapter.Write(f.Path, rc); err != nil {
			_ = rc.Close()
			return totalBytes, fmt.Errorf("write %q: %w", f.Path, err)
		}
		if err := rc.Close(); err != nil {
			return totalBytes, fmt.Errorf("closing download stream for %q: %w", f.Path, err)
		}
		totalBytes += f.Size
	}
	return totalBytes, nil
}

// logSyncError logs a replication sync failure to the activity log.
func (s *Syncer) logSyncError(sourceID int64, sourceName, errMsg string) {
	s.broadcast(map[string]any{
		"type":        "replication_sync_failed",
		"source_id":   sourceID,
		"source_name": sourceName,
		"error":       errMsg,
	})
	s.db.LogActivity("error", "replication",
		fmt.Sprintf("Replication sync failed: %s — %s", sourceName, errMsg),
		fmt.Sprintf(`{"source_id":%d}`, sourceID))
}
