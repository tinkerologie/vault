package replication

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tinkerologie/vault/internal/credentials"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/logx"
	"github.com/tinkerologie/vault/internal/maintlock"
	"github.com/tinkerologie/vault/internal/safepath"
	"github.com/tinkerologie/vault/internal/storage"
	"github.com/tinkerologie/vault/internal/ws"
)

// errPathRefused marks a remote restore point refused because its path reaches
// data held for someone else. It is failed work, not a skip.
var errPathRefused = errors.New("remote restore point path refused")

// ProgressFunc reports sync progress on a scale of 0.0 to 1.0.
type ProgressFunc func(progress float64, detail string)

// Syncer orchestrates pull-based replication from remote Vault sources.
type Syncer struct {
	db        *db.DB
	hub       *ws.Hub
	serverKey []byte

	// inFlight holds the paths being downloaded per destination. Their rows do
	// not exist until the download ends, so the ownership check cannot see them.
	inFlightMu sync.Mutex
	inFlight   map[int64][]string
}

// claimPath reserves a restore point path on a destination for one download,
// reporting false if it overlaps one already in progress.
func (s *Syncer) claimPath(destID int64, p string) (release func(), ok bool) {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	for _, other := range s.inFlight[destID] {
		if storage.PathsOverlap(p, other) {
			return nil, false
		}
	}
	if s.inFlight == nil {
		s.inFlight = map[int64][]string{}
	}
	s.inFlight[destID] = append(s.inFlight[destID], p)
	return func() {
		s.inFlightMu.Lock()
		defer s.inFlightMu.Unlock()
		s.inFlight[destID] = slices.DeleteFunc(s.inFlight[destID], func(o string) bool { return o == p })
	}, true
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
	JobsSynced int `json:"jobs_synced"`
	JobsFailed int `json:"jobs_failed"`
	// jobsOK counts jobs whose sync succeeded regardless of whether they
	// produced new restore points (an in-sync job is a success, not a
	// failure) — used only for status classification.
	jobsOK           int
	RestorePointsNew int    `json:"restore_points_new"`
	BytesTransferred int64  `json:"bytes_transferred"`
	Error            string `json:"error,omitempty"`
}

// SyncSource performs a sync operation for a replication source.
// It pulls backups from the remote Vault instance.
func (s *Syncer) SyncSource(sourceID int64, progress ProgressFunc) (result *SyncResult, err error) {
	src, err := s.db.GetReplicationSource(sourceID)
	if err != nil {
		return nil, fmt.Errorf("get replication source: %w", err)
	}

	// Sync processes data from another machine on a goroutine of its own, where
	// an uncaught panic would take the whole daemon down.
	defer func() {
		if p := recover(); p != nil {
			log.Printf("replication: sync of source %d panicked: %v\n%s", sourceID, p, debug.Stack())
			msg := fmt.Sprintf("internal error during sync: %v", p)
			s.updateSyncStatus(sourceID, "failed", msg)
			s.logSyncError(sourceID, src.Name, msg)
			result, err = nil, fmt.Errorf("sync source %q: %s", src.Name, msg)
		}
	}()

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

	return s.syncRemoteVault(src, progress)
}

// remoteVaultConfig holds optional settings for remote_vault replication sources.
type remoteVaultConfig struct {
	APIKey string `json:"api_key"`
}

// syncRemoteVault performs a pull-based sync from a remote Vault instance.
func (s *Syncer) syncRemoteVault(src db.ReplicationSource, progress ProgressFunc) (*SyncResult, error) {
	sourceID := src.ID

	// A key this server cannot open must not become an unauthenticated request.
	if credentials.IsLocked(src.Config) {
		errMsg := fmt.Sprintf("source %q: %v", src.Name, credentials.ErrLocked)
		s.updateSyncStatus(sourceID, "failed", errMsg)
		s.logSyncError(sourceID, src.Name, errMsg)
		return nil, fmt.Errorf("source %q: %w", src.Name, credentials.ErrLocked)
	}

	if warning := PlaintextWarning(src.URL); warning != "" {
		log.Printf("Replication source %d: %s", sourceID, warning)
	}

	// Extract optional API key from config.
	var cfg remoteVaultConfig
	if src.Config != "" && src.Config != "{}" {
		if err := json.Unmarshal([]byte(src.Config), &cfg); err != nil {
			errMsg := fmt.Sprintf("invalid config JSON for source %q: %v", src.Name, err)
			s.updateSyncStatus(sourceID, "failed", errMsg)
			s.logSyncError(sourceID, src.Name, errMsg)
			return nil, fmt.Errorf("unmarshal source config: %w", err)
		}
	}

	// Build the remote client.
	var client *Client
	var err error
	if cfg.APIKey != "" {
		client, err = NewClientWithAPIKey(src.URL, cfg.APIKey)
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
	// Release pooled connections / mounts when the sync finishes — scheduled
	// syncs leaked one SFTP pool (or NFS mount) per tick without this (#173).
	defer storage.CloseAdapter(localAdapter)

	// Hold off orphan deletion: downloaded files have no restore point row
	// until each one finishes.
	defer maintlock.HoldDestination(s.db, storageDestID)()

	// Only pull jobs the remote peer created itself. Jobs the remote peer
	// replicated from elsewhere carry a non-zero source_id; re-importing them
	// makes reciprocal peers bounce jobs back and forth, nesting "[A] [B] …"
	// prefixes on every sync (#440).
	remoteJobs = localOriginJobs(remoteJobs)

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

		newRPs, bytes, syncErr := s.syncJob(client, src, storageDestID, rj, localAdapter)
		// Restore points that did sync still count when others failed.
		result.RestorePointsNew += newRPs
		result.BytesTransferred += bytes
		if syncErr != nil {
			logx.Warnf("replication: failed to sync job %q from %q: %v", rj.Name, src.Name, syncErr)
			result.JobsFailed++
			continue
		}
		result.jobsOK++
		if newRPs > 0 {
			result.JobsSynced++
		}
	}

	s.completeSyncStatus(sourceID, src.Name, result, progress)
	return result, nil
}

// localOriginJobs returns the remote jobs that originated on the remote peer,
// dropping jobs that are replicas there (non-zero SourceID).
func localOriginJobs(jobs []RemoteJob) []RemoteJob {
	local := make([]RemoteJob, 0, len(jobs))
	for _, j := range jobs {
		if j.SourceID != 0 {
			continue
		}
		local = append(local, j)
	}
	return local
}

// completeSyncStatus broadcasts sync completion and updates the DB status.
// The persisted status is honest about per-job failures (issue #177):
// "success" only when every job synced, "partial" when some failed, and
// "failed" when nothing could be synced — previously a sync whose every job
// failed still reported success, a false safety signal for a 3-2-1 feature.
func (s *Syncer) completeSyncStatus(sourceID int64, sourceName string, result *SyncResult, progress ProgressFunc) {
	progress(0.98, "Sync complete, updating status...")

	status := "success"
	errMsg := ""
	level := "info"
	switch {
	// Restore points recorded from a job that also had failures are progress,
	// so that sync is partial rather than failed.
	case result.JobsFailed > 0 && result.jobsOK == 0 && result.RestorePointsNew == 0:
		status = "failed"
		errMsg = fmt.Sprintf("all %d job(s) failed to sync — see daemon log", result.JobsFailed)
		level = "error"
	case result.JobsFailed > 0:
		status = "partial"
		errMsg = fmt.Sprintf("%d job(s) failed to sync — see daemon log", result.JobsFailed)
		level = "warning"
	}

	// Persist the status together with the per-item counters BEFORE
	// broadcasting completion: the UI reloads on the completion event, so a
	// broadcast that raced ahead of the write could read the previous row and
	// show stale counters / last_sync_success_at. If the write itself fails,
	// stop here rather than announce a completion the database does not
	// reflect — the source stays in its "running" state and self-corrects on
	// the next sync instead of showing stale completed numbers.
	if err := s.db.UpdateReplicationSyncResult(sourceID, status, errMsg,
		result.JobsSynced, result.JobsFailed, result.RestorePointsNew, result.BytesTransferred); err != nil {
		log.Printf("replication: update sync result for source %d: %v", sourceID, err)
		return
	}

	s.broadcast(map[string]any{
		"type":              "replication_sync_completed",
		"source_id":         sourceID,
		"source_name":       sourceName,
		"status":            status,
		"jobs_synced":       result.JobsSynced,
		"jobs_failed":       result.JobsFailed,
		"restore_points":    result.RestorePointsNew,
		"bytes_transferred": result.BytesTransferred,
	})

	s.db.LogActivity(level, "replication",
		fmt.Sprintf("Replication sync %s: %s — %d jobs, %d failed, %d restore points, %d bytes",
			status, sourceName, result.JobsSynced, result.JobsFailed, result.RestorePointsNew, result.BytesTransferred),
		fmt.Sprintf(`{"source_id":%d,"jobs_synced":%d,"jobs_failed":%d,"restore_points":%d,"bytes":%d}`,
			sourceID, result.JobsSynced, result.JobsFailed, result.RestorePointsNew, result.BytesTransferred))

	progress(1.0, "Done")
}

// syncJob synchronizes a single remote job to local storage.
// Returns the number of new restore points and bytes transferred.
func (s *Syncer) syncJob(
	client *Client,
	src db.ReplicationSource,
	destID int64,
	rj RemoteJob,
	localAdapter storage.Adapter,
) (int, int64, error) {
	// Find or create the local replicated job.
	localJobID, err := s.ensureLocalJob(src, destID, rj)
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
	// Paths are compared in normalised form: syncRestorePoint stores the
	// normalised path, so a peer reporting "job/run/" must still match.
	normPath := func(p string) string {
		if n, err := safepath.NormalizeRestorePointPath(p); err == nil {
			return n
		}
		return p
	}
	localPaths := make(map[string]bool, len(localRPs))
	for _, lrp := range localRPs {
		localPaths[normPath(lrp.StoragePath)] = true
	}

	var newCount, failed int
	var firstErr error
	var totalBytes int64

	for _, rrp := range remoteRPs {
		if localPaths[normPath(rrp.StoragePath)] {
			continue // Already synced.
		}

		bytes, err := s.syncRestorePoint(client, src, destID, rj, rrp, localJobID, localAdapter)
		if err != nil {
			// A restore point that was not recorded is failed work: the
			// replica has no copy it can restore from.
			logx.Warnf("replication: restore point %q from %q not recorded: %v", rrp.StoragePath, src.Name, err)
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// A peer listing the same path twice must not record it twice.
		localPaths[normPath(rrp.StoragePath)] = true
		newCount++
		totalBytes += bytes
	}

	if failed > 0 {
		return newCount, totalBytes, fmt.Errorf("%d of %d restore point(s) not recorded: %w", failed, len(remoteRPs), firstErr)
	}
	return newCount, totalBytes, nil
}

// ensureLocalJob finds or creates a local job record for a replicated remote job.
func (s *Syncer) ensureLocalJob(src db.ReplicationSource, destID int64, rj RemoteJob) (int64, error) {
	// Use a prefixed name to distinguish replicated jobs.
	localName := fmt.Sprintf("[%s] %s", src.Name, rj.Name)

	existing, err := s.db.GetJobByName(localName)
	if err == nil {
		// A matching name proves nothing: only a job this source created may
		// receive its restore points, or a peer could attach them to a local
		// job and have them deleted with it.
		if existing.SourceID != src.ID {
			return 0, fmt.Errorf("local job %q already exists and was not replicated from this source; rename it or the source", localName)
		}
		if existing.StorageDestID != 0 && existing.StorageDestID != destID {
			return 0, fmt.Errorf("local job %q uses storage destination %d but this source now writes to %d", localName, existing.StorageDestID, destID)
		}
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
	destID int64,
	rj RemoteJob,
	rrp RemoteRestorePoint,
	localJobID int64,
	localAdapter storage.Adapter,
) (int64, error) {
	// The peer's storage path becomes a local restore point row that later
	// authorises recursive deletes, so it must have the managed
	// <job>/<run> shape (#514).
	storagePath, err := safepath.NormalizeRestorePointPath(rrp.StoragePath)
	if err != nil {
		return 0, fmt.Errorf("invalid remote storage path: %w", err)
	}

	// Claim first, so the check below and the download cover the same state.
	release, claimed := s.claimPath(destID, storagePath)
	if !claimed {
		return 0, fmt.Errorf("%w: %q overlaps a sync already in progress", errPathRefused, storagePath)
	}
	defer release()

	// The peer chooses this path, so it must not reach data held for any other
	// job, including another job from this same source: the download would
	// overwrite it, and the new row would authorise deleting it. This job's own
	// rows are excluded, so a retried or partial sync of its path still works.
	owners, err := s.db.ListRestorePointOwners(destID, localJobID)
	if err != nil {
		return 0, fmt.Errorf("check restore point path ownership: %w", err)
	}
	for _, o := range owners {
		if storage.PathsOverlap(storagePath, o.StoragePath) {
			return 0, fmt.Errorf("%w: %q overlaps a restore point of local job %d", errPathRefused, storagePath, o.JobID)
		}
	}

	// Recursively list and download all files in the remote storage path.
	totalBytes, err := s.downloadDir(client, rj.StorageDestID, storagePath, localAdapter)
	if err != nil {
		return totalBytes, err
	}

	// Every restore point belongs to a run, so record the peer's backup as a
	// finished run of the local job, then the restore point, in one
	// transaction. If that fails, the downloaded files stay unrecorded and
	// the next sync downloads them again to the same path.
	metadata := s.rewriteMetadata(rrp.Metadata, src.Name)
	run := replicatedRun(localJobID, src.Name, rrp, metadata, totalBytes)
	rp := db.RestorePoint{
		JobID:                localJobID,
		BackupType:           rrp.BackupType,
		StoragePath:          storagePath,
		Metadata:             metadata,
		SizeBytes:            totalBytes,
		ParentRestorePointID: 0,
		SourceID:             src.ID,
	}
	if _, _, err := s.db.CreateReplicatedRestorePoint(run, rp, peerTime(rrp.CreatedAt)); err != nil {
		return totalBytes, fmt.Errorf("record restore point: %w", err)
	}

	return totalBytes, nil
}

// replicatedRun describes, as a finished local run, the peer backup that
// produced rrp. Its outcome and item counts come from the restore point
// metadata the peer recorded: a backup with failed items is partial.
func replicatedRun(localJobID int64, sourceName string, rrp RemoteRestorePoint, metadata string, sizeBytes int64) db.JobRun {
	var meta struct {
		Items       int `json:"items"`
		ItemsFailed int `json:"items_failed"`
	}
	// Older peers may store other shapes here; the counts are then unknown.
	_ = json.Unmarshal([]byte(metadata), &meta)
	items, failed := max(meta.Items, 0), max(meta.ItemsFailed, 0)
	status := "success"
	if failed > 0 {
		status = "partial"
	}
	return db.JobRun{
		JobID:       localJobID,
		Status:      status,
		BackupType:  rrp.BackupType,
		RunType:     "backup",
		Log:         fmt.Sprintf("Replicated from %s; the backup ran on that server.", sourceName),
		ItemsTotal:  items + failed,
		ItemsDone:   items,
		ItemsFailed: failed,
		SizeBytes:   sizeBytes,
	}
}

// peerTime parses the peer's restore point timestamp, returning the zero time
// (meaning now) when it is missing or unreadable.
func peerTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// rewriteMetadata adds replication origin info to the metadata. The result
// is built via encoding/json (never string concatenation): a source name
// containing quotes/backslashes, or originals like "{ }", previously produced
// invalid JSON — silently stripping checksums and item_manifests from every
// replicated restore point for consumers like GC and restore (issue #176).
// Whenever the original does not parse as a JSON object, the origin marker
// alone is returned so downstream parsers always receive valid JSON.
func (s *Syncer) rewriteMetadata(original, sourceName string) string {
	meta := map[string]any{}
	if trimmed := strings.TrimSpace(original); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &meta); err != nil {
			log.Printf("replication: metadata from source %q is not valid JSON (%v); keeping origin marker only", sourceName, err)
			meta = map[string]any{}
		}
		// A JSON null decodes without error and leaves the map nil.
		if meta == nil {
			meta = map[string]any{}
		}
	}
	meta["replicated_from"] = sourceName
	out, err := json.Marshal(meta)
	if err != nil {
		return `{"replicated_from":"unknown"}`
	}
	return string(out)
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

// matchMode records how a restore point's listing relates to its path.
type matchMode int

const (
	matchUndecided matchMode = iota
	matchLiteral             // entries sit literally below the restore point
	matchSanitised           // an S3-backed peer reports sanitised key segments
)

// childRemainder reports whether a peer-listed child lies strictly below the
// listed directory and, if so, returns the child's segments below it.
//
// An S3-backed peer reports keys with each segment sanitised (a job "My Job"
// is stored as "My_Job"), while the restore point path keeps the original
// name. The first entry of a restore point decides the mode, and every later
// entry must use the same one: a peer whose entries are literal descendants
// (every non-S3 destination) can never also slip in a sanitised sibling such
// as "My_Job/run" for "My Job/run". The child must be a clean, relative,
// non-escaping path in either mode; sanitising is used only for comparison.
func childRemainder(parent, child string, mode *matchMode) ([]string, bool) {
	if !safepath.IsStrictDescendant(".", child) {
		return nil, false // absolute, root alias or escaping
	}
	ps := strings.Split(path.Clean(parent), "/")
	cs := strings.Split(path.Clean(child), "/")
	if len(cs) <= len(ps) {
		return nil, false
	}
	literal, sanitised := true, true
	for i := range ps {
		literal = literal && cs[i] == ps[i]
		sanitised = sanitised && cs[i] == storage.SanitizeKeySegment(ps[i])
	}
	switch *mode {
	case matchUndecided:
		switch {
		case literal:
			*mode = matchLiteral
		case sanitised:
			*mode = matchSanitised
		default:
			return nil, false
		}
	case matchLiteral:
		if !literal {
			return nil, false
		}
	case matchSanitised:
		if !sanitised {
			return nil, false
		}
	}
	return cs[len(ps):], true
}

// remoteFiles is the part of *Client that downloadDir needs. It is an
// interface so tests can return listings and streams a real peer would not.
type remoteFiles interface {
	ListStorageFiles(storageID int64, prefix string) ([]StorageFile, error)
	DownloadFile(storageID int64, filePath string) (io.ReadCloser, error)
}

// maxDownloadDepth bounds how far below a restore point downloadDir will
// descend. Vault run folders are a few levels deep; a peer that reports a
// deeper tree is broken or hostile.
const maxDownloadDepth = 32

// downloadDir recursively lists and downloads all files from a remote storage
// path. Every listed entry must sit strictly below the directory that was
// listed, so a peer cannot direct writes outside the restore point (#514).
// Files are written locally under dirPath itself, so the local copy matches
// the restore point path even when the peer reports sanitised S3 keys. Each
// directory is visited once, and recursion depth is bounded, so a
// self-referencing listing cannot loop forever.
func (s *Syncer) downloadDir(client remoteFiles, storageID int64, dirPath string, localAdapter storage.Adapter) (int64, error) {
	mode := matchUndecided
	return s.downloadDirConfined(client, storageID, dirPath, path.Clean(dirPath), localAdapter, map[string]bool{}, &mode, 0)
}

// downloadDirConfined is downloadDir's recursive step: it lists remoteDir,
// checks each entry with childRemainder, downloads files to the matching
// path under localDir, and recurses into directories it has not visited.
func (s *Syncer) downloadDirConfined(client remoteFiles, storageID int64, remoteDir, localDir string, localAdapter storage.Adapter, visited map[string]bool, mode *matchMode, depth int) (int64, error) {
	if depth > maxDownloadDepth {
		return 0, fmt.Errorf("remote listing under %q is deeper than %d levels", remoteDir, maxDownloadDepth)
	}
	clean := path.Clean(remoteDir)
	if visited[clean] {
		return 0, fmt.Errorf("remote listing revisits %q", remoteDir)
	}
	visited[clean] = true

	files, err := client.ListStorageFiles(storageID, remoteDir)
	if err != nil {
		return 0, fmt.Errorf("list remote files at %q: %w", remoteDir, err)
	}

	var totalBytes int64
	for _, f := range files {
		rel, ok := childRemainder(clean, f.Path, mode)
		if !ok {
			return totalBytes, fmt.Errorf("remote entry %q is outside %q", f.Path, remoteDir)
		}
		remote := path.Clean(f.Path)
		local := path.Join(append([]string{localDir}, rel...)...)
		if f.IsDir {
			// Recurse into subdirectories.
			bytes, err := s.downloadDirConfined(client, storageID, remote, local, localAdapter, visited, mode, depth+1)
			if err != nil {
				return totalBytes, err
			}
			totalBytes += bytes
			continue
		}

		rc, err := client.DownloadFile(storageID, remote)
		if err != nil {
			return totalBytes, fmt.Errorf("download %q: %w", remote, err)
		}

		if err := localAdapter.Write(local, rc); err != nil {
			_ = rc.Close()
			return totalBytes, fmt.Errorf("write %q: %w", local, err)
		}
		if err := rc.Close(); err != nil {
			return totalBytes, fmt.Errorf("closing download stream for %q: %w", remote, err)
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
