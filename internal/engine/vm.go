//go:build linux

package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
)

// VMHandler implements Handler for libvirt-managed virtual machines.
type VMHandler struct {
	conn *libvirt.Libvirt
}

// NewVMHandler creates a new VMHandler connected to the local QEMU hypervisor.
func NewVMHandler() (*VMHandler, error) {
	uri, err := url.Parse(string(libvirt.QEMUSystem))
	if err != nil {
		return nil, fmt.Errorf("parsing libvirt URI: %w", err)
	}

	conn, err := libvirt.ConnectToURI(uri)
	if err != nil {
		return nil, fmt.Errorf("connecting to libvirt: %w", err)
	}

	return &VMHandler{conn: conn}, nil
}

// Close disconnects from libvirt. Safe to call on a nil receiver.
func (h *VMHandler) Close() error {
	if h == nil || h.conn == nil {
		return nil
	}
	return h.conn.Disconnect()
}

// ListItems enumerates all libvirt domains as BackupItems.
func (h *VMHandler) ListItems() ([]BackupItem, error) {
	domains, _, err := h.conn.ConnectListAllDomains(1, libvirt.ConnectListDomainsActive|libvirt.ConnectListDomainsInactive)
	if err != nil {
		return nil, fmt.Errorf("listing domains: %w", err)
	}

	items := make([]BackupItem, 0, len(domains))
	for _, dom := range domains {
		state, _, _ := h.conn.DomainGetState(dom, 0)

		stateStr := domainStateString(libvirt.DomainState(state))

		// Detect disk format so the job wizard can offer only the backup types
		// the VM actually supports (incremental/differential need qcow2 disks).
		// Read the persistent (inactive) config so the result is stable
		// regardless of running state or in-flight snapshots. Any failure
		// degrades to a conservative "unknown"/not-incremental result rather
		// than failing the whole listing.
		diskFormat, supportsIncremental := "unknown", false
		if xmlDesc, xmlErr := h.conn.DomainGetXMLDesc(dom, libvirt.DomainXMLInactive); xmlErr != nil {
			log.Printf("engine/vm: disk-format detection for %q: reading domain XML: %v", dom.Name, xmlErr)
		} else if disks, _, parseErr := parseDomainDisksWithTargets(xmlDesc); parseErr != nil {
			log.Printf("engine/vm: disk-format detection for %q: parsing disks: %v", dom.Name, parseErr)
		} else {
			diskFormat, supportsIncremental = summariseDiskFormat(disks)
		}

		items = append(items, BackupItem{
			Name: dom.Name,
			Type: "vm",
			Settings: map[string]any{
				"uuid":                 formatDomainUUID(dom.UUID),
				"state":                stateStr,
				"disk_format":          diskFormat,
				"supports_incremental": supportsIncremental,
			},
		})
	}

	return items, nil
}

// Backup performs a backup of a virtual machine to destDir. It honours
// the optional settings:
//   - backup_type: "full" (default), "incremental", or "differential"
//   - parent_checkpoint: name of a previously created libvirt checkpoint to
//     use as the basis for an incremental backup. Ignored for full backups.
//   - backup_run_id: numeric run id used to derive a unique checkpoint name.
//
// On success, BackupResult.Meta carries:
//   - vm_checkpoint: name of the libvirt checkpoint created by this backup
//   - vm_backup_type: the actual type performed (may differ from requested
//     when a fallback to full was needed)
func (h *VMHandler) Backup(ctx context.Context, item BackupItem, destDir string, progress ProgressFunc) (*BackupResult, error) {
	result := &BackupResult{ItemName: item.Name, Meta: map[string]any{}}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("creating dest dir: %w", err)
	}

	// Resolve requested backup type and any parent checkpoint.
	requestedType, _ := item.Settings["backup_type"].(string)
	if requestedType == "" {
		requestedType = "full"
	}
	parentCheckpoint, _ := item.Settings["parent_checkpoint"].(string)

	progress(item.Name, 5, "looking up domain")
	dom, err := h.conn.DomainLookupByName(item.Name)
	if err != nil {
		return nil, fmt.Errorf("looking up domain %s: %w", item.Name, err)
	}

	state, _, err := h.conn.DomainGetState(dom, 0)
	if err != nil {
		return nil, fmt.Errorf("getting domain state: %w", err)
	}
	stateValue := libvirt.DomainState(state)

	progress(item.Name, 10, "loading domain XML")
	inactiveXMLDesc, err := h.conn.DomainGetXMLDesc(dom, libvirt.DomainXMLSecure|libvirt.DomainXMLInactive)
	if err != nil {
		return nil, fmt.Errorf("getting inactive domain XML: %w", err)
	}

	diskXMLFlags := libvirt.DomainXMLSecure
	if libvirtDomainIsShutOff(stateValue) {
		diskXMLFlags |= libvirt.DomainXMLInactive
	}

	liveXMLDesc := inactiveXMLDesc
	if !libvirtDomainIsShutOff(stateValue) {
		liveXMLDesc, err = h.conn.DomainGetXMLDesc(dom, libvirt.DomainXMLSecure)
		if err != nil {
			return nil, fmt.Errorf("getting live domain XML: %w", err)
		}
	}

	diskXMLDesc := selectBackupDiskXML(stateValue, liveXMLDesc, inactiveXMLDesc)

	inventory, err := parseDomainDiskInventory(diskXMLDesc)
	if err != nil {
		return nil, fmt.Errorf("parsing domain XML: %w", err)
	}
	disks, nvramPath := inventory.Disks, inventory.NVRAMPath

	log.Printf("engine/vm: %s (state=%s) disk inventory: %d eligible %s, %d skipped %s",
		item.Name, domainStateString(stateValue), len(disks), describeDomainDisks(disks),
		len(inventory.Skipped), formatSkippedDomainDisks(inventory.Skipped))
	if len(disks) == 0 && len(inventory.Skipped) > 0 {
		// Backing up nothing while disks exist would be a silent data-loss
		// trap: the run would "succeed" with only domain XML and metadata.
		return nil, fmt.Errorf("VM %s has no file-backed disks the backup job can handle; skipped disks: %s", item.Name, formatSkippedDomainDisks(inventory.Skipped))
	}

	cleanDisks, err := h.cleanStaleSnapshots(ctx, item.Name, dom, disks, progress)
	if err != nil {
		return nil, fmt.Errorf("cleaning stale snapshots: %w", err)
	}
	if !sameDomainDisks(disks, cleanDisks) {
		diskXMLDesc, err = h.conn.DomainGetXMLDesc(dom, diskXMLFlags)
		if err != nil {
			return nil, fmt.Errorf("refreshing domain XML after cleanup: %w", err)
		}
		disks = cleanDisks
		_, nvramPath, err = parseDomainDisksWithTargets(diskXMLDesc)
		if err != nil {
			return nil, fmt.Errorf("re-parsing live domain XML after cleanup: %w", err)
		}
	}

	// Save domain XML.
	progress(item.Name, 15, "saving domain XML")
	xmlDesc, err := h.conn.DomainGetXMLDesc(dom, libvirt.DomainXMLSecure|libvirt.DomainXMLInactive)
	if err != nil {
		return nil, fmt.Errorf("getting inactive domain XML: %w", err)
	}
	xmlDesc, err = stripDomainBackingStores(xmlDesc)
	if err != nil {
		return nil, fmt.Errorf("sanitizing domain XML: %w", err)
	}
	xmlPath := filepath.Join(destDir, "domain.xml")
	if err := os.WriteFile(xmlPath, []byte(xmlDesc), 0644); err != nil {
		return nil, fmt.Errorf("writing domain XML: %w", err)
	}
	result.Files = append(result.Files, backupFileInfo(xmlPath))

	progress(item.Name, 18, "saving VM metadata")
	metadataPath, err := writeVMBackupMetadata(destDir, domainStateString(stateValue), item.Settings)
	if err != nil {
		return nil, fmt.Errorf("writing VM metadata: %w", err)
	}
	result.Files = append(result.Files, backupFileInfo(metadataPath))

	// Decide the actual backup type. Incremental/differential requires a
	// parent checkpoint that libvirt still knows about and that disks are
	// qcow2-format. Otherwise fall back to full.
	actualType := requestedType
	if actualType != "full" {
		switch {
		case parentCheckpoint == "":
			log.Printf("engine/vm: no parent checkpoint recorded for VM %s — falling back to full", item.Name)
			actualType = "full"
		case !h.checkpointExists(dom, parentCheckpoint):
			log.Printf("engine/vm: parent checkpoint %q not found for VM %s — falling back to full", parentCheckpoint, item.Name)
			actualType = "full"
			parentCheckpoint = ""
		case !allDisksQcow2(disks):
			log.Printf("engine/vm: VM %s has non-qcow2 disks; libvirt incremental backup unsupported — falling back to full", item.Name)
			actualType = "full"
			parentCheckpoint = ""
		}
	}
	if actualType == "full" {
		parentCheckpoint = ""
	}

	// For libvirt-checkpoint based incremental we ignore the file-mtime gate
	// and let libvirt's persistent dirty bitmap drive the per-disk delta.
	// Differential and incremental are equivalent at the engine layer; the
	// runner controls *which* parent checkpoint they reference.
	copyDisks := disks
	if actualType == "full" {
		// Legacy file-mtime based filtering remains as a no-op for full
		// backups — preserved here for parity with previous behaviour.
		changedSince, hasChangedSince := parseChangedSince(item.Settings)
		if hasChangedSince {
			copyDisks, err = filterChangedDomainDisks(disks, changedSince)
			if err != nil {
				return nil, fmt.Errorf("filtering changed disks: %w", err)
			}
		}
	}

	// Determine backup mode.
	backupMode, _ := item.Settings["backup_mode"].(string)
	if backupMode == "" {
		if stateValue == libvirt.DomainRunning || stateValue == libvirt.DomainPaused {
			backupMode = "snapshot"
		} else {
			backupMode = "cold"
		}
	}

	// Decide whether the output disks must be qcow2. Incremental backup
	// output is always qcow2 (libvirt requirement). Full backups keep the
	// original format unless the source is qcow2 (in which case we keep
	// qcow2 for chain extensibility).
	forceQcow2 := actualType != "full"

	var artifacts []vmBackupArtifact
	checkpointName := ""
	if len(copyDisks) > 0 {
		backupDom, restoreDomainState, err := h.prepareDomainForBackup(ctx, item.Name, dom, stateValue, backupMode, progress)
		if err != nil {
			return nil, err
		}

		artifacts, err = h.backupDisks(ctx, backupDom, item.Name, copyDisks, destDir, parentCheckpoint, forceQcow2, progress, result)
		if err != nil {
			if restoreErr := restoreDomainState(); restoreErr != nil {
				return nil, fmt.Errorf("%v; restoring domain state: %w", err, restoreErr)
			}
			return nil, err
		}

		// Create the checkpoint while the backup domain is still active —
		// libvirt rejects checkpoint creation against an inactive domain.
		// We use backupDom which is the live (paused) handle; the persistent
		// bitmap is recorded against the underlying qcow2 files.
		checkpointName = h.deriveCheckpointName(item)
		if err := h.createBackupCheckpoint(backupDom, checkpointName, parentCheckpoint, copyDisks, progress); err != nil {
			log.Printf("engine/vm: warning: creating checkpoint %q for VM %s: %v", checkpointName, item.Name, err)
			checkpointName = ""
		}

		if err := restoreDomainState(); err != nil {
			return nil, fmt.Errorf("restoring domain state: %w", err)
		}
	} else {
		progress(item.Name, 80, "no VM disks selected for backup")
	}

	// Copy NVRAM file if present. NVRAM is small and not part of incremental
	// chain, so we always copy it for both full and incremental backups so
	// each restore point is self-contained for the firmware variables.
	if nvramPath != "" {
		if _, err := os.Stat(nvramPath); err == nil {
			progress(item.Name, 90, "copying NVRAM")
			nvramDest := filepath.Join(destDir, filepath.Base(nvramPath))
			if err := copyFileWithProgress(ctx, nvramPath, nvramDest, func(copied int64) {
				progress(item.Name, 92, fmt.Sprintf("copying NVRAM: %s", humanizeBytes(float64(copied))))
			}); err != nil {
				return nil, fmt.Errorf("copying NVRAM: %w", err)
			}
			result.Files = append(result.Files, backupFileInfo(nvramDest))
		}
	}

	// Persist chain info into vm_meta.json.
	if err := updateVMBackupMetadata(destDir, func(m *vmBackupMetadata) {
		m.BackupType = actualType
		m.Checkpoint = checkpointName
		m.ParentCheckpoint = parentCheckpoint
		m.Disks = make([]vmDiskMeta, 0, len(artifacts))
		for _, a := range artifacts {
			m.Disks = append(m.Disks, vmDiskMeta{
				Target:     a.Disk.Target,
				BackupFile: a.BackupFile,
				Format:     a.Format,
			})
		}
	}); err != nil {
		log.Printf("engine/vm: warning: updating VM metadata for %s: %v", item.Name, err)
	}

	result.Meta["vm_checkpoint"] = checkpointName
	result.Meta["vm_backup_type"] = actualType
	if parentCheckpoint != "" {
		result.Meta["vm_parent_checkpoint"] = parentCheckpoint
	}

	progress(item.Name, 100, "backup complete")
	result.Success = true
	return result, nil
}

// deriveCheckpointName returns a deterministic, unique checkpoint name based
// on the run id (preferred) or a timestamp fallback.
func (h *VMHandler) deriveCheckpointName(item BackupItem) string {
	if v, ok := item.Settings["backup_run_id"]; ok {
		switch typed := v.(type) {
		case int64:
			if typed > 0 {
				return fmt.Sprintf("vault-run-%d", typed)
			}
		case int:
			if typed > 0 {
				return fmt.Sprintf("vault-run-%d", typed)
			}
		case float64:
			if typed > 0 {
				return fmt.Sprintf("vault-run-%d", int64(typed))
			}
		case string:
			s := strings.TrimSpace(typed)
			if s != "" && s != "0" {
				return fmt.Sprintf("vault-run-%s", s)
			}
		}
	}
	return fmt.Sprintf("vault-%s", time.Now().UTC().Format("20060102-150405"))
}

// checkpointExists returns true if the named libvirt checkpoint is currently
// known to libvirt for the given domain. An RPC error is treated as
// "not found" so callers can fall back to a full backup.
func (h *VMHandler) checkpointExists(dom libvirt.Domain, name string) bool {
	if name == "" {
		return false
	}
	_, err := h.conn.DomainCheckpointLookupByName(dom, name, 0)
	return err == nil
}

// createBackupCheckpoint creates a new libvirt checkpoint after a successful
// backup so it can serve as the basis for the next incremental/differential
// backup.
func (h *VMHandler) createBackupCheckpoint(dom libvirt.Domain, name, parent string, disks []domainDisk, progress ProgressFunc) error {
	if name == "" {
		return nil
	}
	desc := "Vault backup checkpoint"
	if parent != "" {
		desc = "Vault incremental backup checkpoint (parent: " + parent + ")"
	}
	xmlDesc, err := buildCheckpointXML(name, desc, disks)
	if err != nil {
		return err
	}
	progress("", 95, "recording libvirt checkpoint")
	if _, err := h.conn.DomainCheckpointCreateXML(dom, xmlDesc, 0); err != nil {
		return fmt.Errorf("creating checkpoint %s: %w", name, err)
	}
	return nil
}

// DeleteCheckpoint removes a libvirt checkpoint and its dirty bitmaps.
// Used by the runner during retention cleanup so old checkpoints don't
// accumulate in the qcow2 file.
func (h *VMHandler) DeleteCheckpoint(domainName, checkpointName string) error {
	if checkpointName == "" {
		return nil
	}
	dom, err := h.conn.DomainLookupByName(domainName)
	if err != nil {
		if isLibvirtNoDomainError(err) {
			return nil
		}
		return fmt.Errorf("looking up domain %s: %w", domainName, err)
	}
	cp, err := h.conn.DomainCheckpointLookupByName(dom, checkpointName, 0)
	if err != nil {
		// Treat "not found" as success (idempotent delete).
		return nil
	}
	if err := h.conn.DomainCheckpointDelete(cp, 0); err != nil {
		return fmt.Errorf("deleting checkpoint %s: %w", checkpointName, err)
	}
	return nil
}

func (h *VMHandler) backupDisks(ctx context.Context, dom libvirt.Domain, name string, disks []domainDisk, destDir, parentCheckpoint string, forceQcow2 bool, progress ProgressFunc, result *BackupResult) ([]vmBackupArtifact, error) {
	backupXML, artifacts, err := buildBackupXMLWithParent(destDir, disks, parentCheckpoint, forceQcow2)
	if err != nil {
		return nil, fmt.Errorf("building backup XML: %w", err)
	}

	progress(name, 25, "starting libvirt backup job")
	log.Printf("engine/vm: %s starting backup job (parentCheckpoint=%q forceQcow2=%v): %s", name, parentCheckpoint, forceQcow2, backupXML)
	if err := h.conn.DomainBackupBegin(dom, backupXML, nil, 0); err != nil {
		return nil, fmt.Errorf("starting backup job: %w", err)
	}

	if err := h.waitForBackupCompletion(ctx, dom, name, artifacts, progress); err != nil {
		return nil, err
	}

	for _, artifact := range artifacts {
		result.Files = append(result.Files, backupFileInfo(artifact.TargetPath))
	}

	return artifacts, nil
}

// Restore restores a VM from a backup directory.
//
// If item.Settings["restore_destination"] is set, disk images and NVRAM
// are restored under that base directory instead of their original paths,
// and the domain XML is rewritten to reference the new locations.
func (h *VMHandler) Restore(ctx context.Context, item BackupItem, sourceDir string, progress ProgressFunc) error {
	progress(item.Name, 5, "reading domain XML")

	xmlPath := filepath.Join(sourceDir, "domain.xml")
	xmlData, err := os.ReadFile(xmlPath) // #nosec G304 — xmlPath is sourceDir + fixed filename "domain.xml"
	if err != nil {
		return fmt.Errorf("reading domain XML: %w", err)
	}

	startAfterRestore := false
	verifyConfig := vmRestoreVerifyConfig{Mode: vmRestoreVerifyModeRunning}
	metadata, err := readVMRestoreMetadata(sourceDir)
	if err == nil {
		startAfterRestore = metadata.startAfterRestore()
		verifyConfig = metadata.RestoreVerify
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading VM metadata: %w", err)
	}

	restoreDest, _ := item.Settings["restore_destination"].(string)
	if restoreDest != "" {
		normalizedRestoreDest, err := normalizeRestorePath(restoreDest)
		if err != nil {
			return err
		}
		restoreDest = normalizedRestoreDest
	}
	plan, err := buildVMRestorePlan(xmlData, restoreDest)
	if err != nil {
		return fmt.Errorf("building restore plan: %w", err)
	}
	if err := normalizeVMRestorePlan(plan); err != nil {
		return fmt.Errorf("validating restore plan: %w", err)
	}

	if err := h.reconcileExistingDomainForRestore(ctx, item.Name, progress); err != nil {
		return fmt.Errorf("cleaning up existing domain: %w", err)
	}

	// Copy disk files back.
	progress(item.Name, 20, "restoring disk images")
	totalDisks := len(plan.Disks)
	for i, disk := range plan.Disks {
		if err := ctx.Err(); err != nil {
			return err
		}
		pct := 20 + (i*50)/max(totalDisks, 1)
		srcFile := filepath.Join(sourceDir, disk.BackupFile)
		if _, err := os.Stat(srcFile); err != nil {
			continue // skip if backup file doesn't exist
		}

		targetPath, err := normalizeRestorePath(disk.TargetPath)
		if err != nil {
			return fmt.Errorf("validating disk target path %q: %w", disk.TargetPath, err)
		}

		progress(item.Name, pct, fmt.Sprintf("restoring disk %d/%d", i+1, totalDisks))
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return fmt.Errorf("creating dir for disk %s: %w", targetPath, err)
		}
		if err := copyFileWithProgress(ctx, srcFile, targetPath, func(copied int64) {
			progress(item.Name, pct, fmt.Sprintf("restoring disk %d/%d: %s", i+1, totalDisks, humanizeBytes(float64(copied))))
		}); err != nil {
			return fmt.Errorf("restoring disk %s: %w", targetPath, err)
		}
	}

	// Restore NVRAM if present.
	if plan.NVRAMBackupFile != "" {
		nvramSrc := filepath.Join(sourceDir, plan.NVRAMBackupFile)
		if _, err := os.Stat(nvramSrc); err == nil {
			nvramTargetPath, err := normalizeRestorePath(plan.NVRAMTargetPath)
			if err != nil {
				return fmt.Errorf("validating NVRAM target path %q: %w", plan.NVRAMTargetPath, err)
			}

			progress(item.Name, 80, "restoring NVRAM")
			if err := os.MkdirAll(filepath.Dir(nvramTargetPath), 0755); err != nil {
				return fmt.Errorf("creating NVRAM dir: %w", err)
			}
			if err := copyFile(ctx, nvramSrc, nvramTargetPath); err != nil {
				return fmt.Errorf("restoring NVRAM: %w", err)
			}
		}
	}

	// Define domain from (possibly rewritten) XML.
	progress(item.Name, 90, "defining domain")
	dom, err := h.conn.DomainDefineXMLFlags(plan.DomainXML, 0)
	if err != nil {
		return fmt.Errorf("defining domain: %w", err)
	}

	if startAfterRestore {
		startedRestoreDomain := false
		cleanupRestoreStartFailure := func(restoreErr error) error {
			if !startedRestoreDomain {
				return restoreErr
			}

			// Teardown must complete even when the restore was cancelled —
			// use a fresh context, not the (possibly cancelled) run ctx.
			cleanupErr := h.stopStartedRestoreDomain(context.Background(), dom, item.Name)
			if cleanupErr != nil {
				return fmt.Errorf("%w; cleanup started restored VM: %v", restoreErr, cleanupErr)
			}

			return restoreErr
		}

		progress(item.Name, 95, "starting restored VM")
		if err := h.conn.DomainCreate(dom); err != nil {
			return fmt.Errorf("starting restored VM: %w", err)
		}
		startedRestoreDomain = true
		if _, err := h.waitForLibvirtDomainRunning(ctx, dom, item.Name, 2*time.Minute); err != nil {
			return cleanupRestoreStartFailure(fmt.Errorf("verifying restored VM is running: %w", err))
		}
		if err := h.verifyRestoredVMReady(ctx, dom, item.Name, verifyConfig, progress); err != nil {
			return cleanupRestoreStartFailure(fmt.Errorf("verifying restored VM readiness: %w", err))
		}
		progress(item.Name, 99, "verified restored VM running")
	}

	progress(item.Name, 100, "restore complete")
	return nil
}

func (h *VMHandler) reconcileExistingDomainForRestore(ctx context.Context, name string, progress ProgressFunc) error {
	dom, err := h.conn.DomainLookupByName(name)
	if err != nil {
		if isLibvirtNoDomainError(err) {
			return nil
		}
		return fmt.Errorf("looking up existing domain %s: %w", name, err)
	}

	progress(name, 10, "cleaning up existing domain")
	state, _, err := h.conn.DomainGetState(dom, 0)
	if err != nil {
		return fmt.Errorf("getting existing domain state: %w", err)
	}
	stateValue := libvirt.DomainState(state)

	if !libvirtDomainIsShutOff(stateValue) {
		progress(name, 12, "stopping existing domain")
		shutdownErr := h.conn.DomainShutdownFlags(dom, libvirt.DomainShutdownDefault)
		if shutdownErr == nil {
			stateValue, err = h.waitForLibvirtDomainShutOff(ctx, dom, name, vmShutdownTimeout)
			if err != nil {
				return err
			}
		}

		if shutdownErr != nil || !libvirtDomainIsShutOff(stateValue) {
			progress(name, 14, "forcing existing domain stop")
			if err := h.conn.DomainDestroy(dom); err != nil {
				if shutdownErr != nil {
					return fmt.Errorf("stopping existing domain: shutdown failed: %v; destroy failed: %w", shutdownErr, err)
				}
				return fmt.Errorf("forcing existing domain stop: %w", err)
			}

			if _, err := h.waitForLibvirtDomainShutOff(ctx, dom, name, 30*time.Second); err != nil {
				return err
			}
		}
	}

	progress(name, 16, "removing existing domain definition")
	hasManagedSave, err := h.conn.DomainHasManagedSaveImage(dom, 0)
	if err != nil {
		return fmt.Errorf("checking managed save: %w", err)
	}
	if hasManagedSave != 0 {
		if err := h.conn.DomainManagedSaveRemove(dom, 0); err != nil {
			return fmt.Errorf("removing managed save: %w", err)
		}
	}

	if err := h.libvirtUndefineDomain(dom); err != nil {
		return fmt.Errorf("undefining existing domain %s: %w", name, err)
	}

	return nil
}

func (h *VMHandler) stopStartedRestoreDomain(ctx context.Context, dom libvirt.Domain, name string) error {
	shutdownErr := h.conn.DomainShutdownFlags(dom, libvirt.DomainShutdownDefault)
	if shutdownErr == nil {
		if _, err := h.waitForLibvirtDomainShutOff(ctx, dom, name, 30*time.Second); err == nil {
			return nil
		} else {
			shutdownErr = err
		}
	} else if isLibvirtNoDomainError(shutdownErr) {
		return nil
	}

	destroyErr := h.conn.DomainDestroy(dom)
	if destroyErr != nil && !isLibvirtNoDomainError(destroyErr) {
		if shutdownErr != nil {
			return fmt.Errorf("shutdown failed: %v; destroy failed: %w", shutdownErr, destroyErr)
		}
		return fmt.Errorf("destroy failed: %w", destroyErr)
	}

	if _, err := h.waitForLibvirtDomainShutOff(ctx, dom, name, 30*time.Second); err != nil && !isLibvirtNoDomainError(err) {
		if shutdownErr != nil {
			return fmt.Errorf("shutdown failed: %v; post-destroy wait failed: %w", shutdownErr, err)
		}
		return fmt.Errorf("post-destroy wait failed: %w", err)
	}

	return nil
}

func (h *VMHandler) waitForLibvirtDomainShutOff(ctx context.Context, dom libvirt.Domain, name string, timeout time.Duration) (libvirt.DomainState, error) {
	deadline := time.Now().Add(timeout)
	for {
		state, _, err := h.conn.DomainGetState(dom, 0)
		if err != nil {
			return 0, fmt.Errorf("waiting for domain %s to shut off: %w", name, err)
		}
		stateValue := libvirt.DomainState(state)
		if libvirtDomainIsShutOff(stateValue) {
			return stateValue, nil
		}
		if time.Now().After(deadline) {
			return stateValue, fmt.Errorf("waiting for domain %s to shut off: timed out with state %s", name, domainStateString(stateValue))
		}

		if err := sleepCtx(ctx, vmShutdownPollInterval); err != nil {
			return stateValue, err
		}
	}
}

func (h *VMHandler) waitForLibvirtDomainRunning(ctx context.Context, dom libvirt.Domain, name string, timeout time.Duration) (libvirt.DomainState, error) {
	deadline := time.Now().Add(timeout)
	for {
		state, _, err := h.conn.DomainGetState(dom, 0)
		if err != nil {
			return libvirt.DomainNostate, fmt.Errorf("getting domain state: %w", err)
		}

		stateValue := libvirt.DomainState(state)
		if stateValue == libvirt.DomainRunning {
			return stateValue, nil
		}
		if time.Now().After(deadline) {
			return stateValue, fmt.Errorf("waiting for domain %s to reach running state: timed out with state %s", name, domainStateString(stateValue))
		}

		if err := sleepCtx(ctx, vmShutdownPollInterval); err != nil {
			return stateValue, err
		}
	}
}

// sleepCtx waits for d or until ctx is cancelled, returning ctx.Err() on
// cancellation. Replaces bare time.Sleep in the VM poll loops so a cancelled
// run is observed within one poll interval (issue #171).
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func libvirtDomainIsShutOff(state libvirt.DomainState) bool {
	return state == libvirt.DomainShutoff || state == libvirt.DomainShutdown
}

func (h *VMHandler) libvirtUndefineDomain(dom libvirt.Domain) error {
	attempts := []libvirt.DomainUndefineFlagsValues{
		libvirt.DomainUndefineManagedSave | libvirt.DomainUndefineSnapshotsMetadata | libvirt.DomainUndefineCheckpointsMetadata | libvirt.DomainUndefineNvram,
		libvirt.DomainUndefineSnapshotsMetadata | libvirt.DomainUndefineCheckpointsMetadata | libvirt.DomainUndefineNvram,
		libvirt.DomainUndefineSnapshotsMetadata | libvirt.DomainUndefineNvram,
		libvirt.DomainUndefineSnapshotsMetadata | libvirt.DomainUndefineCheckpointsMetadata,
		libvirt.DomainUndefineSnapshotsMetadata,
		libvirt.DomainUndefineNvram,
		0,
	}

	var lastErr error
	for _, flags := range attempts {
		var err error
		if flags == 0 {
			err = h.conn.DomainUndefine(dom)
		} else {
			err = h.conn.DomainUndefineFlags(dom, flags)
		}
		if err == nil {
			return nil
		}
		lastErr = err
	}

	return lastErr
}

func isLibvirtNoDomainError(err error) bool {
	var libvirtErr libvirt.Error
	return errors.As(err, &libvirtErr) && libvirtErr.Code == uint32(libvirt.ErrNoDomain)
}

// domainStateString converts a libvirt domain state to a human-readable string.
func domainStateString(state libvirt.DomainState) string {
	switch state {
	case libvirt.DomainRunning:
		return "running"
	case libvirt.DomainBlocked:
		return "blocked"
	case libvirt.DomainPaused:
		return "paused"
	case libvirt.DomainShutdown:
		return "shutdown"
	case libvirt.DomainShutoff:
		return "shutoff"
	case libvirt.DomainCrashed:
		return "crashed"
	case libvirt.DomainPmsuspended:
		return "pmsuspended"
	default:
		return "unknown"
	}
}

// destroyDomainWithRetry tears down a domain, retrying while the qemu process
// is stuck in uninterruptible IO — after a large backup the process can sit in
// D-state flushing dirty pages to fuse-backed storage, so libvirt's kill wait
// times out with "Failed to terminate process ... with SIGKILL: Device or
// resource busy" even though the process exits once the flush completes.
func (h *VMHandler) destroyDomainWithRetry(ctx context.Context, dom libvirt.Domain, name string) error {
	deadline := time.Now().Add(vmShutdownTimeout)
	for {
		err := h.conn.DomainDestroy(dom)
		if err == nil || isLibvirtNoDomainError(err) {
			return nil
		}

		// The kill may have landed despite the error — check whether the
		// domain is already gone or shut off before retrying.
		state, _, stateErr := h.conn.DomainGetState(dom, 0)
		if stateErr != nil {
			if isLibvirtNoDomainError(stateErr) {
				return nil
			}
		} else if libvirtDomainIsShutOff(libvirt.DomainState(state)) {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("destroying domain %s: giving up after %s: %w", name, vmShutdownTimeout, err)
		}
		log.Printf("engine/vm: destroying domain %s: %v — retrying", name, err)
		if serr := sleepCtx(ctx, vmShutdownPollInterval); serr != nil {
			return fmt.Errorf("destroying domain %s: %w", name, serr)
		}
	}
}

// prepareDomainForBackup normalizes the domain into a libvirt state that can
// run DomainBackupBegin while preserving the caller-visible power state as
// closely as possible.
//
// libvirt backup jobs require an active domain. For cold backups of guests that
// are already shut off, or of running guests that we intentionally stop first,
// we start a temporary paused boot session, run the backup job against that
// paused VM, then tear the session back down. Guests that were already paused
// can use their existing paused state directly without another reboot cycle.
func (h *VMHandler) prepareDomainForBackup(ctx context.Context, name string, dom libvirt.Domain, state libvirt.DomainState, backupMode string, progress ProgressFunc) (libvirt.Domain, func() error, error) {
	if state == libvirt.DomainShutoff || state == libvirt.DomainShutdown {
		progress(name, 20, "starting domain paused for backup")
		backupDom, err := h.conn.DomainCreateWithFlags(dom, uint32(libvirt.DomainStartPaused))
		if err != nil {
			return libvirt.Domain{}, nil, fmt.Errorf("starting domain paused for backup: %w", err)
		}

		return backupDom, func() error {
			// Teardown must complete even after cancellation — fresh context.
			return h.destroyDomainWithRetry(context.Background(), backupDom, name)
		}, nil
	}

	if state == libvirt.DomainPaused && backupMode == "cold" {
		progress(name, 20, "using existing paused domain for cold backup")
		return dom, func() error { return nil }, nil
	}

	if backupMode != "cold" {
		return dom, func() error { return nil }, nil
	}

	progress(name, 20, "shutting down domain for cold backup")
	shutdownErr := h.conn.DomainShutdownFlags(dom, libvirt.DomainShutdownDefault)
	if shutdownErr == nil {
		stateAfterShutdown, err := h.waitForLibvirtDomainShutOff(ctx, dom, name, vmShutdownTimeout)
		if err != nil {
			return libvirt.Domain{}, nil, err
		}
		state = stateAfterShutdown
	}

	if shutdownErr != nil || !libvirtDomainIsShutOff(state) {
		progress(name, 22, "forcing domain stop for cold backup")
		if err := h.destroyDomainWithRetry(ctx, dom, name); err != nil {
			if shutdownErr != nil {
				return libvirt.Domain{}, nil, fmt.Errorf("stopping domain for cold backup: shutdown failed: %v; destroy failed: %w", shutdownErr, err)
			}
			return libvirt.Domain{}, nil, fmt.Errorf("forcing domain stop for cold backup: %w", err)
		}

		if _, err := h.waitForLibvirtDomainShutOff(ctx, dom, name, 30*time.Second); err != nil {
			return libvirt.Domain{}, nil, err
		}
	}

	progress(name, 24, "starting paused backup session")
	backupDom, err := h.conn.DomainCreateWithFlags(dom, uint32(libvirt.DomainStartPaused))
	if err != nil {
		return libvirt.Domain{}, nil, fmt.Errorf("starting paused domain for cold backup: %w", err)
	}

	return backupDom, func() error {
		// Teardown must complete even after cancellation — fresh context.
		if err := h.destroyDomainWithRetry(context.Background(), backupDom, name); err != nil {
			return fmt.Errorf("stopping paused backup session: %w", err)
		}
		if err := h.conn.DomainCreate(dom); err != nil {
			return fmt.Errorf("restarting domain after cold backup: %w", err)
		}
		return nil
	}, nil
}

func (h *VMHandler) waitForBackupCompletion(ctx context.Context, dom libvirt.Domain, name string, artifacts []vmBackupArtifact, progress ProgressFunc) error {
	const backupTimeout = 2 * time.Hour
	// The job can vanish without a completed-stats record (libvirt reports
	// that as a successful query with job type DomainJobNone — see issue
	// #160). Give the artifacts a few polls to appear before concluding the
	// job died without producing them.
	const vanishedGraceAttempts = 5

	vanishedPolls := 0
	deadline := time.Now().Add(backupTimeout)
	for {
		// Cancellation (operator Cancel or stall watchdog) must stop the
		// libvirt push-mode job itself — otherwise the hypervisor keeps
		// writing into a staging dir the runner is about to delete (#171).
		if ctxErr := ctx.Err(); ctxErr != nil {
			log.Printf("engine/vm: backup for %s cancelled — aborting libvirt backup job", name)
			if aerr := h.conn.DomainAbortJob(dom); aerr != nil && !isLibvirtNoDomainError(aerr) {
				log.Printf("engine/vm: aborting backup job for %s: %v", name, aerr)
			}
			return ctxErr
		}
		jobType, params, err := h.conn.DomainGetJobStats(dom, 0)
		if err != nil {
			done, jobErr := h.vanishedBackupJobOutcome(dom, name, artifacts)
			if done {
				if jobErr == nil {
					progress(name, 85, "backup job completed")
				}
				return jobErr
			}
			vanishedPolls++
			if vanishedPolls >= vanishedGraceAttempts {
				return fmt.Errorf("getting backup job stats: %w; %s", err, describeMissingBackupArtifacts(artifacts))
			}
		} else {
			typeValue := libvirt.DomainJobType(jobType)
			switch typeValue {
			case libvirt.DomainJobCompleted, libvirt.DomainJobFailed, libvirt.DomainJobCancelled:
				if jobErr := backupJobError(typeValue, params); jobErr != nil {
					log.Printf("engine/vm: backup job for %s ended with type=%s params=%+v", name, typeValue, params)
					return jobErr
				}
				return nil
			case libvirt.DomainJobNone:
				done, jobErr := h.vanishedBackupJobOutcome(dom, name, artifacts)
				if done {
					if jobErr == nil {
						progress(name, 85, "backup job completed")
					}
					return jobErr
				}
				vanishedPolls++
				if vanishedPolls >= vanishedGraceAttempts {
					return fmt.Errorf("backup job for %s ended without a completion record; %s", name, describeMissingBackupArtifacts(artifacts))
				}
			default:
				vanishedPolls = 0
				progress(name, backupProgressPercent(params), backupProgressMessage(params))
			}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("waiting for backup completion: timed out after %s", backupTimeout)
		}

		_ = sleepCtx(ctx, vmShutdownPollInterval) // cancellation handled at loop top
	}
}

// vanishedBackupJobOutcome consults the completed-job stats once the active
// backup job is gone, falling back to the artifacts on disk when libvirt has
// no completed-job record for the domain.
func (h *VMHandler) vanishedBackupJobOutcome(dom libvirt.Domain, name string, artifacts []vmBackupArtifact) (bool, error) {
	completedType, completedParams, completedErr := h.conn.DomainGetJobStats(dom, libvirt.DomainJobStatsCompleted|libvirt.DomainJobStatsKeepCompleted)
	done, jobErr := resolveVanishedBackupJob(libvirt.DomainJobType(completedType), completedParams, completedErr, backupArtifactsExist(artifacts))
	if done && jobErr != nil {
		log.Printf("engine/vm: backup job for %s failed: completedType=%s completedErr=%v params=%+v", name, libvirt.DomainJobType(completedType), completedErr, completedParams)
	}
	if done && jobErr == nil && (completedErr != nil || libvirt.DomainJobType(completedType) == libvirt.DomainJobNone) {
		log.Printf("engine/vm: backup job for %s finished without a completed-stats record (completedErr=%v); success inferred from artifacts on disk", name, completedErr)
	}
	return done, jobErr
}

func formatDomainUUID(uuid libvirt.UUID) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}

// stripSnapSuffix strips all trailing ".snap" suffixes from a path,
// returning the base image path (e.g. "img.qcow2.snap.snap" -> "img.qcow2").
func stripSnapSuffix(path string) string {
	for strings.HasSuffix(path, ".snap") {
		path = strings.TrimSuffix(path, ".snap")
	}
	return path
}

func (h *VMHandler) getDomainBlockJobInfo(dom libvirt.Domain, target string) (bool, libvirt.DomainBlockJobType, uint64, uint64, error) {
	found, jobType, _, cur, end, err := h.conn.DomainGetBlockJobInfo(dom, target, 0)
	if err != nil {
		return false, 0, 0, 0, fmt.Errorf("getting block job info for %s: %w", target, err)
	}

	return found != 0, libvirt.DomainBlockJobType(jobType), cur, end, nil
}

func domainBlockJobIsReady(cur, end uint64) bool {
	return end == 0 || cur >= end
}

func domainBlockJobIsCommit(jobType libvirt.DomainBlockJobType) bool {
	return jobType == libvirt.DomainBlockJobTypeCommit || jobType == libvirt.DomainBlockJobTypeActiveCommit
}

func (h *VMHandler) waitForDomainBlockJobReady(ctx context.Context, name string, dom libvirt.Domain, target string, timeout time.Duration, progress ProgressFunc) error {
	deadline := time.Now().Add(timeout)
	for {
		found, jobType, cur, end, err := h.getDomainBlockJobInfo(dom, target)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if !domainBlockJobIsCommit(jobType) {
			return fmt.Errorf("disk %s has unsupported block job %s", target, jobType)
		}
		if domainBlockJobIsReady(cur, end) {
			return nil
		}

		progress(name, 13, fmt.Sprintf("waiting for stale block job on %s: %s/%s", target, humanizeBytes(float64(cur)), humanizeBytes(float64(end))))
		if time.Now().After(deadline) {
			return fmt.Errorf("waiting for stale block job on %s: timed out after %s", target, timeout)
		}

		if err := sleepCtx(ctx, vmShutdownPollInterval); err != nil {
			return err
		}
	}
}

func (h *VMHandler) pivotDomainBlockJob(ctx context.Context, name string, dom libvirt.Domain, target string, progress ProgressFunc) error {
	progress(name, 13, fmt.Sprintf("pivoting stale block job on %s", target))
	if err := h.conn.DomainBlockJobAbort(dom, target, libvirt.DomainBlockJobAbortPivot); err != nil {
		return fmt.Errorf("pivoting stale block job on %s: %w", target, err)
	}

	deadline := time.Now().Add(vmShutdownTimeout)
	for {
		found, _, _, _, err := h.getDomainBlockJobInfo(dom, target)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("waiting for pivoted block job on %s to disappear: timed out after %s", target, vmShutdownTimeout)
		}

		if err := sleepCtx(ctx, vmShutdownPollInterval); err != nil {
			return err
		}
	}
}

func (h *VMHandler) resolveStaleSnapshotDisk(ctx context.Context, name string, dom libvirt.Domain, disk domainDisk, progress ProgressFunc) (bool, error) {
	found, jobType, cur, end, err := h.getDomainBlockJobInfo(dom, disk.Target)
	if err != nil {
		return false, err
	}

	if found {
		if !domainBlockJobIsCommit(jobType) {
			return false, fmt.Errorf("disk %s has active block job %s", disk.Target, jobType)
		}
		if !domainBlockJobIsReady(cur, end) {
			if err := h.waitForDomainBlockJobReady(ctx, name, dom, disk.Target, vmShutdownTimeout, progress); err != nil {
				return false, err
			}
		}
		return true, h.pivotDomainBlockJob(ctx, name, dom, disk.Target, progress)
	}

	if _, statErr := os.Stat(disk.Path); statErr != nil {
		if os.IsNotExist(statErr) {
			return false, nil
		}
		return false, fmt.Errorf("stat stale overlay %s: %w", disk.Path, statErr)
	}

	progress(name, 13, fmt.Sprintf("committing stale overlay on %s", disk.Target))
	if err := h.conn.DomainBlockCommit(dom, disk.Target, nil, nil, 0, libvirt.DomainBlockCommitActive|libvirt.DomainBlockCommitDelete); err != nil {
		return false, fmt.Errorf("starting block commit for stale overlay on %s: %w", disk.Target, err)
	}
	if err := h.waitForDomainBlockJobReady(ctx, name, dom, disk.Target, vmShutdownTimeout, progress); err != nil {
		return false, err
	}

	return true, h.pivotDomainBlockJob(ctx, name, dom, disk.Target, progress)
}

// cleanStaleSnapshots detects disk paths that are leftover snapshot overlays
// from a previous failed backup and resolves them back to their base image.
func (h *VMHandler) cleanStaleSnapshots(ctx context.Context, name string, dom libvirt.Domain, disks []domainDisk, progress ProgressFunc) ([]domainDisk, error) {
	hasStale := false
	for _, disk := range disks {
		if strings.HasSuffix(disk.Path, ".snap") {
			hasStale = true
			break
		}
	}
	if !hasStale {
		return disks, nil
	}

	needRedefine := false
	for _, disk := range disks {
		if !strings.HasSuffix(disk.Path, ".snap") {
			continue
		}

		base := stripSnapSuffix(disk.Path)
		log.Printf("engine: detected stale snapshot overlay %s, base image %s", disk.Path, base)
		progress(name, 12, fmt.Sprintf("cleaning stale snapshot: %s", filepath.Base(disk.Path)))

		resolved, err := h.resolveStaleSnapshotDisk(ctx, name, dom, disk, progress)
		if err != nil {
			return nil, fmt.Errorf("resolving stale snapshot disk %s: %w", disk.Target, err)
		}
		if resolved {
			continue
		}

		log.Printf("engine: stale overlay %s not on disk, will redefine XML to use %s", disk.Path, base)
		needRedefine = true

		cur := disk.Path
		for cur != base {
			if err := os.Remove(cur); err == nil {
				log.Printf("engine: removed stale overlay file %s", cur)
			}
			cur = strings.TrimSuffix(cur, ".snap")
		}
	}

	if needRedefine {
		progress(name, 14, "redefining domain XML to remove stale overlays")
		xmlDesc, err := h.conn.DomainGetXMLDesc(dom, libvirt.DomainXMLInactive)
		if err != nil {
			return nil, fmt.Errorf("reading domain XML for redefine: %w", err)
		}

		patchedXML := xmlDesc
		for _, disk := range disks {
			if !strings.HasSuffix(disk.Path, ".snap") {
				continue
			}
			base := stripSnapSuffix(disk.Path)
			cur := disk.Path
			for cur != base {
				patchedXML = strings.ReplaceAll(patchedXML, cur, base)
				cur = strings.TrimSuffix(cur, ".snap")
			}
		}

		if _, err := h.conn.DomainDefineXMLFlags(patchedXML, 0); err != nil {
			return nil, fmt.Errorf("redefining domain with patched XML: %w", err)
		}
		log.Printf("engine: redefined domain %s to remove stale snapshot overlay paths", name)
	}

	refreshedDomain, err := h.conn.DomainLookupByName(name)
	if err != nil {
		return nil, fmt.Errorf("re-reading domain after cleanup: %w", err)
	}

	xmlDesc, err := h.conn.DomainGetXMLDesc(refreshedDomain, libvirt.DomainXMLInactive)
	if err != nil {
		return nil, fmt.Errorf("re-reading domain XML after cleanup: %w", err)
	}
	finalDisks, _, err := parseDomainDisksWithTargets(xmlDesc)
	if err != nil {
		return nil, fmt.Errorf("re-parsing domain XML after cleanup: %w", err)
	}

	for _, disk := range finalDisks {
		if strings.HasSuffix(disk.Path, ".snap") {
			return nil, fmt.Errorf("disk %s still has .snap suffix after cleanup", disk.Path)
		}
	}

	return finalDisks, nil
}
