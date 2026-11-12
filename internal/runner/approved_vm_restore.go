package runner

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tinkerologie/vault/internal/bounded"
	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/docsmeta"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/safepath"
	"github.com/tinkerologie/vault/internal/storage"
	"github.com/tinkerologie/vault/internal/tempdir"
)

const maxVMReviewObjects = 128

// VMRestoreHost is the local hypervisor boundary used by approved VM restores.
type VMRestoreHost interface {
	ReviewVMRestore(ctx context.Context, name, destination string, xmlData, metadata []byte, artifacts []string, key []byte) (engine.VMRestoreEffects, string, error)
	Restore(context.Context, engine.BackupItem, string, engine.ProgressFunc) error
	Close() error
}

func (r *Runner) openVMRestoreHost() (VMRestoreHost, error) {
	if r.vmRestoreHost != nil {
		return r.vmRestoreHost()
	}
	return engine.NewVMHandler()
}

// VMRestoreReview contains the required VM chain and the operator's preview.
type VMRestoreReview struct {
	Plan             ApprovedRestorePlan
	Effects          *engine.VMRestoreEffects
	Chain            RecoveryChain
	Membership       Status
	ItemTypeEvidence Status
	Verification     RecoveryVerification
	Warnings         []string
	Reasons          []string
	SourceDigest     string
	SourceAnchor     string
}

type vmRestoreWork struct {
	destination  string
	stagingAvoid []string
	expectation  string
	steps        []vmReviewedStep
}

type vmReviewedStep struct {
	evidence engine.VMBackupEvidence
	digest   string
}

func (work vmRestoreWork) RequiresPassphrase() bool { return false }

// InspectVMRestoreRequest reads only bounded metadata and local VM state.
// The selected VM's own full/delta history determines which points to stage.
func (r *Runner) InspectVMRestoreRequest(ctx context.Context, plan ApprovedRestorePlan, destination, passphrase string) (VMRestoreReview, error) {
	result := VMRestoreReview{
		Plan: plan, Membership: Membership(plan.Point, plan.Item), ItemTypeEvidence: recordedItemType(plan.Point, plan.Item),
		Chain: RecoveryChain{Status: Unknown, Steps: []RecoveryChainStep{}}, Reasons: []string{}, Warnings: []string{},
		Verification: recoveryVerification(ctx, r.db, plan.Point),
	}
	switch result.Verification.Status {
	case "passed":
	case "none":
		if result.Verification.VerifiedDuringBackup == nil || !*result.Verification.VerifiedDuringBackup {
			result.Warnings = append(result.Warnings, "unverified_backup")
		}
	default:
		result.Warnings = append(result.Warnings, "latest_verification_"+result.Verification.Status)
	}
	if result.Membership != Confirmed {
		result.Chain.Status = result.Membership
		result.Chain.Reason = "vm_membership_" + string(result.Membership)
		result.Reasons = append(result.Reasons, result.Chain.Reason)
		return result, nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	select {
	case approvedReadSlots <- struct{}{}:
	default:
		return VMRestoreReview{}, ErrRunnerBusy
	}
	type response struct {
		review VMRestoreReview
		err    error
	}
	done := make(chan response, 1)
	go func() {
		defer func() {
			<-approvedReadSlots
			if recover() != nil {
				done <- response{err: ErrApprovedStorageUnavailable}
			}
		}()
		err := r.inspectVMRestore(probeCtx, &result, destination, passphrase)
		done <- response{review: result, err: err}
	}()
	select {
	case answer := <-done:
		if ctx.Err() != nil {
			return VMRestoreReview{}, ctx.Err()
		}
		if probeCtx.Err() != nil {
			return VMRestoreReview{}, ErrApprovedStorageUnavailable
		}
		return answer.review, answer.err
	case <-probeCtx.Done():
		if ctx.Err() != nil {
			return VMRestoreReview{}, ctx.Err()
		}
		return VMRestoreReview{}, ErrApprovedStorageUnavailable
	}
}

func (r *Runner) inspectVMRestore(ctx context.Context, result *VMRestoreReview, destination, passphrase string) error {
	plan := &result.Plan
	dest, err := r.db.GetStorageDestinationContext(ctx, plan.Job.StorageDestID)
	if errors.Is(err, db.ErrNotFound) {
		result.Chain.Status, result.Chain.Reason = Missing, "required_data_unusable"
		result.Reasons = append(result.Reasons, result.Chain.Reason)
		return nil
	}
	if err != nil {
		return err
	}
	plan.Destination = dest
	adapter, err := storage.NewAdapterWithOptions(dest.Type, dest.Config, storage.Options{
		MetadataProbeContext: ctx, MetadataProbeLimit: maxVMReviewObjects,
	})
	if err != nil {
		return ErrApprovedStorageUnavailable
	}
	defer storage.CloseAdapter(adapter)
	stop := context.AfterFunc(ctx, func() { storage.CloseAdapter(adapter) })
	defer stop()

	work := vmRestoreWork{destination: destination}
	mac := hmac.New(sha256.New, r.serverKey)
	encoder := json.NewEncoder(mac)
	if err := encoder.Encode([]any{"vault-reviewed-vm-source-v1", dest.ID, dest.Type, dest.Config, plan.Job.Compression}); err != nil {
		return err
	}
	xmlData, metadata, files, err := r.captureVMRestoreChain(ctx, result, &work, adapter, passphrase, encoder)
	if err != nil || result.Chain.Status != Confirmed {
		return err
	}
	handler, err := r.openVMRestoreHost()
	if err != nil {
		result.Reasons = append(result.Reasons, "vm_host_unavailable")
		return nil
	}
	defer handler.Close()
	effects, expectation, err := handler.ReviewVMRestore(ctx, plan.Item.ItemName, destination, xmlData, metadata, files, r.serverKey)
	if err != nil {
		result.Reasons = append(result.Reasons, vmReviewReason(err))
		return nil
	}
	if err := encoder.Encode(expectation); err != nil {
		return err
	}
	work.expectation = expectation
	work.stagingAvoid = append(work.stagingAvoid, effects.Destination)
	for _, disk := range effects.Disks {
		work.stagingAvoid = append(work.stagingAvoid, disk.Destination)
	}
	if effects.NVRAM != nil {
		work.stagingAvoid = append(work.stagingAvoid, effects.NVRAM.Destination)
	}
	plan.Work = work
	result.Effects = &effects
	result.SourceDigest = hex.EncodeToString(mac.Sum(nil))
	return nil
}

func (r *Runner) captureVMRestoreChain(ctx context.Context, result *VMRestoreReview, work *vmRestoreWork, adapter storage.Adapter, passphrase string, digest *json.Encoder) ([]byte, []byte, []string, error) {
	var selectedXML, selectedMetadata []byte
	var selectedFiles []string
	plan := &result.Plan
	point := plan.Point
	seen := make(map[int64]bool)
	var selected engine.VMBackupEvidence
	var predecessor string

	defer func() {
		slices.Reverse(plan.Steps)
		slices.Reverse(work.steps)
		slices.Reverse(result.Chain.Steps)
		if result.Chain.Reason != "" {
			result.Reasons = append(result.Reasons, result.Chain.Reason)
		}
	}()
	for len(seen) < maxEvidenceChainDepth {
		if ctx.Err() != nil {
			return nil, nil, nil, ctx.Err()
		}
		seen[point.ID] = true
		member := Membership(point, plan.Item)
		typeEvidence := recordedItemType(point, plan.Item)
		if err := digest.Encode(point); err != nil {
			return nil, nil, nil, err
		}

		entry := RecoveryChainStep{RestorePointID: point.ID, BackupType: IdentifyPoint(point).BackupType,
			Membership: member, ItemTypeEvidence: typeEvidence, DataRequired: member != Missing}
		if entry.DataRequired {
			entry.Artifacts = Unknown
		}
		result.Chain.Steps = append(result.Chain.Steps, entry)
		step := &result.Chain.Steps[len(result.Chain.Steps)-1]
		if member == Unknown || (member == Missing && typeEvidence == Missing) {
			result.Chain.Status, result.Chain.Reason = Unknown, "vm_chain_membership_unknown"
			break
		}
		if member == Confirmed {
			files, err := listVMReviewStep(adapter, point, plan.Item.ItemName)
			if err != nil {
				if errors.Is(err, ErrApprovedStorageUnavailable) {
					return nil, nil, nil, err
				}
				recordVMChainFailure(result, step, err)
				break
			}
			if point.ID == plan.Point.ID {
				mac := hmac.New(sha256.New, r.serverKey)
				dest := plan.Destination
				if err := json.NewEncoder(mac).Encode([]any{"vault-reviewed-vm-source-anchor-v1", dest.ID, dest.Type, dest.Config, plan.Job.Compression, point, files}); err != nil {
					return nil, nil, nil, err
				}
				result.SourceAnchor = hex.EncodeToString(mac.Sum(nil))
			}
			xmlData, metadata, err := readVMReviewSidecars(adapter, point, plan.Item.ItemName, passphrase, files)
			if errors.Is(err, ErrRestorePassphrase) || errors.Is(err, ErrApprovedStorageUnavailable) {
				return nil, nil, nil, err
			}
			if err != nil {
				recordVMChainFailure(result, step, err)
				break
			}
			evidence, err := engine.InspectVMBackup(xmlData, metadata, vmObjectNames(files))
			if err != nil {
				recordVMChainFailure(result, step, err)
				break
			}
			if evidence.Name != plan.Item.ItemName {
				recordVMChainFailure(result, step, engine.ErrVMRestoreIdentityChanged)
				break
			}
			step.ItemTypeEvidence, step.Artifacts = Confirmed, Confirmed
			step.BackupType = "unknown"
			switch evidence.BackupType {
			case "full", "incremental", "differential":
				step.BackupType = evidence.BackupType
			}
			step.VMTargets = make([]string, 0, len(evidence.Disks))
			for _, disk := range evidence.Disks {
				step.VMTargets = append(step.VMTargets, RedactRecoveryLabel(disk.Target))
			}
			if point.ID == plan.Point.ID {
				selected = evidence
				selectedXML, selectedMetadata, selectedFiles = xmlData, metadata, vmObjectNames(files)
				result.ItemTypeEvidence = Confirmed
			} else if evidence.UUID != selected.UUID || evidence.Checkpoint == "" || predecessor != evidence.Checkpoint {
				result.Chain.Status, result.Chain.Reason = Broken, "vm_checkpoint_discontinuity"
				break
			}
			if !containsVMRestoreDisks(evidence, selected) {
				result.Chain.Status, result.Chain.Reason = Broken, "vm_required_disk_layer_missing"
				break
			}
			fingerprint, err := vmSidecarDigest(r.serverKey, xmlData, metadata)
			if err != nil {
				return nil, nil, nil, err
			}
			if err := digest.Encode([]any{files, fingerprint}); err != nil {
				return nil, nil, nil, err
			}
			plan.Steps = append(plan.Steps, ApprovedRestoreStep{Point: point, Objects: files})
			work.steps = append(work.steps, vmReviewedStep{evidence: evidence, digest: fingerprint})
			if evidence.BackupType == "full" && evidence.ParentCheckpoint == "" {
				result.Chain.Status = Confirmed
				return selectedXML, selectedMetadata, selectedFiles, nil
			}
			if (evidence.BackupType != "incremental" && evidence.BackupType != "differential") || evidence.ParentCheckpoint == "" ||
				(evidence.Checkpoint != "" && evidence.ParentCheckpoint == evidence.Checkpoint) {
				result.Chain.Status, result.Chain.Reason = Unknown, "vm_checkpoint_unconfirmed"
				break
			}
			for _, disk := range evidence.Disks {
				if disk.Format != "qcow2" {
					result.Chain.Status, result.Chain.Reason = Broken, "vm_required_disk_layer_missing"
					return nil, nil, nil, nil
				}
			}
			predecessor = evidence.ParentCheckpoint
		}
		parentID := point.ParentRestorePointID
		if parentID == 0 {
			result.Chain.Status, result.Chain.Reason = Missing, "vm_full_backup_missing"
			break
		}
		if seen[parentID] {
			result.Chain.Status, result.Chain.Reason = Broken, "cycle"
			break
		}
		parent, err := r.db.GetRestorePointContext(ctx, parentID)
		if errors.Is(err, db.ErrNotFound) {
			result.Chain.Status, result.Chain.Reason = Missing, "missing_parent"
			result.Chain.MissingRestorePointID = parentID
			break
		}
		if err != nil {
			return nil, nil, nil, err
		}
		if parent.JobID != point.JobID || parent.SourceID != point.SourceID {
			result.Chain.Status, result.Chain.Reason = Broken, "foreign_parent"
			break
		}
		point = parent
	}
	if result.Chain.Reason == "" {
		result.Chain.Reason = "depth_limit"
	}
	return nil, nil, nil, nil
}

func containsVMRestoreDisks(evidence, selected engine.VMBackupEvidence) bool {
	for _, disk := range selected.Disks {
		if !slices.ContainsFunc(evidence.Disks, func(layer engine.VMBackupDisk) bool {
			return layer.Target == disk.Target && layer.Format == disk.Format
		}) {
			return false
		}
	}
	return true
}

func recordVMChainFailure(result *VMRestoreReview, step *RecoveryChainStep, err error) {
	status := Unknown
	switch {
	case errors.Is(err, engine.ErrVMBackupArtifactMissing):
		status = Missing
	case errors.Is(err, errVMManifest), errors.Is(err, engine.ErrVMRestoreIdentityChanged):
		status = Broken
	}
	step.Artifacts, result.Chain.Status, result.Chain.Reason = status, status, vmReviewReason(err)
}

var errVMManifest = errors.New("VM backup data cannot be restored from an item manifest")

func vmReviewReason(err error) string {
	switch {
	case errors.Is(err, errVMManifest):
		return "vm_manifest_unsupported"
	case errors.Is(err, engine.ErrVMBackupArtifactMissing):
		return "vm_required_artifact_missing"
	case errors.Is(err, bounded.ErrTooLarge):
		return "vm_sidecar_too_large"
	case errors.Is(err, engine.ErrVMRestoreTargetDisallowed):
		return "vm_target_disallowed"
	case errors.Is(err, engine.ErrVMRestoreTargetCollision):
		return "vm_target_collision"
	case errors.Is(err, engine.ErrVMRestoreIdentityChanged):
		return "vm_identity_mismatch"
	case errors.Is(err, engine.ErrVMRestoreTargetInUse):
		return "vm_target_in_use"
	case errors.Is(err, engine.ErrVMRestoreHostUnavailable):
		return "vm_host_unavailable"
	default:
		return "vm_sidecar_unconfirmed"
	}
}

func listVMReviewStep(adapter storage.Adapter, point db.RestorePoint, name string) ([]storage.FileInfo, error) {
	if _, chunked, err := lookupItemManifest(point, name); chunked || err != nil {
		return nil, errVMManifest
	}
	prefix := filepath.Join(point.StoragePath, name)
	if clean, err := safepath.NormalizeRelative(prefix, false); err != nil || clean != prefix {
		return nil, engine.ErrVMBackupEvidenceUnknown
	}
	files, err := adapter.List(prefix)
	switch {
	case storage.IsNotExist(err):
		return nil, engine.ErrVMBackupArtifactMissing
	case err != nil:
		return nil, ErrApprovedStorageUnavailable
	case len(files) > maxVMReviewObjects:
		return nil, engine.ErrVMBackupEvidenceUnknown
	}

	files, _, err = selectRestoreObjects(files, parseItemChecksums(point.Metadata, name))
	if err != nil {
		return nil, engine.ErrVMBackupEvidenceUnknown
	}
	slices.SortFunc(files, func(a, b storage.FileInfo) int { return strings.Compare(a.Path, b.Path) })
	for _, file := range files {
		if filepath.Dir(file.Path) != prefix {
			return nil, engine.ErrVMBackupEvidenceUnknown
		}
		local := localRestoreName(filepath.Base(file.Path))
		if clean, err := safepath.NormalizeComponent(local); err != nil || clean != local {
			return nil, engine.ErrVMBackupEvidenceUnknown
		}
	}
	return files, nil
}

func readVMReviewSidecars(adapter storage.Adapter, point db.RestorePoint, name, passphrase string, files []storage.FileInfo) ([]byte, []byte, error) {
	checksums := parseItemChecksums(point.Metadata, name)
	byName := make(map[string]storage.FileInfo, len(files))
	for _, file := range files {
		byName[localRestoreName(filepath.Base(file.Path))] = file
	}
	xmlData, err := readVMReviewSidecar(adapter, byName["domain.xml"], "domain.xml", passphrase, checksums)
	if err != nil {
		return nil, nil, err
	}
	metadata, err := readVMReviewSidecar(adapter, byName["vm_meta.json"], "vm_meta.json", passphrase, checksums)
	if err != nil {
		return nil, nil, err
	}
	return xmlData, metadata, nil
}

func readVMReviewSidecar(adapter storage.Adapter, object storage.FileInfo, name, passphrase string, checksums map[string]string) ([]byte, error) {
	if object.Path == "" || object.Size <= 0 {
		return nil, engine.ErrVMBackupArtifactMissing
	}
	if strings.HasSuffix(object.Path, ".age") && passphrase == "" {
		return nil, ErrRestorePassphrase
	}
	raw, err := adapter.Read(object.Path)
	if storage.IsNotExist(err) {
		return nil, engine.ErrVMBackupArtifactMissing
	}
	if err != nil {
		return nil, ErrApprovedStorageUnavailable
	}
	defer raw.Close()
	base := filepath.Base(object.Path)
	hash := sha256.New()
	encoded := &io.LimitedReader{R: raw, N: engine.VMRestoreSidecarLimit*2 + 1}
	plain, closePlain, local, err := OpenStoredStream(io.TeeReader(encoded, hash), base, passphrase)
	if crypto.IsWrongPassphrase(err) {
		return nil, ErrRestorePassphrase
	}
	if err != nil {
		return nil, engine.ErrVMBackupEvidenceUnknown
	}
	defer func() { _ = closePlain() }()
	if local != name {
		return nil, engine.ErrVMBackupEvidenceUnknown
	}
	data, err := bounded.ReadAll(plain, engine.VMRestoreSidecarLimit)
	if err != nil {
		if errors.Is(err, bounded.ErrTooLarge) {
			return nil, err
		}
		return nil, engine.ErrVMBackupEvidenceUnknown
	}
	if encoded.N <= 0 {
		return nil, bounded.ErrTooLarge
	}
	if expected := checksums[base]; expected != "" && expected != hex.EncodeToString(hash.Sum(nil)) {
		return nil, engine.ErrVMBackupEvidenceUnknown
	}
	return data, nil
}

func vmObjectNames(files []storage.FileInfo) []string {
	names := make([]string, 0, len(files))
	for _, file := range files {
		if file.Size > 0 {
			names = append(names, localRestoreName(filepath.Base(file.Path)))
		}
	}
	return names
}

func vmSidecarDigest(key, xmlData, metadata []byte) (string, error) {
	mac := hmac.New(sha256.New, key)
	if err := json.NewEncoder(mac).Encode([]any{"vault-reviewed-vm-sidecars-v1", xmlData, metadata}); err != nil {
		return "", err
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (work vmRestoreWork) restore(ctx context.Context, r *Runner, approved *ApprovedRestore, passphrase string, reporter restoreProgressReporter) error {
	plan := approved.Plan
	stageOverride, _ := r.db.GetSetting("staging_dir_override", docsmeta.DefaultFor("staging_dir_override"))
	stage, cleanup, err := tempdir.CreateRestoreDirOutside(stageOverride, work.stagingAvoid...)
	if err != nil {
		return err
	}
	defer cleanup()

	dirs := make([]string, 0, len(plan.Steps))
	for i, step := range plan.Steps {
		dir := filepath.Join(stage, fmt.Sprintf("step-%d", i))
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
		start, end := i*40/len(plan.Steps), (i+1)*40/len(plan.Steps)
		if err := r.stageRestorePointItemFrom(ctx, plan.Job, plan.Destination, step.Point, plan.Item.ItemName, dir, passphrase, start, end, reporter, step.Objects); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return changedApprovedRestore(err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := work.steps[i].checkStaged(dir, r.serverKey); err != nil {
			return changedApprovedRestore(err)
		}
		dirs = append(dirs, dir)
	}

	source := dirs[len(dirs)-1]
	if len(dirs) > 1 {
		r.reportRestoreProgress(reporter, 40, "Assembling reviewed VM disk layers")
		disks, err := work.stagedChainDisks(dirs)
		if err != nil {
			return err
		}
		assembled := filepath.Join(stage, "assembled")
		if err := os.Mkdir(assembled, 0o700); err != nil {
			return err
		}
		if err := flattenVMChainDisks(ctx, source, assembled, disks); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		source = assembled
	}
	if err := approved.revalidateStaged(ctx); err != nil {
		return err
	}
	handler, err := r.openVMRestoreHost()
	if err != nil {
		return err
	}
	defer handler.Close()

	item := engine.BackupItem{Name: plan.Item.ItemName, Type: "vm", Settings: map[string]any{"restore_destination": work.destination}}
	ctx = engine.WithVMRestoreExpectation(ctx, r.serverKey, work.expectation)
	err = handler.Restore(ctx, item, source, func(name string, percent int, message string) {
		if percent < 0 {
			return
		}
		reporter.ItemName = name
		r.reportRestoreProgress(reporter, 40+percent*60/100, message)
	})
	if errors.Is(err, engine.ErrVMReviewedEffectsChanged) {
		return ErrReviewedRestoreChanged
	}
	return err
}

func (step vmReviewedStep) checkStaged(dir string, key []byte) error {
	xmlData, err := readStagedVMReviewSidecar(dir, "domain.xml")
	if err != nil {
		return err
	}
	metadata, err := readStagedVMReviewSidecar(dir, "vm_meta.json")
	if err != nil {
		return err
	}
	digest, err := vmSidecarDigest(key, xmlData, metadata)
	if err != nil || !hmac.Equal([]byte(digest), []byte(step.digest)) {
		return ErrReviewedRestoreChanged
	}
	for _, disk := range step.evidence.Disks {
		if !isStagedVMFile(dir, disk.BackupFile) || (disk.Format == "qcow2" && !isSelfContainedQCOW2(filepath.Join(dir, disk.BackupFile))) {
			return ErrReviewedRestoreChanged
		}
	}
	if step.evidence.NVRAMFile != "" && !isStagedVMFile(dir, step.evidence.NVRAMFile) {
		return ErrReviewedRestoreChanged
	}
	return nil
}

func readStagedVMReviewSidecar(dir, name string) ([]byte, error) {
	f, err := os.Open(filepath.Join(dir, name)) // #nosec G304 -- fixed names in private restore staging
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return bounded.ReadAll(f, engine.VMRestoreSidecarLimit)
}

func isStagedVMFile(dir, name string) bool {
	info, err := os.Lstat(filepath.Join(dir, name))
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// QCOW2 backups are independent files. For an incremental restore, Vault
// links the reviewed layers itself. Reject external backing or data file
// references before qemu-img or libvirt can open a staged image.
func isSelfContainedQCOW2(path string) bool {
	f, err := os.Open(path) // #nosec G304 -- recorded regular file in private restore staging
	if err != nil {
		return false
	}
	defer f.Close()
	var header [104]byte
	if _, err := io.ReadFull(f, header[:72]); err != nil ||
		string(header[:4]) != "QFI\xfb" || binary.BigEndian.Uint64(header[8:16]) != 0 {
		return false
	}
	switch binary.BigEndian.Uint32(header[4:8]) {
	case 2:
		return true
	case 3:
		if _, err := io.ReadFull(f, header[72:]); err != nil {
			return false
		}
		const corruptOrExternalData = (1 << 1) | (1 << 2)
		return binary.BigEndian.Uint64(header[72:80])&corruptOrExternalData == 0
	default:
		return false
	}
}

func (work vmRestoreWork) stagedChainDisks(dirs []string) ([]vmChainDisk, error) {
	selected := work.steps[len(work.steps)-1].evidence
	disks := make([]vmChainDisk, 0, len(selected.Disks))
	for _, disk := range selected.Disks {
		layers := make([]string, 0, len(dirs))
		for i, step := range work.steps {
			j := slices.IndexFunc(step.evidence.Disks, func(layer engine.VMBackupDisk) bool {
				return layer.Target == disk.Target && layer.Format == "qcow2"
			})
			if j < 0 {
				return nil, ErrReviewedRestoreChanged
			}
			layers = append(layers, filepath.Join(dirs[i], step.evidence.Disks[j].BackupFile))
		}
		disks = append(disks, vmChainDisk{Target: disk.Target, Layers: layers, OutputName: disk.BackupFile})
	}
	return disks, nil
}
