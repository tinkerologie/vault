package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/engine"
	jobintake "github.com/ruaan-deysel/vault/internal/jobs"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// subManifestFunc resolves a nested sub-manifest by chunk ID. Container
// manifests reference volume file data only indirectly, so browsing one
// requires a second fetch per volume.
type subManifestFunc func(dedup.ID) (dedup.Manifest, error)

// dedupManifestToTarIndex synthesizes a TarIndex-shaped response from a
// dedup manifest so the restore wizard's file picker can render dedup
// restore points using the same UI as classic tar-backed restore points.
// The "archive" field is set to the item name (there is no single archive
// in dedup mode — content lives in /_vault/packs/) so the picker still has
// a label to show.
//
// Folder and plugin manifests map one key to one file, so they pass through
// unchanged. Container manifests do not: their Files map holds only synthetic
// keys (see the contract above engine.BackupChunked), and the real per-file
// entries live one level down in a FolderHandler sub-manifest per volume.
// Emitting the top-level keys verbatim surfaced engine metadata as files,
// listed excluded volumes as restorable, and rendered the skipped-volume
// sentinel as "-1 B" (issue #333). So container entries are resolved:
// synthetic metadata is dropped, skipped volumes are dropped, and each
// backed-up volume is expanded from its sub-manifest with paths prefixed by
// the volume's container-internal destination.
//
// The synthetic-key rules apply ONLY to container items. A folder or plugin
// manifest maps one key to one real path, so a user file legitimately named
// "__inspect" or living under a "__vol__…" directory must pass through
// untouched rather than be dropped or mis-expanded as a volume.
//
// getSub may be nil, in which case volumes cannot be expanded and are omitted
// rather than reported with pointer-entry sizes.
func dedupManifestToTarIndex(itemName, itemType string, m dedup.Manifest, getSub subManifestFunc) engine.TarIndex {
	isContainer := itemType == "container"
	idx := engine.TarIndex{
		Version: 1,
		Archive: itemName,
		Files:   make([]engine.TarIndexEntry, 0, len(m.Files)),
	}
	for p, e := range m.Files {
		if isContainer && engine.IsSyntheticContainerKey(p) {
			continue
		}
		if dest, isVol := engine.ContainerVolumeDest(p); isVol && isContainer {
			if engine.IsSkippedVolumeEntry(e) || getSub == nil {
				continue
			}
			idx.Files = append(idx.Files, expandVolumeEntry(dest, e, getSub)...)
			continue
		}
		idx.Files = append(idx.Files, engine.TarIndexEntry{
			Path:    p,
			Size:    e.Size,
			Mode:    fmt.Sprintf("%04o", e.Mode&0o7777),
			ModTime: e.ModTime,
			IsDir:   e.IsDir,
		})
	}
	sort.Slice(idx.Files, func(i, j int) bool { return idx.Files[i].Path < idx.Files[j].Path })
	return idx
}

// expandVolumeEntry resolves one __vol__<dest> entry into the real file list
// held by its sub-manifest, with every path rewritten to the container-internal
// absolute path the user recognises (e.g. "/config/settings.yml").
//
// A volume whose sub-manifest cannot be read yields no entries: showing nothing
// is better than showing a pointer entry whose size describes the manifest
// rather than the volume.
func expandVolumeEntry(dest string, e dedup.ManifestEntry, getSub subManifestFunc) []engine.TarIndexEntry {
	sub, err := getSub(e.Chunks[0])
	if err != nil {
		log.Printf("api: restore contents: volume %s sub-manifest unreadable, omitting from picker: %v", dest, err)
		return nil
	}
	out := make([]engine.TarIndexEntry, 0, len(sub.Files))
	for p, se := range sub.Files {
		out = append(out, engine.TarIndexEntry{
			Path:    path.Join(dest, p),
			Size:    se.Size,
			Mode:    fmt.Sprintf("%04o", se.Mode&0o7777),
			ModTime: se.ModTime,
			IsDir:   se.IsDir,
		})
	}
	return out
}

// ScheduleReloader is called after job CRUD to reload the cron scheduler.
type ScheduleReloader = func() error

// NextRunResolver returns the next scheduled run time for a job.
type NextRunResolver = func(jobID int64) (string, bool)

type JobHandler struct {
	db     *db.DB
	runner *runner.Runner
	// intake owns every Job write. It is shared with the MCP adapter so both
	// enforce the same policy by construction rather than by convention.
	intake         *jobintake.Intake
	schedReload    ScheduleReloader
	nextRun        NextRunResolver
	onConfigChange ConfigChangeHook
}

func NewJobHandler(database *db.DB, r *runner.Runner, reload ScheduleReloader, intake *jobintake.Intake) *JobHandler {
	return &JobHandler{db: database, runner: r, schedReload: reload, intake: intake}
}

// respondIntakeError maps a Job Intake error onto an HTTP status. Only
// jobintake.ValidationError is the caller's fault; everything else is a server
// fault and must not be reported as bad input.
func respondIntakeError(w http.ResponseWriter, err error, entity string) {
	switch {
	case errors.Is(err, jobintake.ErrJobNotFound):
		respondError(w, http.StatusNotFound, "not found")
	case errors.Is(err, jobintake.ErrRestoreInProgress):
		respondError(w, http.StatusConflict, err.Error())
	default:
		if ve, ok := jobintake.IsValidation(err); ok {
			respondError(w, http.StatusBadRequest, ve.Message)
			return
		}
		respondWriteError(w, err, entity)
	}
}

// SetNextRunResolver sets the function used to look up the next scheduled run.
func (h *JobHandler) SetNextRunResolver(fn NextRunResolver) {
	h.nextRun = fn
}

// SetConfigChangeHook registers a function called after job mutations to flush
// the database to USB flash.
func (h *JobHandler) SetConfigChangeHook(fn ConfigChangeHook) {
	h.onConfigChange = fn
}

// notifyConfigChange calls the config change hook if set.
func (h *JobHandler) notifyConfigChange() {
	if h.onConfigChange != nil {
		h.onConfigChange()
	}
}

// broadcastConfigChange sends a `config_changed` WebSocket event so that
// dashboards / 3-2-1 compliance widgets / recovery plans re-fetch derived
// state without requiring a full page reload. The `entity` field tells
// the client what changed (e.g., "job", "storage", "replication").
func (h *JobHandler) broadcastConfigChange(entity string) {
	if h.runner == nil {
		return
	}
	h.runner.Broadcast(map[string]any{
		"type":   "config_changed",
		"entity": entity,
	})
}

// reloadScheduler triggers a scheduler reload, logging any errors.
func (h *JobHandler) reloadScheduler() {
	if h.schedReload != nil {
		if err := h.schedReload(); err != nil {
			log.Printf("Warning: scheduler reload failed: %v", err)
		}
	}
}

func (h *JobHandler) List(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.db.ListJobs()
	if err != nil {
		respondInternalError(w, err)
		return
	}
	if r.URL.Query().Get("details") != "true" {
		respondJSON(w, http.StatusOK, jobs)
		return
	}
	items, err := h.db.ListJobItems(r.Context())
	if err != nil {
		respondInternalError(w, err)
		return
	}
	baselines, err := h.db.ListJobBaselines(r.Context())
	if err != nil {
		respondInternalError(w, err)
		return
	}
	itemsByJob := make(map[int64][]db.JobItem, len(jobs))
	for _, item := range items {
		itemsByJob[item.JobID] = append(itemsByJob[item.JobID], item)
	}
	baselineByJob := make(map[int64]db.JobBaseline, len(baselines))
	for _, baseline := range baselines {
		baselineByJob[baseline.JobID] = baseline
	}
	type detailedJob struct {
		db.Job
		Items    []db.JobItem   `json:"items"`
		Baseline db.JobBaseline `json:"baseline"`
	}
	result := make([]detailedJob, 0, len(jobs))
	for _, job := range jobs {
		jobItems := itemsByJob[job.ID]
		if jobItems == nil {
			jobItems = []db.JobItem{}
		}
		baseline := baselineByJob[job.ID]
		baseline.JobID = job.ID
		result = append(result, detailedJob{Job: job, Items: jobItems, Baseline: baseline})
	}
	respondJSON(w, http.StatusOK, result)
}

func (h *JobHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		db.Job
		Items []db.JobItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// Everything past decoding — validation, normalisation, persistence, the
	// scheduler reload, the USB flush and the WebSocket broadcast — belongs to
	// Job Intake. This handler's job is HTTP: decode, call, encode.
	job, items, err := h.intake.Create(req.Job, req.Items)
	if err != nil {
		respondIntakeError(w, err, "job")
		return
	}
	// Keep the Job fields at the top level for backwards compatibility
	// (front-end reads result.id) and add the persisted items, with their
	// server-assigned IDs, alongside.
	respondJSON(w, http.StatusCreated, struct {
		db.Job
		Items []db.JobItem `json:"items"`
	}{job, items})
}

func (h *JobHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	job, err := h.db.GetJob(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	items, _ := h.db.GetJobItems(id)
	respondJSON(w, http.StatusOK, map[string]any{"job": job, "items": items})
}

func (h *JobHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		db.Job
		Items []db.JobItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// A PUT that omits items leaves the persisted items in place; Job Intake
	// applies that rule (and checks source/destination overlap against the
	// persisted items in that case).
	job, items, err := h.intake.Update(id, req.Job, req.Items)
	if err != nil {
		respondIntakeError(w, err, "job")
		return
	}
	respondJSON(w, http.StatusOK, struct {
		db.Job
		Items []db.JobItem `json:"items"`
	}{job, items})
}

func (h *JobHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	// The asynchronous storage sweep (issue #111) is why this is 202 rather
	// than 204: a large backup on a slow remote takes far longer than an HTTP
	// client will wait, which used to surface as a spurious "daemon
	// unavailable" even though the server kept working. Job Intake decides
	// whether a sweep started; this handler only picks the status code.
	outcome, err := h.intake.Delete(id, jobintake.DeleteOptions{
		DeleteFiles: r.URL.Query().Get("deleteFiles") == "true",
	})
	if err != nil {
		respondIntakeError(w, err, "job")
		return
	}
	if outcome.CleanupStarted {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *JobHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	// Reject unknown jobs with 404 rather than returning an empty list, which
	// masked typos and was inconsistent with the sibling restore-points route.
	if _, err := h.db.GetJob(id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	const maxLimit = 1000
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		parsed, err := strconv.Atoi(l)
		if err != nil || parsed < 1 {
			respondError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if parsed > maxLimit {
			parsed = maxLimit
		}
		limit = parsed
	}
	runs, err := h.db.GetJobRuns(id, limit)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	// Always return an array shape — never JSON null. Front-ends call
	// .length and .map on the response and would throw on null.
	if runs == nil {
		runs = []db.JobRun{}
	}
	respondJSON(w, http.StatusOK, runs)
}

func (h *JobHandler) GetRestorePoints(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	job, err := h.db.GetJob(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	rps, err := h.db.ListRestorePoints(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	annotated := runner.AnnotateRestorePoints(job, rps)
	if annotated == nil {
		annotated = []runner.AnnotatedRestorePoint{}
	}
	respondJSON(w, http.StatusOK, annotated)
}

// RestorePointContents returns the list of files inside an archive at a
// restore point, sourced from the engine-side tar index sidecar.
//
//	GET /api/v1/jobs/{id}/restore-points/{rpid}/contents?item=<itemName>&file=<archiveName>
//
// `item` selects the per-item subdirectory under the restore point's storage
// path (e.g. "Flash Drive"). `file` is the archive basename — when omitted
// the handler scans for any "*.index.json[.age]" sidecar and uses the first
// it finds (so callers can omit the file parameter for single-archive items
// like folders / plugins).
//
// On encrypted jobs the sidecar is uploaded as `<archive>.index.json.age`
// and is decrypted on the fly using the runner's configured passphrase.
// Returns 404 when no index sidecar exists (e.g. backups produced before
// this feature was added); the restore wizard falls back to whole-archive
// extraction in that case.
func (h *JobHandler) RestorePointContents(w http.ResponseWriter, r *http.Request) {
	jobID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rpID, ok := parseID(w, r, "rpid")
	if !ok {
		return
	}

	rp, err := h.db.GetRestorePoint(rpID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "restore point not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	if rp.JobID != jobID {
		respondError(w, http.StatusNotFound, "restore point not found")
		return
	}

	itemName := strings.TrimSpace(r.URL.Query().Get("item"))
	if itemName == "" {
		respondError(w, http.StatusBadRequest, "item query parameter is required")
		return
	}
	// Guard against browsing an item that this restore point never captured
	// (e.g. an item added to the job after these backups ran). Without this
	// the storage lookup below fails with a generic 500.
	if members, known := rp.BackedUpItems(); known {
		if _, ok := members[itemName]; !ok {
			respondError(w, http.StatusNotFound, "this item is not in the selected restore point")
			return
		}
	}
	archiveName := strings.TrimSpace(r.URL.Query().Get("file"))
	// Container manifests need synthetic-key resolution; folder and plugin
	// manifests must pass through untouched (see dedupManifestToTarIndex).
	itemType := h.itemType(jobID, itemName)

	job, err := h.db.GetJob(jobID)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	dest, err := h.db.GetStorageDestination(job.StorageDestID)
	if err != nil {
		respondInternalError(w, err)
		return
	}

	// Dedup points carry a complete manifest and restore directly from it —
	// no chain merge (and no resurrection, issue #231). Check before the
	// chain branch so dedup increments browse as exactly their manifest.
	if mID, isDedup := runner.ResolveItemManifestID(rp, itemName); isDedup {
		// One repo open serves the item manifest and every volume
		// sub-manifest it points at.
		getManifest, closeSession, err := h.runner.OpenDedupManifests(dest)
		if err != nil {
			respondInternalError(w, err)
			return
		}
		defer closeSession()
		manifest, err := getManifest(mID)
		if err != nil {
			respondInternalError(w, err)
			return
		}
		respondJSON(w, http.StatusOK, dedupManifestToTarIndex(itemName, itemType, manifest, subManifestFunc(getManifest)))
		return
	}

	// Restoring a classic incremental/differential point replays the whole
	// chain, so the file picker must show the merged chain contents — this
	// point's own index only lists the files that changed in this increment
	// (often none, which rendered as an empty picker).
	if rp.BackupType == "incremental" || rp.BackupType == "differential" {
		chain, chainErr := h.runner.BuildRestoreChain(rp)
		if chainErr != nil || len(chain) < 2 {
			// Fail closed: a broken/incomplete chain must not silently browse
			// as just this increment's delta.
			log.Printf("api: restore point %d: chain walk failed (len=%d): %v", rp.ID, len(chain), chainErr) // #nosec G706 //nolint:gosec // IDs are validated int64s, err is from the internal DB layer
			respondError(w, http.StatusNotFound, "restore chain is incomplete; file browsing is unavailable for this restore point")
			return
		}
		h.respondMergedChainContents(w, chain, dest, itemName, itemType, archiveName)
		return
	}

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	defer storage.CloseAdapter(adapter)

	itemPrefix := path.Join(rp.StoragePath, itemName)

	// Resolve which sidecar to read. When `file` is supplied, build both
	// candidates explicitly (with and without .age). When `file` is
	// omitted, list the per-item directory and pick the first index file.
	candidates, err := resolveIndexCandidates(adapter, itemPrefix, archiveName)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	if len(candidates) == 0 {
		respondError(w, http.StatusNotFound, "no tar index sidecar found for this item")
		return
	}

	var (
		indexReader io.ReadCloser
		sidecarPath string
	)
	for _, candidate := range candidates {
		rc, err := adapter.Read(candidate)
		if err != nil {
			continue
		}
		indexReader = rc
		sidecarPath = candidate
		break
	}
	if indexReader == nil {
		respondError(w, http.StatusNotFound, "tar index sidecar not readable at any candidate path")
		return
	}
	defer indexReader.Close()

	var src io.Reader = indexReader
	if strings.HasSuffix(sidecarPath, ".age") {
		pass := h.runner.ResolvePassphrase()
		if pass == "" {
			respondError(w, http.StatusFailedDependency, "index is encrypted but no passphrase is configured")
			return
		}
		dec, err := crypto.DecryptReader(pass, indexReader)
		if err != nil {
			respondInternalError(w, err)
			return
		}
		defer dec.Close()
		src = dec
	}

	idx, err := engine.ReadTarIndex(src)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, idx)
}

// errIndexEncryptedNoPassphrase marks a chain-step index that cannot be read
// because it is age-encrypted and no passphrase is configured.
var errIndexEncryptedNoPassphrase = errors.New("index is encrypted but no passphrase is configured")

// respondMergedChainContents merges the per-item indexes of every restore
// point in the chain (oldest full first) and responds with the union, later
// steps overriding earlier ones by path — mirroring what a chain restore
// actually produces on disk. A step is skipped only when its metadata
// conclusively shows the item was not captured by that step; a missing or
// unreadable index otherwise fails the request (fail closed) so the wizard
// falls back to whole-item restore instead of presenting a partial file list.
func (h *JobHandler) respondMergedChainContents(w http.ResponseWriter, chain []db.RestorePoint, dest db.StorageDestination, itemName, itemType, archiveName string) {
	var adapter storage.Adapter
	getAdapter := func() (storage.Adapter, error) {
		if adapter == nil {
			a, err := storage.NewAdapter(dest.Type, dest.Config)
			if err != nil {
				return nil, err
			}
			adapter = a
		}
		return adapter, nil
	}
	defer func() {
		if adapter != nil {
			storage.CloseAdapter(adapter)
		}
	}()

	merged := make(map[string]engine.TarIndexEntry)
	for i, step := range chain {
		// Skip steps that conclusively did not capture this item (e.g. the
		// item was added to the job after this step's backup ran).
		if members, known := step.BackedUpItems(); known {
			if _, ok := members[itemName]; !ok {
				continue
			}
		}
		// The `file` query targets the selected (newest) point's archive;
		// parent steps auto-discover their own sidecar.
		stepArchive := ""
		if i == len(chain)-1 {
			stepArchive = archiveName
		}
		idx, err := h.itemIndexForPoint(getAdapter, step, dest, itemName, itemType, stepArchive)
		if err != nil {
			if errors.Is(err, errIndexEncryptedNoPassphrase) {
				respondError(w, http.StatusFailedDependency, errIndexEncryptedNoPassphrase.Error())
				return
			}
			log.Printf("api: restore point %d: chain step %d index unavailable: %v", chain[len(chain)-1].ID, step.ID, err) // #nosec G706 //nolint:gosec // IDs are validated int64s, err is from an admin-configured adapter
			respondError(w, http.StatusNotFound, fmt.Sprintf(
				"index for chain step %d is missing or unreadable; file browsing is unavailable for this restore point", step.ID))
			return
		}
		for _, f := range idx.Files {
			merged[f.Path] = f
		}
	}

	// When the newest point has an authoritative listing, drop merged
	// entries absent from it — the chain-restore prune pass removes those
	// files, so the picker must not offer them (issue #231). Without a
	// listing (pre-listing backups) the union stays, matching the restore.
	newest := chain[len(chain)-1]
	if listing, ok := h.runner.ReadItemSidecar(dest, newest, itemName, engine.ListingSuffix); ok {
		keep := make(map[string]struct{}, len(listing.Files))
		for _, f := range listing.Files {
			keep[f.Path] = struct{}{}
		}
		for p := range merged {
			if _, ok := keep[p]; !ok {
				delete(merged, p)
			}
		}
	}

	files := make([]engine.TarIndexEntry, 0, len(merged))
	for _, f := range merged {
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	respondJSON(w, http.StatusOK, engine.TarIndex{Version: 1, Archive: itemName, Files: files})
}

// itemType resolves a job item's type ("container", "folder", "plugin", …).
//
// Returns "" when the item is no longer configured on the job — a restore
// point can outlive its item. Callers treat that as "not a container", which
// is the safe default: it leaves manifest keys untouched rather than dropping
// paths that only look synthetic.
func (h *JobHandler) itemType(jobID int64, itemName string) string {
	items, err := h.db.GetJobItems(jobID)
	if err != nil {
		// The item name is caller-supplied, so it is deliberately left out of the
		// log line; the job ID identifies the failure well enough.
		log.Printf("api: restore contents: cannot resolve item types for job %d: %v", jobID, err) // #nosec G706 //nolint:gosec // jobID is a validated int64, err is from the internal DB layer
		return ""
	}
	for _, it := range items {
		if it.ItemName == itemName {
			return it.ItemType
		}
	}
	return ""
}

// itemIndexForPoint fetches one restore point's index for an item: the dedup
// manifest for chunked points, otherwise the tar-index sidecar (decrypting
// .age sidecars with the configured passphrase).
func (h *JobHandler) itemIndexForPoint(getAdapter func() (storage.Adapter, error), rp db.RestorePoint, dest db.StorageDestination, itemName, itemType, archiveName string) (engine.TarIndex, error) {
	if mID, isDedup := runner.ResolveItemManifestID(rp, itemName); isDedup {
		getManifest, closeSession, err := h.runner.OpenDedupManifests(dest)
		if err != nil {
			return engine.TarIndex{}, err
		}
		defer closeSession()
		manifest, err := getManifest(mID)
		if err != nil {
			return engine.TarIndex{}, err
		}
		return dedupManifestToTarIndex(itemName, itemType, manifest, subManifestFunc(getManifest)), nil
	}

	adapter, err := getAdapter()
	if err != nil {
		return engine.TarIndex{}, err
	}
	itemPrefix := path.Join(rp.StoragePath, itemName)
	candidates, err := resolveIndexCandidates(adapter, itemPrefix, archiveName)
	if err != nil {
		return engine.TarIndex{}, err
	}
	var (
		indexReader io.ReadCloser
		sidecarPath string
	)
	for _, candidate := range candidates {
		rc, readErr := adapter.Read(candidate)
		if readErr != nil {
			continue
		}
		indexReader = rc
		sidecarPath = candidate
		break
	}
	if indexReader == nil {
		return engine.TarIndex{}, fmt.Errorf("no readable tar index sidecar under %s", itemPrefix)
	}
	defer indexReader.Close()

	var src io.Reader = indexReader
	if strings.HasSuffix(sidecarPath, ".age") {
		pass := h.runner.ResolvePassphrase()
		if pass == "" {
			return engine.TarIndex{}, errIndexEncryptedNoPassphrase
		}
		dec, decErr := crypto.DecryptReader(pass, indexReader)
		if decErr != nil {
			return engine.TarIndex{}, decErr
		}
		defer dec.Close()
		src = dec
	}
	return engine.ReadTarIndex(src)
}

// resolveIndexCandidates returns the list of storage paths to probe for the
// requested archive's tar index sidecar. When `archiveName` is supplied the
// list is just the two encryption variants of `<itemPrefix>/<archive>.index.json`.
// Otherwise the item directory is listed and any `*.index.json[.age]` files
// found are returned in their natural order (alphabetical from List()).
func resolveIndexCandidates(adapter storage.Adapter, itemPrefix, archiveName string) ([]string, error) {
	if archiveName != "" {
		// Strip any user-supplied .age suffix so we always probe the plain
		// path first then the encrypted variant.
		base := strings.TrimSuffix(archiveName, ".age")
		stem := path.Join(itemPrefix, base+engine.IndexSuffix)
		return []string{stem, stem + ".age"}, nil
	}
	entries, err := adapter.List(itemPrefix)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		base := path.Base(e.Path)
		if strings.HasSuffix(base, engine.IndexSuffix) || strings.HasSuffix(base, engine.IndexSuffix+".age") {
			out = append(out, e.Path)
		}
	}
	return out, nil
}

// RetentionPreview returns the impact of a hypothetical Long-Term Retention
// (LTR) policy against the job's current restore points without actually
// applying it. Used by the Jobs wizard to show "would keep X of Y" as the
// user tunes the keep_* fields.
//
//	GET /api/v1/jobs/{id}/retention-preview?keep_latest=3&keep_daily=7&keep_weekly=4&keep_monthly=12&keep_yearly=5
//
//	Returns: {
//	  "total_restore_points": N,
//	  "kept_directly":        []int64,  // IDs the policy would keep outright
//	  "kept_with_ancestors":  []int64,  // IDs kept once chain protection is layered on
//	  "would_delete":         []int64,  // IDs that would be pruned
//	}
func (h *JobHandler) RetentionPreview(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	if _, err := h.db.GetJob(id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "job not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	q := r.URL.Query()
	parseN := func(key string) int {
		if s := q.Get(key); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n >= 0 {
				return n
			}
		}
		return 0
	}
	policy := runner.LTRPolicy{
		KeepLatest:  parseN("keep_latest"),
		KeepDaily:   parseN("keep_daily"),
		KeepWeekly:  parseN("keep_weekly"),
		KeepMonthly: parseN("keep_monthly"),
		KeepYearly:  parseN("keep_yearly"),
	}
	rps, err := h.db.ListRestorePoints(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	sorted := sortRestorePointsNewestFirst(rps)

	if !policy.IsActive() {
		respondJSON(w, http.StatusOK, map[string]any{
			"total_restore_points": len(rps),
			"kept_directly":        []int64{},
			"kept_with_ancestors":  []int64{},
			"would_delete":         []int64{},
			"policy_active":        false,
		})
		return
	}

	direct := runner.LTRDirectlyKept(sorted, policy, time.Local)
	protected := runner.LTRProtectedRestorePointIDs(sorted, policy, time.Local)
	directIDs := make([]int64, 0, len(direct))
	for k := range direct {
		directIDs = append(directIDs, k)
	}
	protectedIDs := make([]int64, 0, len(protected))
	for k := range protected {
		protectedIDs = append(protectedIDs, k)
	}
	deleteIDs := make([]int64, 0, len(rps))
	for _, rp := range rps {
		if _, ok := protected[rp.ID]; !ok {
			deleteIDs = append(deleteIDs, rp.ID)
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"total_restore_points": len(rps),
		"kept_directly":        directIDs,
		"kept_with_ancestors":  protectedIDs,
		"would_delete":         deleteIDs,
		"policy_active":        true,
	})
}

// sortRestorePointsNewestFirst is a local copy of the chain_health helper
// so the API handler can pre-sort without importing internal sorting.
func sortRestorePointsNewestFirst(points []db.RestorePoint) []db.RestorePoint {
	out := make([]db.RestorePoint, len(points))
	copy(out, points)
	for i := 0; i < len(out)-1; i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].CreatedAt.After(out[i].CreatedAt) ||
				(out[j].CreatedAt.Equal(out[i].CreatedAt) && out[j].ID > out[i].ID) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// VerifyRestorePoint kicks off a verification of a restore point.
//
//	POST /api/v1/jobs/{id}/restore-points/{rpid}/verify  {"mode": "quick"|"deep"}
//
// Returns 202 + {"verify_run_id": N} so the caller can poll
// GET /verify-runs/{vrid} or subscribe to WebSocket verify_progress events.
func (h *JobHandler) VerifyRestorePoint(w http.ResponseWriter, r *http.Request) {
	jobID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rpID, ok := parseID(w, r, "rpid")
	if !ok {
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Mode == "" {
		req.Mode = string(runner.VerifyModeQuick)
	}
	mode := runner.VerifyMode(strings.ToLower(req.Mode))
	if !mode.IsValid() {
		respondError(w, http.StatusBadRequest, "mode must be 'quick' or 'deep'")
		return
	}

	rp, err := h.db.GetRestorePoint(rpID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "restore point not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	if rp.JobID != jobID {
		respondError(w, http.StatusNotFound, "restore point not found")
		return
	}

	id, err := h.runner.RunVerify(rp, mode)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusAccepted, map[string]any{
		"verify_run_id":    id,
		"restore_point_id": rp.ID,
		"mode":             string(mode),
	})
}

// GetVerifyRun returns the current state of a verify run.
//
//	GET /api/v1/jobs/{id}/verify-runs/{vrid}
func (h *JobHandler) GetVerifyRun(w http.ResponseWriter, r *http.Request) {
	_, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	vrID, ok := parseID(w, r, "vrid")
	if !ok {
		return
	}
	vr, err := h.db.GetVerifyRun(vrID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "verify run not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, vr)
}

// ListRestorePointVerifyRuns returns recent verify runs for a restore
// point. Used by the UI to render the per-restore-point verify-history
// badge ("Verified Deep · 2h ago ✓").
//
//	GET /api/v1/jobs/{id}/restore-points/{rpid}/verify-runs?limit=10
func (h *JobHandler) ListRestorePointVerifyRuns(w http.ResponseWriter, r *http.Request) {
	jobID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rpID, ok := parseID(w, r, "rpid")
	if !ok {
		return
	}
	rp, err := h.db.GetRestorePoint(rpID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "restore point not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	if rp.JobID != jobID {
		respondError(w, http.StatusNotFound, "restore point not found")
		return
	}
	limit := 10
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, parseErr := strconv.Atoi(s); parseErr == nil && n > 0 {
			limit = n
		}
	}
	rows, err := h.db.ListVerifyRunsForRestorePoint(rpID, limit)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, rows)
}

// DeleteRestorePoint deletes a single restore point and its storage files.
//
//	DELETE /api/v1/jobs/{id}/restore-points/{rpid}
func (h *JobHandler) DeleteRestorePoint(w http.ResponseWriter, r *http.Request) {
	jobID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rpID, ok := parseID(w, r, "rpid")
	if !ok {
		return
	}

	rp, err := h.db.GetRestorePoint(rpID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "restore point not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	if rp.JobID != jobID {
		respondError(w, http.StatusNotFound, "restore point not found")
		return
	}

	// Capture the storage destination before deleting the DB row, then sweep
	// the files asynchronously (issue #111) — deleting a large restore point on
	// a slow remote can outlast the HTTP client and surface as a spurious
	// "daemon unavailable" even though the delete succeeds.
	var (
		cleanupDest db.StorageDestination
		doCleanup   bool
	)
	if rp.StoragePath != "" {
		job, jErr := h.db.GetJob(jobID)
		switch {
		case jErr == nil:
			dest, dErr := h.db.GetStorageDestination(job.StorageDestID)
			switch {
			case dErr == nil:
				cleanupDest, doCleanup = dest, true
			case errors.Is(dErr, db.ErrNotFound):
				// Orphaned job (issue #113): no destination to clean.
				log.Printf("handlers: restore point %d's job %d has no storage destination; deleting record only", rpID, jobID) // #nosec G706 //nolint:gosec // jobID and rpID are int64 from URL params
			default:
				respondInternalError(w, dErr)
				return
			}
		case errors.Is(jErr, db.ErrNotFound):
			// Job gone but the restore point row lingers; delete the record only.
		default:
			respondInternalError(w, jErr)
			return
		}
	}

	if err := h.db.DeleteRestorePoint(rpID); err != nil {
		respondInternalError(w, err)
		return
	}

	h.db.LogActivity("info", "system", fmt.Sprintf("Restore point #%d deleted", rpID),
		fmt.Sprintf(`{"restore_point_id":%d,"job_id":%d}`, rpID, jobID))

	if doCleanup {
		h.runner.CleanupRestorePointStorageAsync(jobID, rpID, cleanupDest, rp.StoragePath)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunNow triggers an immediate backup run for a job.
//
//	POST /api/v1/jobs/{id}/run
func (h *JobHandler) RunNow(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	_, err := h.db.GetJob(id)
	if err != nil {
		respondError(w, http.StatusNotFound, "job not found")
		return
	}

	// Run the backup asynchronously. Manual "run now" invocations bypass
	// automatic retry scheduling — the user can re-press the button.
	go h.runner.RunJobManual(id)

	respondJSON(w, http.StatusAccepted, map[string]any{
		"message": "backup started",
		"job_id":  id,
	})
}

// Cancel requests cancellation of a currently running job.
//
//	POST /api/v1/jobs/{id}/cancel
func (h *JobHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	if err := h.runner.CancelJob(id); err != nil {
		respondError(w, http.StatusConflict, err.Error())
		return
	}
	respondJSON(w, http.StatusAccepted, map[string]any{
		"message": "cancellation requested",
		"job_id":  id,
	})
}

// RunnerStatus returns the current state of the backup/restore runner.
//
//	GET /api/v1/runner/status
func (h *JobHandler) RunnerStatus(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.runner.Status())
}

// RestorePointPreflight runs cheap pre-restore validation (storage reachable,
// backup present, decryptable, free space) so the UI can show a go/no-go
// checklist before a restore is started.
//
//	POST /api/v1/jobs/{id}/restore-points/{rpid}/preflight
func (h *JobHandler) RestorePointPreflight(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rpID, ok := parseID(w, r, "rpid")
	if !ok {
		return
	}
	var req struct {
		Passphrase  string `json:"passphrase"`
		Destination string `json:"destination"`
	}
	// Body is optional (unencrypted, original-location restore needs nothing).
	_ = json.NewDecoder(r.Body).Decode(&req)

	job, err := h.db.GetJob(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "job not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	rp, err := h.db.GetRestorePoint(rpID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "restore point not found")
			return
		}
		respondInternalError(w, err)
		return
	}
	if rp.JobID != id {
		respondError(w, http.StatusNotFound, "restore point not found")
		return
	}
	respondJSON(w, http.StatusOK, h.runner.PreflightRestore(job, rp, req.Passphrase, req.Destination))
}

// Restore triggers a restore from a specific restore point.
//
//	POST /api/v1/jobs/{id}/restore
func (h *JobHandler) Restore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RestorePointID int64    `json:"restore_point_id"`
		Items          []string `json:"items"`
		ItemName       string   `json:"item_name"`
		ItemType       string   `json:"item_type"`
		Destination    string   `json:"destination"`
		Passphrase     string   `json:"passphrase"`
		// FilePaths is the optional per-item include-list used by the
		// partial-restore file picker. Keys are item names; values are
		// tar entry paths chosen from the index sidecar. Items absent
		// from this map (or with an empty slice) restore everything.
		FilePaths map[string][]string `json:"file_paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.RestorePointID == 0 {
		respondError(w, http.StatusBadRequest, "restore_point_id is required")
		return
	}

	// Find the restore point in the database.
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rps, err := h.db.ListRestorePoints(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}

	var found *db.RestorePoint
	for _, rp := range rps {
		if rp.ID == req.RestorePointID {
			found = &rp
			break
		}
	}
	if found == nil {
		respondError(w, http.StatusNotFound, "restore point not found")
		return
	}

	// Build the list of items to restore. Supports three modes:
	// 1. Legacy single item: item_name + item_type
	// 2. Named list: items array (types resolved from job_items)
	// 3. All items: no items/item_name → restore everything in the job
	type restoreTarget struct {
		Name string
		Type string
	}

	var targets []restoreTarget

	if req.ItemName != "" && req.ItemType != "" {
		// Legacy single-item restore.
		targets = append(targets, restoreTarget{Name: req.ItemName, Type: req.ItemType})
	} else {
		// Look up job items to resolve types.
		jobItems, itemsErr := h.db.GetJobItems(id)
		if itemsErr != nil {
			respondInternalError(w, fmt.Errorf("fetching job items: %w", itemsErr))
			return
		}
		itemTypeMap := make(map[string]string, len(jobItems))
		for _, ji := range jobItems {
			itemTypeMap[ji.ItemName] = ji.ItemType
		}

		if len(req.Items) > 0 {
			// Restore specific named items.
			for _, name := range req.Items {
				iType, ok := itemTypeMap[name]
				if !ok {
					respondError(w, http.StatusBadRequest, "item not found in job: "+name)
					return
				}
				targets = append(targets, restoreTarget{Name: name, Type: iType})
			}
		} else {
			// Restore all items from the job.
			for _, ji := range jobItems {
				targets = append(targets, restoreTarget{Name: ji.ItemName, Type: ji.ItemType})
			}
		}
	}

	if len(targets) == 0 {
		respondError(w, http.StatusBadRequest, "no items to restore")
		return
	}

	// Reject items this restore point never captured (e.g. added to the job
	// after these backups ran). Restoring them would fail mid-run; fail fast
	// with a clear message instead. Skipped for legacy restore points whose
	// per-item membership is unknown.
	if members, known := found.BackedUpItems(); known {
		for _, t := range targets {
			if _, ok := members[t.Name]; !ok {
				respondError(w, http.StatusBadRequest, t.Name+" is not in this restore point")
				return
			}
		}
	}

	// Build runner targets and execute tracked restore asynchronously.
	runnerTargets := make([]runner.RestoreTarget, 0, len(targets))
	for _, t := range targets {
		runnerTargets = append(runnerTargets, runner.RestoreTarget{
			Name:      t.Name,
			Type:      t.Type,
			FilePaths: req.FilePaths[t.Name],
		})
	}

	go h.runner.RunRestore(*found, runnerTargets, req.Destination, req.Passphrase)

	respondJSON(w, http.StatusAccepted, map[string]any{
		"message":          "restore started",
		"restore_point_id": found.ID,
		"items":            len(targets),
	})
}

// NextRun returns the next scheduled run time for a single job.
//
//	GET /api/v1/jobs/{id}/next-run
func (h *JobHandler) NextRun(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	if h.nextRun == nil {
		respondJSON(w, http.StatusOK, map[string]any{"scheduled": false})
		return
	}
	next, ok := h.nextRun(id)
	if !ok {
		respondJSON(w, http.StatusOK, map[string]any{"scheduled": false})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"scheduled": true, "next_run": next})
}

// scanStale loads the job's items, classifies each against live inventory,
// persists missing_since (marking newly-missing, clearing reappeared), and
// returns the items currently classified Missing.
func (h *JobHandler) scanStale(jobID int64) ([]db.JobItem, error) {
	items, err := h.db.GetJobItems(jobID)
	if err != nil {
		return nil, err
	}
	inv := engine.GatherInventory()
	var stale []db.JobItem
	var markIDs, clearIDs []int64
	for _, item := range items {
		settings, err := item.ParsedSettings()
		if err != nil {
			log.Printf("Warning: job item %d has malformed settings JSON: %v", item.ID, err)
		}
		status := inv.Status(item.ItemType, item.ItemName, settings)
		if status == engine.StatusMissing {
			stale = append(stale, item)
			if item.MissingSince == nil {
				markIDs = append(markIDs, item.ID)
			}
		} else if status == engine.StatusPresent && item.MissingSince != nil {
			// Only clear when confirmed PRESENT — StatusUnknown (engine down)
			// must not clear a real stale mark.
			clearIDs = append(clearIDs, item.ID)
		}
	}
	if len(markIDs) > 0 {
		if err := h.db.MarkJobItemsMissing(markIDs, time.Now().UTC().Format(time.RFC3339)); err != nil {
			log.Printf("Warning: failed to mark job items missing %v: %v", markIDs, err)
		}
	}
	if len(clearIDs) > 0 {
		if err := h.db.ClearJobItemsMissing(clearIDs); err != nil {
			log.Printf("Warning: failed to clear missing_since on %v: %v", clearIDs, err)
		}
	}
	return stale, nil
}

// GetStaleItems runs a live scan and returns the job's currently-missing items.
//
//	GET /api/v1/jobs/{id}/stale-items
func (h *JobHandler) GetStaleItems(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	if _, err := h.db.GetJob(id); err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	stale, err := h.scanStale(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	if stale == nil {
		stale = []db.JobItem{}
	}
	respondJSON(w, http.StatusOK, map[string]any{"stale_items": stale, "count": len(stale)})
}

// DeleteJobItem removes a single item from a job (per-item remediation). Only
// the item row is deleted; existing restore points are preserved.
//
//	DELETE /api/v1/jobs/{id}/items/{itemId}
func (h *JobHandler) DeleteJobItem(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	itemID, ok := parseID(w, r, "itemId")
	if !ok {
		return
	}
	items, err := h.db.GetJobItems(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	found := false
	for _, it := range items {
		if it.ID == itemID {
			found = true
			break
		}
	}
	if !found {
		respondError(w, http.StatusNotFound, "item not found in job")
		return
	}
	if err := h.db.DeleteJobItem(itemID); err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"deleted": itemID})
	h.reloadScheduler()
	h.notifyConfigChange()
	h.broadcastConfigChange("job")
}

// RemoveStaleItems re-validates and deletes all items that are STILL missing,
// returning what was removed. Re-validation avoids removing an item that
// reappeared between the scan and the click.
//
//	POST /api/v1/jobs/{id}/stale-items/remove
func (h *JobHandler) RemoveStaleItems(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	if _, err := h.db.GetJob(id); err != nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	stale, err := h.scanStale(id)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	ids := make([]int64, 0, len(stale))
	for _, it := range stale {
		ids = append(ids, it.ID)
	}
	if err := h.db.DeleteJobItemsByIDs(ids); err != nil {
		respondInternalError(w, err)
		return
	}
	if stale == nil {
		stale = []db.JobItem{}
	}
	respondJSON(w, http.StatusOK, map[string]any{"removed": stale, "count": len(stale)})
	if len(ids) > 0 {
		h.reloadScheduler()
		h.notifyConfigChange()
		h.broadcastConfigChange("job")
	}
}

// AllNextRuns returns next scheduled run times for all jobs.
//
//	GET /api/v1/jobs/next-runs
func (h *JobHandler) AllNextRuns(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.db.ListJobs()
	if err != nil {
		respondInternalError(w, err)
		return
	}
	result := make(map[string]any)
	for _, job := range jobs {
		if h.nextRun != nil {
			if next, ok := h.nextRun(job.ID); ok {
				result[strconv.FormatInt(job.ID, 10)] = next
			}
		}
	}
	respondJSON(w, http.StatusOK, result)
}
