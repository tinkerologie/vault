package runner

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/safepath"
	"github.com/tinkerologie/vault/internal/storage"
)

const (
	maxEvidenceChainDepth = 128
	maxEvidenceRequests   = 64
	maxEvidenceObjects    = 64
	evidenceProbeTimeout  = 3 * time.Second
)

type RecoveryEvidence struct {
	Point               PointIdentity        `json:"restore_point"`
	Item                ItemSelection        `json:"item"`
	Membership          Status               `json:"membership"`
	ItemTypeEvidence    Status               `json:"item_type_evidence"`
	Chain               RecoveryChain        `json:"chain"`
	Verification        RecoveryVerification `json:"verification"`
	EligibleForApproval bool                 `json:"eligible_for_approval"`
	Warnings            []string             `json:"warnings"`
}

type RecoveryChain struct {
	Status                Status              `json:"status"`
	Reason                string              `json:"reason,omitempty"`
	MissingRestorePointID int64               `json:"missing_restore_point_id,omitempty"`
	Steps                 []RecoveryChainStep `json:"steps"`
}

type RecoveryChainStep struct {
	RestorePointID   int64              `json:"restore_point_id"`
	BackupType       string             `json:"backup_type"`
	Membership       Status             `json:"membership"`
	ItemTypeEvidence Status             `json:"item_type_evidence"`
	DataRequired     bool               `json:"data_required"`
	Artifacts        Status             `json:"artifacts,omitempty"`
	ClassicObjects   []storage.FileInfo `json:"-"`
}

type RecoveryVerification struct {
	Status               string     `json:"status"`
	Mode                 string     `json:"mode,omitempty"`
	StartedAt            *time.Time `json:"started_at,omitempty"`
	CompletedAt          *time.Time `json:"completed_at,omitempty"`
	VerifiedDuringBackup *bool      `json:"verified_during_backup"`
}

type restoreChainError struct {
	status   Status
	reason   string
	parentID int64
	err      error
}

func (e *restoreChainError) Error() string { return e.err.Error() }

func (e *restoreChainError) Unwrap() error { return e.err }

// walkRestoreChain returns recorded ancestors in the same order as restore
// execution. It keeps the known portion when an ancestor cannot be resolved.
func walkRestoreChain(ctx context.Context, database *db.DB, point db.RestorePoint, limit int) ([]db.RestorePoint, *restoreChainError) {
	chain := []db.RestorePoint{point}
	seen := map[int64]bool{point.ID: true}
	current := point
	var failure *restoreChainError
	if point.BackupType != "full" {
		for current.ParentRestorePointID > 0 {
			id := current.ParentRestorePointID
			if seen[id] {
				failure = &restoreChainError{status: Broken, reason: "cycle", err: fmt.Errorf("restore chain loops back to restore point %d", id)}
				break
			}
			if limit > 0 && len(chain) >= limit {
				failure = &restoreChainError{status: Unknown, reason: "depth_limit", err: errors.New("restore chain exceeds the evidence depth limit")}
				break
			}
			parent, err := database.GetRestorePointContext(ctx, id)
			switch {
			case errors.Is(err, db.ErrNotFound):
				failure = &restoreChainError{status: Missing, reason: "missing_parent", parentID: id, err: fmt.Errorf("missing parent restore point %d for restore point %d", id, current.ID)}
			case err != nil:
				failure = &restoreChainError{status: Unknown, reason: "catalog_unavailable", err: fmt.Errorf("getting parent restore point %d: %w", id, err)}
			case parent.JobID != point.JobID || parent.SourceID != point.SourceID:
				failure = &restoreChainError{status: Broken, reason: "foreign_parent", err: fmt.Errorf("parent restore point %d belongs to another Job or Replication Source", id)}
			}
			if failure != nil {
				break
			}
			chain = append(chain, parent)
			seen[id] = true
			current = parent
		}
	}
	slices.Reverse(chain)
	return chain, failure
}

func isDependentPoint(point db.RestorePoint) bool {
	return point.BackupType == "incremental" || point.BackupType == "differential"
}

// InspectRecovery resolves item-specific evidence without running Verification
// or opening backup file contents. The result describes a point in time.
func InspectRecovery(ctx context.Context, database *db.DB, id int64, item ItemSelection, wrap func(storage.Adapter) RecoveryMetadata) (RecoveryEvidence, error) {
	if err := item.Validate(); err != nil {
		return RecoveryEvidence{}, err
	}
	point, err := database.GetRestorePointContext(ctx, id)
	if err != nil {
		return RecoveryEvidence{}, err
	}
	points, failure := walkRestoreChain(ctx, database, point, maxEvidenceChainDepth)
	_, deduplicated, selectedManifestErr := lookupItemManifest(point, item.ItemName)
	if failure == nil {
		switch {
		case isDependentPoint(point) && points[0].BackupType != "full" && !standaloneImportedDedup(point, deduplicated):
			failure = &restoreChainError{status: Missing, reason: "missing_full_backup", err: errors.New("restore chain has no terminal full backup")}
		case point.BackupType != "full" && !isDependentPoint(point):
			failure = &restoreChainError{status: Unknown, reason: "unknown_backup_type", err: errors.New("unrecognized backup type")}
		}
	}

	result := RecoveryEvidence{
		Point: IdentifyPoint(point), Item: item.Redacted(),
		Membership: Membership(point, item), ItemTypeEvidence: recordedItemType(point, item),
		Chain:        RecoveryChain{Status: Confirmed, Steps: make([]RecoveryChainStep, 0, len(points))},
		Verification: recoveryVerification(ctx, database, point), Warnings: make([]string, 0, 2),
	}
	if failure != nil {
		result.Chain.Status, result.Chain.Reason = failure.status, failure.reason
		result.Chain.MissingRestorePointID = failure.parentID
	}
	for _, step := range points {
		member := Membership(step, item)
		selected := step.ID == point.ID
		// The Job chain still needs a full anchor. Classic replay skips
		// every point known to omit this item, including an earlier full.
		// A deduplicated point needs only its own stored manifest.
		// A conflicting type with the same name is not a usable
		// replay step. The classic Runner selects its steps by name.
		names, _ := step.BackedUpItems()
		_, sameName := names[item.ItemName]
		required := selected || (!deduplicated && (member != Missing || sameName))
		entry := RecoveryChainStep{RestorePointID: step.ID, BackupType: IdentifyPoint(step).BackupType, Membership: member,
			ItemTypeEvidence: recordedItemType(step, item), DataRequired: required}
		if required {
			entry.Artifacts = Unknown
			manifestErr := selectedManifestErr
			if !selected {
				_, _, manifestErr = lookupItemManifest(step, item.ItemName)
			}
			if manifestErr != nil {
				entry.Artifacts = Broken
			}
		}
		result.Chain.Steps = append(result.Chain.Steps, entry)
		if failure == nil && required && member == Missing {
			result.Chain.Status, result.Chain.Reason = Missing, "item_missing_from_required_step"
		} else if result.Chain.Status == Confirmed && required && member == Unknown {
			result.Chain.Status, result.Chain.Reason = Unknown, "unknown_step_membership"
		}
	}
	replayEvidenceUnavailable := false
	if result.Membership == Confirmed && failure == nil && selectedManifestErr == nil {
		statuses := probeRecoveryArtifacts(ctx, database, point.JobID, points, result.Chain.Steps, item, deduplicated, wrap)
		for i, probe := range statuses {
			status := probe.status
			if status != "" {
				replayEvidenceUnavailable = replayEvidenceUnavailable || probe.replayEvidenceUnavailable
				result.Chain.Steps[i].ClassicObjects = probe.objects
				result.Chain.Steps[i].Artifacts = status
				if _, manifest, manifestErr := lookupItemManifest(points[i], item.ItemName); !manifest && manifestErr == nil && status == Confirmed {
					result.Chain.Steps[i].ItemTypeEvidence = Confirmed
					if points[i].ID == point.ID {
						result.ItemTypeEvidence = Confirmed
					}
				}
			}
		}
	}
	result.EligibleForApproval = result.Membership == Confirmed && result.Chain.Status == Confirmed
	hasUnknownArtifacts := false
	for _, step := range result.Chain.Steps {
		if step.Artifacts == Broken || step.Artifacts == Missing ||
			(item.ItemType == "container" && step.Artifacts == Unknown && len(step.ClassicObjects) != 0) {
			result.EligibleForApproval = false
		}
		hasUnknownArtifacts = hasUnknownArtifacts || step.Artifacts == Unknown
	}
	if result.Membership == Confirmed && result.ItemTypeEvidence == Unknown {
		result.Warnings = append(result.Warnings, "item_type_evidence_unavailable")
	}
	if hasUnknownArtifacts {
		result.Warnings = append(result.Warnings, "artifact_evidence_unavailable")
	}
	if replayEvidenceUnavailable {
		result.Warnings = append(result.Warnings, "folder_replay_evidence_unavailable")
	}
	switch result.Verification.Status {
	case "passed":
		// The timestamp is surfaced so operators can assess age.
	case "none":
		if result.Verification.VerifiedDuringBackup == nil || !*result.Verification.VerifiedDuringBackup {
			result.Warnings = append(result.Warnings, "unverified_backup")
		}
	default:
		result.Warnings = append(result.Warnings, "latest_verification_"+result.Verification.Status)
	}
	return result, nil
}

// Imported dedup manifests are complete snapshots. Import does not recreate
// parent IDs, so this case is distinct from a recorded parent that is missing.
func standaloneImportedDedup(point db.RestorePoint, deduplicated bool) bool {
	if !deduplicated || point.ParentRestorePointID != 0 {
		return false
	}
	var meta struct {
		Imported bool `json:"imported"`
	}
	return json.Unmarshal([]byte(point.Metadata), &meta) == nil && meta.Imported
}

func recoveryVerification(ctx context.Context, database *db.DB, point db.RestorePoint) RecoveryVerification {
	result := RecoveryVerification{Status: "none"}
	var meta struct {
		Verified *bool `json:"verified"`
	}
	if json.Unmarshal([]byte(point.Metadata), &meta) == nil {
		result.VerifiedDuringBackup = meta.Verified
	}
	runs, err := database.ListVerifyRunsForRestorePointContext(ctx, point.ID, 1)
	if err != nil {
		result.Status = "unknown"
	} else if len(runs) > 0 {
		run := runs[0]
		result.Status = "unknown"
		switch run.Status {
		case "passed", "failed", "cancelled", "running":
			result.Status = run.Status
		}
		if VerifyMode(run.Mode).IsValid() {
			result.Mode = run.Mode
		}
		result.StartedAt, result.CompletedAt = &run.StartedAt, run.CompletedAt
	}
	return result
}

// Legacy providers can block inside context-free Stat/List methods. Retain the
// slot until the provider and its cleanup actually return, even after the
// caller's timeout, so slow storage cannot accumulate unbounded workers.
var recoveryProbeSlots = make(chan struct{}, 8)

type stepProbeResult struct {
	index                     int
	status                    Status
	objects                   []storage.FileInfo
	replayEvidenceUnavailable bool
}

func probeRecoveryArtifacts(ctx context.Context, database *db.DB, jobID int64, points []db.RestorePoint, steps []RecoveryChainStep, item ItemSelection, deduplicated bool, wrap func(storage.Adapter) RecoveryMetadata) []stepProbeResult {
	ctx, cancel := context.WithTimeout(ctx, evidenceProbeTimeout)
	defer cancel()
	select {
	case recoveryProbeSlots <- struct{}{}:
	default:
		return nil
	}
	// The caller may populate response steps as soon as the deadline expires
	// while a legacy provider is still returning from its last operation.
	steps = slices.Clone(steps)
	updates := make(chan stepProbeResult, len(steps))
	go func() {
		defer func() { <-recoveryProbeSlots }()
		defer close(updates)
		defer guardPanic("recovery metadata probe")
		job, err := database.GetJobContext(ctx, jobID)
		if err != nil {
			return
		}
		dest, err := database.GetStorageDestinationContext(ctx, job.StorageDestID)
		if errors.Is(err, db.ErrNotFound) {
			for i, step := range steps {
				if step.Artifacts == Unknown {
					updates <- stepProbeResult{index: i, status: Missing}
				}
			}
		}
		if err != nil {
			return
		}
		adapter, err := storage.NewAdapterWithOptions(dest.Type, dest.Config, storage.Options{
			MetadataProbeContext: ctx, MetadataProbeLimit: maxEvidenceObjects,
		})
		if err != nil {
			return
		}
		defer storage.CloseAdapter(adapter)
		probe := evidenceProbe{adapter: wrap(adapter), remaining: maxEvidenceRequests}
		for i, step := range steps {
			if ctx.Err() != nil {
				return
			}
			if step.Artifacts == Unknown && step.Membership == Confirmed {
				probe.classicObjects = nil
				probe.replayEvidenceUnavailable = false
				status := probe.inspect(ctx, database, dest.ID, points[i], item, deduplicated)
				updates <- stepProbeResult{i, status, probe.classicObjects, probe.replayEvidenceUnavailable}
			}
		}
	}()
	statuses := make([]stepProbeResult, len(steps))
	for {
		select {
		case update, ok := <-updates:
			if !ok {
				return statuses
			}
			statuses[update.index] = update
		case <-ctx.Done():
			// Preserve proven outcomes even if a later step or provider
			// cleanup timed out.
			for {
				select {
				case update, ok := <-updates:
					if !ok {
						return statuses
					}
					statuses[update.index] = update
				default:
					return statuses
				}
			}
		}
	}
}

type RecoveryMetadata interface {
	Stat(string) (storage.FileInfo, error)
	List(string) ([]storage.FileInfo, error)
}

type evidenceProbe struct {
	adapter                   RecoveryMetadata
	remaining                 int
	classicObjects            []storage.FileInfo
	replayEvidenceUnavailable bool
}

func (p *evidenceProbe) inspect(ctx context.Context, database *db.DB, destID int64, point db.RestorePoint, item ItemSelection, selectedDeduplicated bool) Status {
	if manifestID, ok := resolveManifestID(point, item.ItemName); ok {
		// Classic container and VM restore stages classic files from every
		// step. Those paths cannot consume an ancestor's dedup manifest.
		if item.ItemType == "vm" || (item.ItemType == "container" && !selectedDeduplicated) {
			return Broken
		}
		return p.deduplicated(ctx, database, destID, manifestID)
	}
	return p.classic(point, item)
}

func (p *evidenceProbe) stat(name string) (storage.FileInfo, Status) {
	if p.remaining <= 0 {
		return storage.FileInfo{}, Unknown
	}
	p.remaining--
	info, err := p.adapter.Stat(name)
	switch {
	case storage.IsNotExist(err):
		return info, Missing
	case err != nil:
		return info, Unknown
	default:
		return info, Confirmed
	}
}

func (p *evidenceProbe) deduplicated(ctx context.Context, database *db.DB, destID int64, manifestID dedup.ID) Status {
	header, status := p.stat(dedup.RepoRoot + "/repo.json")
	if status != Confirmed {
		return status
	}
	if header.IsDir || header.Size <= 0 {
		return Broken
	}
	pack, offset, length, err := database.LocateDedupChunkContext(ctx, destID, manifestID[:])
	if errors.Is(err, sql.ErrNoRows) {
		// The SQLite index is a cache of storage state. A missing entry is
		// unusable by the current restore path even if the cache can be rebuilt.
		return Missing
	}
	if err != nil {
		return Unknown
	}
	if path.Clean(pack) != pack || !strings.HasPrefix(pack, dedup.RepoRoot+"/packs/") || offset < 0 || length <= 0 {
		return Broken
	}
	info, status := p.stat(pack)
	if status == Confirmed && (info.IsDir || offset > info.Size || length > info.Size-offset) {
		return Broken
	}
	return status
}

func (p *evidenceProbe) classic(point db.RestorePoint, item ItemSelection) Status {
	base, err := safepath.NormalizeRestorePointPath(point.StoragePath)
	if err != nil {
		return Unknown
	}
	name, err := safepath.NormalizeRelative(item.ItemName, false)
	if err != nil || name != item.ItemName || strings.Contains(name, `\`) || p.remaining <= 0 {
		return Unknown
	}
	prefix := path.Join(base, name)
	expected := parseItemChecksums(point.Metadata, item.ItemName)
	if len(expected) > maxEvidenceObjects {
		return Unknown
	}
	p.remaining--
	files, err := p.adapter.List(prefix)
	switch {
	case storage.IsNotExist(err):
		return Missing
	case err != nil:
		if len(expected) == 0 || (item.ItemType != "folder" && len(expected) > p.remaining) {
			return Unknown
		}
		unknown := false
		for name := range expected {
			if name != path.Base(name) || name == "." || name == ".." || strings.Contains(name, `\`) {
				return Broken
			}
			if optionalFolderReplayObject(item, name) {
				// Without a complete listing, reserve the Stat budget for data
				// that is required to restore the selected folder.
				p.replayEvidenceUnavailable = true
				continue
			}
			info, status := p.stat(path.Join(prefix, name))
			switch status {
			case Unknown:
				unknown = true
				continue
			case Missing:
				return Missing
			}
			info.Path = path.Join(prefix, name)
			files = append(files, info)
		}
		if unknown {
			return Unknown
		}
	}
	selected, _, err := selectRestoreObjects(files, expected)
	if err != nil {
		return Broken
	}
	if len(selected) == 0 {
		return Missing
	}
	if len(expected) != 0 && len(selected) != len(expected) {
		for name := range expected {
			if slices.ContainsFunc(selected, func(file storage.FileInfo) bool { return path.Base(file.Path) == name }) {
				continue
			}
			if !optionalFolderReplayObject(item, name) {
				return Missing
			}
			p.replayEvidenceUnavailable = true
		}
	}
	required := []string{"data.tar"}
	switch item.ItemType {
	case "container":
		required = []string{"config.json"}
		if point.BackupType == "full" {
			required = append(required, "image.tar")
		}
	case "vm":
		required = []string{"domain.xml"}
	}
	for _, name := range required {
		index := slices.IndexFunc(selected, func(file storage.FileInfo) bool {
			local := localRestoreName(path.Base(file.Path))
			if strings.HasSuffix(name, ".tar") {
				return local == name || local == name+".gz" || local == name+".zst"
			}
			return local == name
		})
		if index < 0 {
			return Missing
		}
		if selected[index].Size <= 0 {
			return Broken
		}
	}
	if item.ItemType == "vm" {
		return vmDiskEvidence(selected, expected)
	}
	if item.ItemType == "folder" || item.ItemType == "container" {
		slices.SortFunc(selected, func(a, b storage.FileInfo) int { return cmp.Compare(a.Path, b.Path) })
		p.classicObjects = selected
	}
	if item.ItemType == "container" {
		// The listing can establish config/image presence without showing
		// which volumes were captured. The operator's review reads the
		// actual inspect JSON and validates the saved volume inventory.
		if len(expected) == 0 || !slices.ContainsFunc(selected, func(file storage.FileInfo) bool {
			return localRestoreName(path.Base(file.Path)) == "volumes.json" && file.Size > 0
		}) {
			return Unknown
		}
	}
	return Confirmed
}

func optionalFolderReplayObject(item ItemSelection, name string) bool {
	return item.ItemType == "folder" && name == path.Base(name) && !strings.Contains(name, `\`) &&
		(isSidecarBase(name, engine.IndexSuffix) || isSidecarBase(name, engine.ListingSuffix))
}

// VM backups record their disks as vdisk<index>[.<format>]. A directory
// listing alone cannot tell how many disks the backup was meant to contain.
func vmDiskEvidence(files []storage.FileInfo, expected map[string]string) Status {
	found := false
	for _, file := range files {
		name := path.Base(file.Path)
		if _, recorded := expected[name]; !recorded {
			continue
		}
		local := localRestoreName(name)
		stem := strings.TrimSuffix(local, path.Ext(local))
		index, disk := strings.CutPrefix(stem, "vdisk")
		if !disk || index == "" || strings.IndexFunc(index, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			continue
		}
		if file.Size <= 0 {
			return Broken
		}
		found = true
	}
	if !found {
		return Unknown
	}
	return Confirmed
}
