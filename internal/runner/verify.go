package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"strings"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/engine"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// VerifyMode selects the verification strategy.
//
//   - VerifyModeQuick: storage HEAD on every recorded file in the restore
//     point's manifest. Reports any 404 / unexpected-size mismatch. Cheap
//     enough to schedule weekly.
//   - VerifyModeDeep: HEAD + full stream read + SHA-256 recompute against
//     the recorded checksum. Bandwidth-equivalent to one full restore.
//     Catches bit rot on cold storage.
type VerifyMode string

const (
	VerifyModeQuick VerifyMode = "quick"
	VerifyModeDeep  VerifyMode = "deep"
)

// IsValid reports whether m is a recognised verify mode.
func (m VerifyMode) IsValid() bool {
	return m == VerifyModeQuick || m == VerifyModeDeep
}

// VerifyResult is a per-file outcome from a verify run, surfaced to the
// caller via the verify_runs row's status / error_summary fields and to
// observers via the WebSocket stream.
type VerifyResult struct {
	Path     string // storage path relative to the storage destination root
	Expected string // expected SHA-256 (deep mode only)
	Actual   string // actual SHA-256  (deep mode only)
	Size     int64  // bytes read for this file (deep mode)
	Err      error  // non-nil on missing file, size mismatch, or checksum mismatch
}

// RunScheduledVerify is the scheduler-facing wrapper: given a job ID and a
// mode string, pick the job's most recent restore point and dispatch a
// verify run. Used as the VerifyRunner hook in the cron scheduler. Logs
// and silently no-ops when the job has no restore points yet (a freshly
// created job won't have run a backup).
func (r *Runner) RunScheduledVerify(jobID int64, mode string) {
	job, err := r.db.GetJob(jobID)
	if err != nil {
		log.Printf("runner: scheduled verify: cannot load job %d: %v", jobID, err)
		return
	}
	rp, err := r.db.GetLastRestorePoint(jobID)
	if err != nil {
		log.Printf("runner: scheduled verify: job %d (%s) has no restore points yet, skipping", jobID, job.Name)
		return
	}
	id, err := r.RunVerify(rp, VerifyMode(mode))
	if err != nil {
		log.Printf("runner: scheduled verify: dispatch failed for job %d: %v", jobID, err)
		return
	}
	log.Printf("runner: scheduled verify queued for job %d (%s), mode=%s, verify_run_id=%d, restore_point_id=%d",
		jobID, job.Name, mode, id, rp.ID)
}

// RunVerify executes a verification of one restore point. It returns the
// new verify_run row's ID immediately and continues in a background goroutine
// so the API call is non-blocking. The Runner's job-mutex is NOT taken —
// verification runs concurrently with scheduled backups.
func (r *Runner) RunVerify(rp db.RestorePoint, mode VerifyMode) (int64, error) {
	if !mode.IsValid() {
		return 0, fmt.Errorf("invalid verify mode %q (want %q or %q)", mode, VerifyModeQuick, VerifyModeDeep)
	}
	job, err := r.db.GetJob(rp.JobID)
	if err != nil {
		return 0, fmt.Errorf("loading job for restore point %d: %w", rp.ID, err)
	}
	dest, err := r.db.GetStorageDestination(job.StorageDestID)
	if err != nil {
		return 0, fmt.Errorf("loading storage destination %d: %w", job.StorageDestID, err)
	}
	id, err := r.db.CreateVerifyRun(rp.ID, string(mode))
	if err != nil {
		return 0, err
	}
	go r.runVerifyLoop(id, rp, mode, dest)
	return id, nil
}

// runVerifyLoop is the actual work goroutine. Updates the verify_runs row
// incrementally; broadcasts WebSocket progress for the UI.
//
// Dedup restore points (rp.ManifestID populated) take an entirely different
// path: there are no per-file tarballs on storage, just a manifest plus a
// pack of chunks. The dedup branch is in runVerifyLoopDedup; the classic
// per-file flow below is unchanged.
func (r *Runner) runVerifyLoop(verifyID int64, rp db.RestorePoint, mode VerifyMode, dest db.StorageDestination) {
	// A dedup restore point is identified by either the single-item
	// manifest_id shortcut OR per-item IDs in metadata.item_manifests
	// (multi-item jobs). The classic per-file path below has neither and
	// relies on metadata.checksums instead.
	if len(restorePointTopManifestIDs(rp)) > 0 {
		r.runVerifyLoopDedup(verifyID, rp, mode, dest)
		return
	}
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		r.finishVerify(verifyID, "failed", fmt.Sprintf("storage adapter: %v", err))
		return
	}
	defer storage.CloseAdapter(adapter)

	expected, err := parseRestorePointChecksums(rp.Metadata)
	if err != nil {
		r.finishVerify(verifyID, "failed", fmt.Sprintf("parsing recorded checksums: %v", err))
		return
	}
	if len(expected) == 0 {
		r.finishVerify(verifyID, "failed", "no recorded file checksums in restore point metadata (was this backup created with verify_backup enabled?)")
		return
	}

	r.broadcast(map[string]any{
		"type":             "verify_started",
		"verify_run_id":    verifyID,
		"restore_point_id": rp.ID,
		"mode":             string(mode),
		"files_total":      len(expected),
	})

	var (
		filesChecked int
		filesFailed  int
		bytesRead    int64
		failures     []string
	)
	for storageRelPath, want := range expected {
		fullPath := path.Join(rp.StoragePath, storageRelPath)
		result := r.verifyOneFile(adapter, fullPath, want, mode)
		filesChecked++
		bytesRead += result.Size
		if result.Err != nil {
			filesFailed++
			failures = append(failures, fmt.Sprintf("%s: %v", fullPath, result.Err))
		}
		if filesChecked%5 == 0 || result.Err != nil {
			if err := r.db.UpdateVerifyRunProgress(verifyID, filesChecked, filesFailed, bytesRead); err != nil {
				log.Printf("runner: verify progress update failed for run %d: %v", verifyID, err)
			}
			r.broadcast(map[string]any{
				"type":          "verify_progress",
				"verify_run_id": verifyID,
				"files_checked": filesChecked,
				"files_total":   len(expected),
				"files_failed":  filesFailed,
				"bytes_read":    bytesRead,
			})
		}
	}

	// Final progress flush.
	if err := r.db.UpdateVerifyRunProgress(verifyID, filesChecked, filesFailed, bytesRead); err != nil {
		log.Printf("runner: verify final progress update failed for run %d: %v", verifyID, err)
	}

	status := "passed"
	summary := ""
	if filesFailed > 0 {
		status = "failed"
		summary = strings.Join(failures, "\n")
	}
	r.finishVerify(verifyID, status, summary)
	r.broadcast(map[string]any{
		"type":          "verify_complete",
		"verify_run_id": verifyID,
		"status":        status,
		"files_checked": filesChecked,
		"files_failed":  filesFailed,
		"bytes_read":    bytesRead,
	})
}

// runVerifyLoopDedup verifies a dedup restore point. Quick mode reads the
// manifest, collects the unique set of pack files referenced by every chunk,
// and Stats each pack (no decryption, cheap). Deep mode range-reads every
// chunk, AEAD-decrypts it, and recomputes its content ID to detect bit rot
// or tampering on the pack store.
//
// Progress events use the same `verify_progress` / `verify_complete` shape
// as the classic loop so the WebSocket UI consumer needs no branching.
// `files_checked` reflects packs verified in quick mode and chunks verified
// in deep mode (chunk granularity is the natural unit for dedup stats).
func (r *Runner) runVerifyLoopDedup(verifyID int64, rp db.RestorePoint, mode VerifyMode, dest db.StorageDestination) {
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		r.finishVerify(verifyID, "failed", fmt.Sprintf("storage adapter: %v", err))
		return
	}
	defer storage.CloseAdapter(adapter)

	repo, err := dedup.OpenRepo(r.db, adapter, dest.ID, r.serverKey)
	if err != nil {
		r.finishVerify(verifyID, "failed", fmt.Sprintf("open dedup repo: %v", err))
		return
	}

	// Collect every top-level manifest for this restore point (one per item
	// for multi-item jobs) and expand the closure through container-volume
	// sub-manifests so deep verify re-hashes the actual file data — not just
	// the manifest pointers. allChunks therefore covers manifest chunks AND
	// leaf data chunks, exactly what a restore would read.
	tops := restorePointTopManifestIDs(rp)
	if len(tops) == 0 {
		r.finishVerify(verifyID, "failed", "dedup restore point has no manifest IDs")
		return
	}
	manifestChunks, dataChunks, err := engine.WalkManifestClosure(repo, tops)
	if err != nil {
		r.finishVerify(verifyID, "failed", fmt.Sprintf("walking manifest closure: %v", err))
		return
	}
	allChunks := make([]dedup.ID, 0, len(manifestChunks)+len(dataChunks))
	allChunks = append(allChunks, manifestChunks...)
	allChunks = append(allChunks, dataChunks...)

	var (
		filesChecked int
		filesFailed  int
		bytesRead    int64
		failures     []string
	)

	// totalUnits is what the UI shows as the denominator on the progress bar.
	// Quick = unique pack count; Deep = total chunks across the closure.
	totalUnits := 0
	switch mode {
	case VerifyModeQuick:
		seen := map[string]struct{}{}
		for _, cid := range allChunks {
			packPath, _, _, locErr := repo.LocateForVerify(cid)
			if locErr != nil {
				continue
			}
			seen[packPath] = struct{}{}
		}
		totalUnits = len(seen)
	case VerifyModeDeep:
		totalUnits = len(allChunks)
	}

	r.broadcast(map[string]any{
		"type":             "verify_started",
		"verify_run_id":    verifyID,
		"restore_point_id": rp.ID,
		"mode":             string(mode),
		"files_total":      totalUnits,
	})

	emitProgress := func() {
		if err := r.db.UpdateVerifyRunProgress(verifyID, filesChecked, filesFailed, bytesRead); err != nil {
			log.Printf("runner: verify progress update failed for run %d: %v", verifyID, err)
		}
		r.broadcast(map[string]any{
			"type":          "verify_progress",
			"verify_run_id": verifyID,
			"files_checked": filesChecked,
			"files_total":   totalUnits,
			"files_failed":  filesFailed,
			"bytes_read":    bytesRead,
		})
	}

	switch mode {
	case VerifyModeQuick:
		// Gather unique packs referenced by every chunk in the closure.
		// LocateForVerify returns the pack path for each chunk ID; we
		// dedupe and Stat each pack exactly once.
		packs := map[string]struct{}{}
		for _, cid := range allChunks {
			packPath, _, _, locErr := repo.LocateForVerify(cid)
			if locErr != nil {
				filesFailed++
				failures = append(failures, fmt.Sprintf("locate chunk %x: %v", cid[:8], locErr))
				continue
			}
			packs[packPath] = struct{}{}
		}
		for p := range packs {
			if _, statErr := adapter.Stat(p); statErr != nil {
				filesFailed++
				failures = append(failures, fmt.Sprintf("stat pack %s: %v", p, statErr))
				continue
			}
			filesChecked++
			if filesChecked%5 == 0 || filesFailed > 0 {
				emitProgress()
			}
		}

	case VerifyModeDeep:
		// For every chunk in the closure: range-read from its pack, decrypt
		// (AEAD tag verifies authenticity), and recompute the content ID.
		// Any failure increments filesFailed but the loop continues so we
		// report every bad chunk, not just the first.
		for _, cid := range allChunks {
			body, getErr := repo.Get(cid)
			if getErr != nil {
				filesFailed++
				failures = append(failures, fmt.Sprintf("get chunk %x: %v", cid[:8], getErr))
				continue
			}
			got := repo.ChunkID(body)
			if got != cid {
				filesFailed++
				failures = append(failures, fmt.Sprintf("chunk id mismatch: want %x got %x", cid[:8], got[:8]))
				continue
			}
			bytesRead += int64(len(body))
			filesChecked++
			if filesChecked%50 == 0 {
				emitProgress()
			}
		}

	default:
		r.finishVerify(verifyID, "failed", fmt.Sprintf("unknown verify mode: %s", mode))
		return
	}

	// Final progress flush so the row's counters match the totals we report.
	emitProgress()

	status := "passed"
	summary := ""
	if filesFailed > 0 {
		status = "failed"
		summary = strings.Join(failures, "\n")
	}
	r.finishVerify(verifyID, status, summary)
	r.broadcast(map[string]any{
		"type":          "verify_complete",
		"verify_run_id": verifyID,
		"status":        status,
		"files_checked": filesChecked,
		"files_failed":  filesFailed,
		"bytes_read":    bytesRead,
	})
}

// verifyOneFile applies the configured mode to a single file. Quick =
// adapter.Stat + size compare; Deep = adapter.Read + streaming SHA-256.
func (r *Runner) verifyOneFile(adapter storage.Adapter, storagePath string, want recordedChecksum, mode VerifyMode) VerifyResult {
	info, err := adapter.Stat(storagePath)
	if err != nil {
		return VerifyResult{Path: storagePath, Err: fmt.Errorf("stat: %w", err)}
	}
	if want.Size > 0 && info.Size != want.Size {
		return VerifyResult{Path: storagePath, Size: 0, Err: fmt.Errorf("size mismatch: storage=%d recorded=%d", info.Size, want.Size)}
	}
	if mode == VerifyModeQuick {
		// Quick mode performs no data transfer — Size: 0 ensures
		// the run row's bytes_read reflects what was actually read
		// from storage (zero for quick, full size for deep).
		return VerifyResult{Path: storagePath, Size: 0}
	}

	rc, err := adapter.Read(storagePath)
	if err != nil {
		return VerifyResult{Path: storagePath, Err: fmt.Errorf("read: %w", err)}
	}
	defer rc.Close()
	hasher := sha256.New()
	n, copyErr := io.Copy(hasher, rc)
	if copyErr != nil {
		return VerifyResult{Path: storagePath, Size: n, Err: fmt.Errorf("stream: %w", copyErr)}
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if want.SHA256 != "" && actual != want.SHA256 {
		return VerifyResult{
			Path:     storagePath,
			Expected: want.SHA256,
			Actual:   actual,
			Size:     n,
			Err:      errors.New("checksum mismatch"),
		}
	}
	return VerifyResult{Path: storagePath, Expected: want.SHA256, Actual: actual, Size: n}
}

func (r *Runner) finishVerify(verifyID int64, status, errorSummary string) {
	if err := r.db.FinishVerifyRun(verifyID, status, errorSummary); err != nil {
		log.Printf("runner: failed to mark verify run %d as %s: %v", verifyID, status, err)
	}
}

// restorePointTopManifestIDs returns every top-level dedup manifest ID for a
// restore point: the single-item shortcut (restore_points.manifest_id) plus
// each per-item hex ID under metadata.item_manifests (multi-item jobs). Used
// both to detect a dedup restore point and to seed the verify closure walk.
// Returns nil for a classic (non-dedup) restore point.
func restorePointTopManifestIDs(rp db.RestorePoint) []dedup.ID {
	var (
		out  []dedup.ID
		seen = map[dedup.ID]struct{}{}
	)
	add := func(id dedup.ID) {
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(rp.ManifestID) == 32 {
		var id dedup.ID
		copy(id[:], rp.ManifestID)
		add(id)
	}
	if rp.Metadata != "" {
		var meta struct {
			ItemManifests map[string]string `json:"item_manifests"`
		}
		if err := json.Unmarshal([]byte(rp.Metadata), &meta); err == nil {
			for _, hexID := range meta.ItemManifests {
				raw, derr := hex.DecodeString(hexID)
				if derr != nil || len(raw) != 32 {
					continue
				}
				var id dedup.ID
				copy(id[:], raw)
				add(id)
			}
		}
	}
	return out
}

// recordedChecksum holds what we know about a file from the restore point's
// manifest. Size may be 0 if the manifest didn't record it (older backups).
type recordedChecksum struct {
	SHA256 string
	Size   int64
}

// parseRestorePointChecksums extracts the per-file SHA-256 checksums from a
// restore point's metadata.checksums map (written by runner.writeManifest).
// Returns a map keyed by the storage-relative path (e.g.
// "ItemName/data.tar.zst").
//
// The actual restore-point metadata shape (see writeManifest in runner.go)
// records `items` as a *count* and `item_sizes` as the per-item total size;
// per-file sizes are not currently persisted, so verify quick-mode can only
// rely on the SHA-256 list. Size mismatches are still surfaced when the
// adapter's Stat() differs from a recorded size > 0; today's restore points
// always carry Size = 0 and the check is skipped.
func parseRestorePointChecksums(metadata string) (map[string]recordedChecksum, error) {
	if metadata == "" {
		return nil, nil
	}
	var meta struct {
		Checksums map[string]map[string]string `json:"checksums"`
	}
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
		return nil, err
	}
	out := make(map[string]recordedChecksum)
	for itemName, files := range meta.Checksums {
		for fileName, hexHash := range files {
			key := path.Join(itemName, fileName)
			out[key] = recordedChecksum{SHA256: hexHash}
		}
	}
	return out, nil
}

// Re-export the started-time placeholder so handler callers can supply a
// reasonable value when they need to construct a VerifyRun-like response
// before the row exists. Unused today but kept for future scheduler hooks.
var _ = time.Now
