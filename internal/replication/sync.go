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

	// onConfigChange flushes the database to flash after sync changes
	// configuration. Nil when there is nothing to flush to.
	onConfigChange func()

	// gates holds a sourceGate for each source with a sync or update in
	// progress. An entry is removed when its last holder lets go.
	gatesMu sync.Mutex
	gates   map[int64]*sourceGate
}

// sourceGate serialises a source's updates with the start of its syncs. A sync
// registers under mu before it reads the source, and an update holds mu while
// it reads and writes the source, so a sync either starts after the update
// and reads its result, or is running and the update knows it.
type sourceGate struct {
	mu      sync.Mutex
	running int // syncs in progress, under mu
	refs    int // syncs and updates holding the gate, under Syncer.gatesMu
}

// acquireGate returns sourceID's gate, creating it if needed, and holds it in
// the map until releaseGate. A gate nobody holds can be removed and
// recreated later: with no holder, a fresh gate is in the same state.
func (s *Syncer) acquireGate(sourceID int64) *sourceGate {
	s.gatesMu.Lock()
	defer s.gatesMu.Unlock()
	if s.gates == nil {
		s.gates = map[int64]*sourceGate{}
	}
	g := s.gates[sourceID]
	if g == nil {
		g = &sourceGate{}
		s.gates[sourceID] = g
	}
	g.refs++
	return g
}

// releaseGate lets go of a gate from acquireGate, removing it once unheld.
func (s *Syncer) releaseGate(sourceID int64, g *sourceGate) {
	s.gatesMu.Lock()
	defer s.gatesMu.Unlock()
	if g.refs--; g.refs == 0 && s.gates[sourceID] == g {
		delete(s.gates, sourceID)
	}
}

// ErrSyncInProgress refuses changing a source's storage destination while a
// sync of that source is running.
var ErrSyncInProgress = errors.New("a sync of this replication source is running; wait for it to finish before changing its storage destination")

// beginSync records a running sync of sourceID and returns its end.
func (s *Syncer) beginSync(sourceID int64) (end func()) {
	g := s.acquireGate(sourceID)
	g.mu.Lock()
	g.running++
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		g.running--
		g.mu.Unlock()
		s.releaseGate(sourceID, g)
	}
}

// UpdateSource runs update, which reads and writes one replication source,
// while no sync of that source can start, and tells it whether one is
// running. Syncs of other sources are not held up. update should read the
// source afresh and refuse a destination change, with ErrSyncInProgress,
// while syncing is true. A source that does not exist gets db.ErrNotFound
// without update being called.
func (s *Syncer) UpdateSource(sourceID int64, update func(syncing bool) error) error {
	if _, err := s.db.GetReplicationSource(sourceID); err != nil {
		return fmt.Errorf("get replication source %d: %w", sourceID, err)
	}
	g := s.acquireGate(sourceID)
	defer s.releaseGate(sourceID, g)
	g.mu.Lock()
	defer g.mu.Unlock()
	return update(g.running > 0)
}

// SetConfigChangeHook registers the daemon's configuration flush. Call it
// before the first sync.
func (s *Syncer) SetConfigChangeHook(fn func()) {
	s.onConfigChange = fn
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
	// A source that does not exist leaves no gate behind.
	if _, err := s.db.GetReplicationSource(sourceID); err != nil {
		return nil, fmt.Errorf("get replication source %d: %w", sourceID, err)
	}
	// Registered before the source is read for the sync, so a destination
	// change either finishes first, and this sync reads the new destination,
	// or is refused.
	defer s.beginSync(sourceID)()
	src, err := s.db.GetReplicationSource(sourceID)
	if err != nil {
		return nil, fmt.Errorf("get replication source %d: %w", sourceID, err)
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
	// This sync's files go to storageDestID, so the source and its jobs must
	// name it: restore and preflight open the job's destination, and a later
	// sync must keep writing to the same place. Jobs created while the source
	// had no destination are bound too, even if the source names one now.
	bound, err := s.db.AdoptReplicationDestination(sourceID, storageDestID)
	if err != nil {
		storage.CloseAdapter(localAdapter)
		errMsg := fmt.Sprintf("record storage destination: %v", err)
		s.updateSyncStatus(sourceID, "failed", errMsg)
		s.logSyncError(sourceID, src.Name, errMsg)
		return nil, fmt.Errorf("record storage destination: %w", err)
	}
	// The binding is configuration: flush it like any other, so losing the
	// working database before the next periodic snapshot cannot lose it.
	if bound && s.onConfigChange != nil {
		s.onConfigChange()
	}
	src.StorageDestID = storageDestID
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

	// Index local restore points by normalised path: syncRestorePoint stores
	// the normalised path, so a peer reporting "job/run/" must still match.
	normPath := func(p string) string {
		if n, err := safepath.NormalizeRestorePointPath(p); err == nil {
			return n
		}
		return p
	}
	localIDs := make(map[string]int64, len(localRPs))
	for _, lrp := range localRPs {
		localIDs[normPath(lrp.StoragePath)] = lrp.ID
	}
	remoteByID := make(map[int64]RemoteRestorePoint, len(remoteRPs))
	for _, rrp := range remoteRPs {
		remoteByID[rrp.ID] = rrp
	}

	var newCount, failed int
	var firstErr error
	var totalBytes int64
	var peerRuns map[int64]RemoteRun
	var history historyState

	// Parents first, so an incremental or differential point can be linked to
	// the local copy of the backup it builds on.
	for _, rrp := range parentsFirst(remoteRPs) {
		path := normPath(rrp.StoragePath)
		if _, ok := localIDs[path]; ok {
			continue // Already synced.
		}

		if peerRuns == nil {
			peerRuns, history = s.peerRuns(client, src.Name, rj)
		}
		// Checked before downloading, so a refused point leaves no files.
		err := dedupUnsupported(rrp)
		var parentID, rpID, bytes int64
		if err == nil {
			parentID, err = localParent(rrp, remoteByID, localIDs, normPath)
		}
		var peerRun *RemoteRun
		if err == nil {
			peerRun, err = peerRunFor(client, rj, rrp.JobRunID, peerRuns, history)
		}
		if err == nil {
			rpID, bytes, err = s.syncRestorePoint(client, src, destID, rj, rrp, localJobID, parentID, peerRun, localAdapter)
		}
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
		localIDs[path] = rpID
		newCount++
		totalBytes += bytes
	}

	if failed > 0 {
		return newCount, totalBytes, fmt.Errorf("%d of %d restore point(s) not recorded: %w", failed, len(remoteRPs), firstErr)
	}
	return newCount, totalBytes, nil
}

// historyState is what a peer's history listing says about run outcomes.
type historyState int

const (
	historyListed      historyState = iota // the newest page was listed
	historyUnsupported                     // the peer has no history endpoint
	historyFailed                          // the listing failed for another reason
)

// peerRuns returns the newest page of the remote job's runs by ID, so each
// copied backup can carry the outcome its run had on the peer, and what the
// listing says about the peer. Only a 404 or 405 means the peer lacks the
// endpoint; any other failure leaves the outcomes unknown.
func (s *Syncer) peerRuns(client *Client, sourceName string, rj RemoteJob) (map[int64]RemoteRun, historyState) {
	runs, err := client.ListJobRuns(rj.ID)
	state := historyListed
	if err != nil {
		logx.Warnf("replication: run history of %q from %q unavailable: %v", rj.Name, sourceName, err)
		state = historyFailed
		if endpointMissing(err) {
			state = historyUnsupported
		}
	}
	byID := make(map[int64]RemoteRun, len(runs))
	for _, r := range runs {
		byID[r.ID] = r
	}
	return byID, state
}

// peerRunFor returns the peer's run behind a restore point. A run outside the
// newest history page is fetched on its own, since its outcome cannot be read
// from the restore point: a run that skipped files is partial with no failed
// items. The result is nil only when the peer has neither a history nor a
// single-run endpoint, leaving the outcome to the restore point metadata.
// When the peer could have described the run but did not, the run is marked
// unconfirmed rather than taken as a success. Any other lookup failure fails
// the restore point, so the next sync tries again.
func peerRunFor(client *Client, rj RemoteJob, runID int64, page map[int64]RemoteRun, history historyState) (*RemoteRun, error) {
	if runID <= 0 {
		return nil, nil
	}
	if run, ok := page[runID]; ok {
		return &run, nil
	}
	run, err := client.GetRun(runID)
	switch {
	case err == nil && run.ID == runID && run.JobID == rj.ID:
		return run, nil
	case err == nil:
		// The peer answered with another run, so its answer says nothing.
		return &RemoteRun{ID: runID, unconfirmed: true}, nil
	case errors.Is(err, errRunNotFound) && history == historyUnsupported:
		return nil, nil
	case errors.Is(err, errRunNotFound):
		return &RemoteRun{ID: runID, unconfirmed: true}, nil
	default:
		return nil, fmt.Errorf("look up the peer's outcome for this backup: %w", err)
	}
}

// errDedupUnsupported marks a restore point refused because it is
// deduplicated: its data lives in the peer's shared chunk store under
// _vault/packs, which replication does not copy, so the replica could not
// restore it.
var errDedupUnsupported = errors.New("replication of deduplicated backups isn't supported yet: their data is in the peer's _vault/packs, which replication doesn't copy")

// dedupUnsupported returns errDedupUnsupported for a restore point whose
// data is in the dedup chunk store: one whose metadata maps an item to a dedup
// manifest ID, or whose row carries a manifest ID, the same signals restore
// uses to choose the dedup reader. A dedup destination alone says nothing:
// VM and ZFS backups are written the classic way there and replicate fine.
func dedupUnsupported(rrp RemoteRestorePoint) error {
	var meta struct {
		ItemManifests map[string]any `json:"item_manifests"`
	}
	_ = json.Unmarshal([]byte(rrp.Metadata), &meta)
	for _, id := range meta.ItemManifests {
		if hexID, ok := id.(string); ok && hexID != "" {
			return errDedupUnsupported
		}
	}
	if len(rrp.ManifestID) > 0 {
		return errDedupUnsupported
	}
	return nil
}

// parentsFirst orders restore points so each one follows the parent it lists,
// keeping the peer's order otherwise. A parent loop cannot recurse forever:
// a point already being visited is not visited again.
func parentsFirst(rps []RemoteRestorePoint) []RemoteRestorePoint {
	index := make(map[int64]int, len(rps))
	for i, rp := range rps {
		if _, dup := index[rp.ID]; !dup {
			index[rp.ID] = i
		}
	}
	seen := make([]bool, len(rps))
	out := make([]RemoteRestorePoint, 0, len(rps))
	var visit func(i int)
	visit = func(i int) {
		if seen[i] {
			return
		}
		seen[i] = true
		if p := rps[i].ParentRestorePointID; p > 0 {
			if pi, ok := index[p]; ok {
				visit(pi)
			}
		}
		out = append(out, rps[i])
	}
	for i := range rps {
		visit(i)
	}
	return out
}

// localParent returns the local restore point that holds the copy of rrp's
// parent, or 0 when rrp stands alone. A point whose parent has no local copy
// cannot be restored, so that is an error rather than a standalone point.
func localParent(rrp RemoteRestorePoint, remoteByID map[int64]RemoteRestorePoint, localIDs map[string]int64, normPath func(string) string) (int64, error) {
	if rrp.ParentRestorePointID <= 0 {
		return 0, nil
	}
	parent, ok := remoteByID[rrp.ParentRestorePointID]
	if !ok || rrp.ParentRestorePointID == rrp.ID {
		return 0, fmt.Errorf("%s backup depends on restore point %d, which the peer does not list, so it could not be restored", rrp.BackupType, rrp.ParentRestorePointID)
	}
	id, ok := localIDs[normPath(parent.StoragePath)]
	if !ok {
		return 0, fmt.Errorf("%s backup depends on %q, which was not copied, so it could not be restored", rrp.BackupType, parent.StoragePath)
	}
	return id, nil
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
		StorageDestID:   destID,
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
	parentID int64,
	peerRun *RemoteRun,
	localAdapter storage.Adapter,
) (rpID, bytes int64, err error) {
	// The peer's storage path becomes a local restore point row that later
	// authorises recursive deletes, so it must have the managed
	// <job>/<run> shape (#514).
	storagePath, err := safepath.NormalizeRestorePointPath(rrp.StoragePath)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid remote storage path: %w", err)
	}

	// Claim first, so the check below and the download cover the same state.
	release, claimed := s.claimPath(destID, storagePath)
	if !claimed {
		return 0, 0, fmt.Errorf("%w: %q overlaps a sync already in progress", errPathRefused, storagePath)
	}
	defer release()

	// The peer chooses this path, so it must not reach data held for any other
	// job, including another job from this same source: the download would
	// overwrite it, and the new row would authorise deleting it. This job's own
	// rows are excluded, so a retried or partial sync of its path still works.
	owners, err := s.db.ListRestorePointOwners(destID, localJobID)
	if err != nil {
		return 0, 0, fmt.Errorf("check restore point path ownership: %w", err)
	}
	for _, o := range owners {
		if storage.PathsOverlap(storagePath, o.StoragePath) {
			return 0, 0, fmt.Errorf("%w: %q overlaps a restore point of local job %d", errPathRefused, storagePath, o.JobID)
		}
	}

	// Recursively list and download all files in the remote storage path.
	totalBytes, err := s.downloadDir(client, rj.StorageDestID, storagePath, localAdapter)
	if err != nil {
		return 0, totalBytes, err
	}

	// Every restore point belongs to a run, so record the peer's backup as a
	// finished run of the local job, then the restore point, in one
	// transaction. If that fails, the downloaded files stay unrecorded and
	// the next sync downloads them again to the same path.
	metadata := s.rewriteMetadata(rrp.Metadata, src.Name)
	run := replicatedRun(localJobID, src.Name, rrp, metadata, totalBytes, peerRun)
	createdAt, skew := peerTime(rrp.CreatedAt, time.Now())
	if skew != "" {
		logx.Warnf("replication: restore point %q from %q: %s", rrp.StoragePath, src.Name, skew)
		run.Log += " " + skew
	}
	rp := db.RestorePoint{
		JobID:                localJobID,
		BackupType:           rrp.BackupType,
		StoragePath:          storagePath,
		Metadata:             metadata,
		SizeBytes:            totalBytes,
		ParentRestorePointID: parentID,
		SourceID:             src.ID,
	}
	_, rpID, err = s.db.CreateReplicatedRestorePoint(run, rp, createdAt, destID)
	if err != nil {
		return 0, totalBytes, fmt.Errorf("record restore point: %w", err)
	}
	return rpID, totalBytes, nil
}

// replicatedRun describes, as a finished local run, the peer backup that
// produced rrp. The outcome is the peer run's own when the peer described it,
// so a backup the peer marked partial (failed items, or files it could not
// read) stays partial here. Otherwise the counts come from the restore point
// metadata, where only failed items are recorded. A run the peer keeps
// history for but could not describe is unconfirmed, so it is recorded as
// partial, not as a success it may not have been.
func replicatedRun(localJobID int64, sourceName string, rrp RemoteRestorePoint, metadata string, sizeBytes int64, peerRun *RemoteRun) db.JobRun {
	run := db.JobRun{
		JobID:      localJobID,
		Status:     "success",
		BackupType: rrp.BackupType,
		RunType:    "backup",
		Log:        fmt.Sprintf("Replicated from %s; the backup ran on that server.", sourceName),
		SizeBytes:  sizeBytes,
	}
	if peerRun != nil && !peerRun.unconfirmed {
		run.ItemsTotal = max(peerRun.ItemsTotal, 0)
		run.ItemsDone = max(peerRun.ItemsDone, 0)
		run.ItemsFailed = max(peerRun.ItemsFailed, 0)
		// A restore point only exists for a run that backed something up, so
		// any outcome other than a clean one is partial here.
		if (peerRun.Status != "completed" && peerRun.Status != "success") || run.ItemsFailed > 0 {
			run.Status = "partial"
		}
		return run
	}
	var meta struct {
		Items       int `json:"items"`
		ItemsFailed int `json:"items_failed"`
	}
	// Older peers may store other shapes here; the counts are then unknown.
	_ = json.Unmarshal([]byte(metadata), &meta)
	run.ItemsDone, run.ItemsFailed = max(meta.Items, 0), max(meta.ItemsFailed, 0)
	run.ItemsTotal = run.ItemsDone + run.ItemsFailed
	if run.ItemsFailed > 0 {
		run.Status = "partial"
	}
	if peerRun != nil && peerRun.unconfirmed {
		run.Status = "partial"
		run.Log += fmt.Sprintf(" Outcome unconfirmed: %s couldn't report how its run %d ended, so this backup is marked partial, as it may have skipped files.", sourceName, peerRun.ID)
	}
	return run
}

// maxPeerClockSkew is how far ahead of this server's clock a peer's backup
// time may be. Retention keeps the newest backups, so a backup dated far in
// the future would outlive every real one; a day covers a peer whose clock or
// time zone is set wrong.
const maxPeerClockSkew = 24 * time.Hour

// peerTime parses the peer's restore point timestamp. It returns the zero time
// (meaning now) when the timestamp is missing or unreadable, or later than
// maxPeerClockSkew past now, with the reason. Old timestamps are kept: they
// are backups made long ago.
func peerTime(s string, now time.Time) (time.Time, string) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, ""
	}
	if t.After(now.Add(maxPeerClockSkew)) {
		return time.Time{}, fmt.Sprintf("The peer dated this backup %s, more than %s ahead of this server's clock, so the time it was copied is used instead.", s, maxPeerClockSkew)
	}
	return t, ""
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
