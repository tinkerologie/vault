//go:build linux && cgo

package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"libvirt.org/go/libvirt"
)

// VMHandler implements Handler for libvirt-managed virtual machines.
type VMHandler struct {
	conn *libvirt.Connect
}

// NewVMHandler creates a new VMHandler connected to the local QEMU hypervisor.
func NewVMHandler() (*VMHandler, error) {
	conn, err := libvirt.NewConnect("qemu:///system")
	if err != nil {
		return nil, fmt.Errorf("connecting to libvirt: %w", err)
	}
	return &VMHandler{conn: conn}, nil
}

// ListItems enumerates all libvirt domains as BackupItems.
func (h *VMHandler) ListItems() ([]BackupItem, error) {
	domains, err := h.conn.ListAllDomains(libvirt.CONNECT_LIST_DOMAINS_ACTIVE | libvirt.CONNECT_LIST_DOMAINS_INACTIVE)
	if err != nil {
		return nil, fmt.Errorf("listing domains: %w", err)
	}

	items := make([]BackupItem, 0, len(domains))
	for _, dom := range domains {
		name, _ := dom.GetName()
		uuid, _ := dom.GetUUIDString()
		state, _, _ := dom.GetState()

		stateStr := domainStateString(state)

		items = append(items, BackupItem{
			Name: name,
			Type: "vm",
			Settings: map[string]any{
				"uuid":  uuid,
				"state": stateStr,
			},
		})
		dom.Free()
	}
	return items, nil
}

// Backup performs a backup of a virtual machine to destDir.
func (h *VMHandler) Backup(item BackupItem, destDir string, progress ProgressFunc) (*BackupResult, error) {
	result := &BackupResult{ItemName: item.Name}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("creating dest dir: %w", err)
	}

	progress(item.Name, 5, "looking up domain")
	dom, err := h.conn.LookupDomainByName(item.Name)
	if err != nil {
		return nil, fmt.Errorf("looking up domain %s: %w", item.Name, err)
	}
	defer dom.Free()

	state, _, err := dom.GetState()
	if err != nil {
		return nil, fmt.Errorf("getting domain state: %w", err)
	}

	// Save domain XML.
	progress(item.Name, 10, "saving domain XML")
	xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_SECURE | libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		return nil, fmt.Errorf("getting domain XML: %w", err)
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

	// Parse XML to find disk paths, target devices, and NVRAM.
	disks, nvramPath, err := parseDomainDisksWithTargets(xmlDesc)
	if err != nil {
		return nil, fmt.Errorf("parsing domain XML: %w", err)
	}

	changedSince, hasChangedSince := parseChangedSince(item.Settings)
	copyDisks := disks
	if hasChangedSince {
		copyDisks, err = filterChangedDomainDisks(disks, changedSince)
		if err != nil {
			return nil, fmt.Errorf("filtering changed disks: %w", err)
		}
	}

	// Determine backup mode.
	backupMode, _ := item.Settings["backup_mode"].(string)
	if backupMode == "" {
		if state == libvirt.DOMAIN_RUNNING {
			backupMode = "snapshot"
		} else {
			backupMode = "cold"
		}
	}

	switch backupMode {
	case "snapshot":
		if err := h.backupSnapshot(dom, item.Name, disks, copyDisks, destDir, progress, result); err != nil {
			return nil, err
		}
	case "cold":
		if err := h.backupCold(dom, item.Name, state, copyDisks, destDir, progress, result); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported backup mode: %s", backupMode)
	}

	// Copy NVRAM file if present.
	if nvramPath != "" {
		if _, err := os.Stat(nvramPath); err == nil {
			copyNVRAM := true
			if hasChangedSince {
				copyNVRAM, err = pathChangedSince(nvramPath, changedSince)
				if err != nil {
					return nil, fmt.Errorf("checking NVRAM changes: %w", err)
				}
			}
			if copyNVRAM {
				progress(item.Name, 90, "copying NVRAM")
			}
			if copyNVRAM {
				nvramDest := filepath.Join(destDir, filepath.Base(nvramPath))
				if err := copyFileWithProgress(nvramPath, nvramDest, func(copied int64) {
					progress(item.Name, 92, fmt.Sprintf("copying NVRAM: %d bytes", copied))
				}); err != nil {
					return nil, fmt.Errorf("copying NVRAM: %w", err)
				}
				result.Files = append(result.Files, backupFileInfo(nvramDest))
			}
		}
	}

	progress(item.Name, 100, "backup complete")
	result.Success = true
	return result, nil
}

// backupSnapshot performs a live snapshot-based backup.
func (h *VMHandler) backupSnapshot(dom *libvirt.Domain, name string, snapshotDisks []domainDisk, copyDisks []domainDisk, destDir string, progress ProgressFunc, result *BackupResult) error {
	progress(name, 20, "creating external snapshot")

	// Build snapshot XML for external disks.
	snapshotXML, err := buildSnapshotXML(name, snapshotDisks)
	if err != nil {
		return fmt.Errorf("building snapshot XML: %w", err)
	}

	_, err = dom.CreateSnapshotXML(snapshotXML,
		libvirt.DOMAIN_SNAPSHOT_CREATE_DISK_ONLY|
			libvirt.DOMAIN_SNAPSHOT_CREATE_ATOMIC|
			libvirt.DOMAIN_SNAPSHOT_CREATE_NO_METADATA)
	if err != nil {
		return fmt.Errorf("creating snapshot: %w", err)
	}

	// Copy the backing files (original disk images before snapshot).
	// Use copyOrFlattenDisk to handle qcow2 overlays with backing chains.
	totalDisks := len(copyDisks)
	for i, disk := range copyDisks {
		diskPath := disk.Path
		pct := 30 + (i*40)/max(totalDisks, 1)
		progress(name, pct, fmt.Sprintf("copying disk %d/%d: %s", i+1, totalDisks, filepath.Base(diskPath)))

		destPath := filepath.Join(destDir, fmt.Sprintf("vdisk%d%s", disk.Index, filepath.Ext(diskPath)))
		if err := copyOrFlattenDisk(diskPath, destPath, func(copied int64) {
			progress(name, pct, fmt.Sprintf("copying disk %d/%d: %d bytes", i+1, totalDisks, copied))
		}); err != nil {
			return fmt.Errorf("copying disk %s: %w", diskPath, err)
		}
		result.Files = append(result.Files, backupFileInfo(destPath))
	}

	// Blockcommit snapshot overlays back into base images.
	progress(name, 75, "committing snapshot changes")
	for _, disk := range snapshotDisks {
		snapshotOverlay := disk.Path + ".snap"
		if err := dom.BlockCommit(disk.Target, "", "",
			0, libvirt.DOMAIN_BLOCK_COMMIT_ACTIVE|libvirt.DOMAIN_BLOCK_COMMIT_DELETE); err != nil {
			// Best-effort: log but don't fail.
			progress(name, 80, fmt.Sprintf("warning: blockcommit for %s: %v", filepath.Base(disk.Path), err))
		}
		// Clean up snapshot overlay file.
		os.Remove(snapshotOverlay)
	}

	return nil
}

// backupCold performs a cold (shutdown) backup.
func (h *VMHandler) backupCold(dom *libvirt.Domain, name string, state libvirt.DomainState, disks []domainDisk, destDir string, progress ProgressFunc, result *BackupResult) error {
	wasRunning := state == libvirt.DOMAIN_RUNNING || state == libvirt.DOMAIN_PAUSED

	if wasRunning {
		progress(name, 15, "shutting down domain")
		if err := dom.Shutdown(); err != nil {
			return fmt.Errorf("shutting down domain: %w", err)
		}

		// Wait for domain to shut down (up to 5 minutes).
		progress(name, 20, "waiting for shutdown")
		deadline := time.Now().Add(vmShutdownTimeout)
		for time.Now().Before(deadline) {
			st, _, _ := dom.GetState()
			if st == libvirt.DOMAIN_SHUTOFF {
				break
			}
			time.Sleep(vmShutdownPollInterval)
		}

		st, _, _ := dom.GetState()
		if st != libvirt.DOMAIN_SHUTOFF {
			// Force destroy if graceful shutdown failed.
			progress(name, 25, "forcing domain stop")
			if err := dom.Destroy(); err != nil {
				return fmt.Errorf("force stopping domain: %w", err)
			}
		}
	}

	// Copy disk images. Flatten qcow2 overlays with backing chains
	// so the backup is fully self-contained.
	totalDisks := len(disks)
	for i, disk := range disks {
		diskPath := disk.Path
		pct := 30 + (i*50)/max(totalDisks, 1)
		progress(name, pct, fmt.Sprintf("copying disk %d/%d: %s", i+1, totalDisks, filepath.Base(diskPath)))

		destPath := filepath.Join(destDir, fmt.Sprintf("vdisk%d%s", disk.Index, filepath.Ext(diskPath)))
		if err := copyOrFlattenDisk(diskPath, destPath, func(copied int64) {
			progress(name, pct, fmt.Sprintf("copying disk %d/%d: %d bytes", i+1, totalDisks, copied))
		}); err != nil {
			return fmt.Errorf("copying disk %s: %w", diskPath, err)
		}
		result.Files = append(result.Files, backupFileInfo(destPath))
	}

	// Restart domain if it was running.
	if wasRunning {
		progress(name, 85, "starting domain")
		if err := dom.Create(); err != nil {
			return fmt.Errorf("starting domain: %w", err)
		}
	}

	return nil
}

// Restore restores a VM from a backup directory.
//
// If item.Settings["restore_destination"] is set, disk images and NVRAM
// are restored under that base directory instead of their original paths,
// and the domain XML is rewritten to reference the new locations.
func (h *VMHandler) Restore(item BackupItem, sourceDir string, progress ProgressFunc) error {
	progress(item.Name, 5, "reading domain XML")

	xmlPath := filepath.Join(sourceDir, "domain.xml")
	xmlData, err := os.ReadFile(xmlPath)
	if err != nil {
		return fmt.Errorf("reading domain XML: %w", err)
	}

	restoreDest, _ := item.Settings["restore_destination"].(string)
	plan, err := buildVMRestorePlan(xmlData, restoreDest)
	if err != nil {
		return fmt.Errorf("building restore plan: %w", err)
	}

	if err := h.reconcileExistingDomainForRestore(item.Name, progress); err != nil {
		return fmt.Errorf("cleaning up existing domain: %w", err)
	}

	// Copy disk files back.
	progress(item.Name, 20, "restoring disk images")
	totalDisks := len(plan.Disks)
	for i, disk := range plan.Disks {
		pct := 20 + (i*50)/max(totalDisks, 1)
		srcFile := filepath.Join(sourceDir, disk.BackupFile)
		if _, err := os.Stat(srcFile); err != nil {
			continue // skip if backup file doesn't exist
		}

		progress(item.Name, pct, fmt.Sprintf("restoring disk %d/%d", i+1, totalDisks))
		if err := os.MkdirAll(filepath.Dir(disk.TargetPath), 0755); err != nil {
			return fmt.Errorf("creating dir for disk %s: %w", disk.TargetPath, err)
		}
		if err := copyOrFlattenDisk(srcFile, disk.TargetPath, func(copied int64) {
			progress(item.Name, pct, fmt.Sprintf("restoring disk %d/%d: %d bytes", i+1, totalDisks, copied))
		}); err != nil {
			return fmt.Errorf("restoring disk %s: %w", disk.TargetPath, err)
		}
	}

	// Restore NVRAM if present.
	if plan.NVRAMBackupFile != "" {
		nvramSrc := filepath.Join(sourceDir, plan.NVRAMBackupFile)
		if _, err := os.Stat(nvramSrc); err == nil {
			progress(item.Name, 80, "restoring NVRAM")
			if err := os.MkdirAll(filepath.Dir(plan.NVRAMTargetPath), 0755); err != nil {
				return fmt.Errorf("creating NVRAM dir: %w", err)
			}
			if err := copyFile(nvramSrc, plan.NVRAMTargetPath); err != nil {
				return fmt.Errorf("restoring NVRAM: %w", err)
			}
		}
	}

	// Define domain from (possibly rewritten) XML.
	progress(item.Name, 90, "defining domain")
	dom, err := h.conn.DomainDefineXML(plan.DomainXML)
	if err != nil {
		return fmt.Errorf("defining domain: %w", err)
	}
	dom.Free()

	progress(item.Name, 100, "restore complete")
	return nil
}

func (h *VMHandler) reconcileExistingDomainForRestore(name string, progress ProgressFunc) error {
	dom, err := h.conn.LookupDomainByName(name)
	if err != nil {
		if isLibvirtNoDomainError(err) {
			return nil
		}
		return fmt.Errorf("looking up existing domain %s: %w", name, err)
	}
	defer dom.Free()

	progress(name, 10, "cleaning up existing domain")
	state, _, err := dom.GetState()
	if err != nil {
		return fmt.Errorf("getting existing domain state: %w", err)
	}

	if !libvirtDomainIsShutOff(state) {
		progress(name, 12, "stopping existing domain")
		shutdownErr := dom.Shutdown()
		if shutdownErr == nil {
			state, err = waitForLibvirtDomainShutOff(dom, name, vmShutdownTimeout)
			if err != nil {
				return err
			}
		}

		if shutdownErr != nil || !libvirtDomainIsShutOff(state) {
			progress(name, 14, "forcing existing domain stop")
			if err := dom.Destroy(); err != nil {
				if shutdownErr != nil {
					return fmt.Errorf("stopping existing domain: shutdown failed: %v; destroy failed: %w", shutdownErr, err)
				}
				return fmt.Errorf("forcing existing domain stop: %w", err)
			}

			if _, err := waitForLibvirtDomainShutOff(dom, name, 30*time.Second); err != nil {
				return err
			}
		}
	}

	progress(name, 16, "removing existing domain definition")
	hasManagedSave, err := dom.HasManagedSaveImage(0)
	if err != nil {
		return fmt.Errorf("checking managed save: %w", err)
	}
	if hasManagedSave {
		if err := dom.ManagedSaveRemove(0); err != nil {
			return fmt.Errorf("removing managed save: %w", err)
		}
	}

	if err := libvirtUndefineDomain(dom); err != nil {
		return fmt.Errorf("undefining existing domain %s: %w", name, err)
	}

	return nil
}

func waitForLibvirtDomainShutOff(dom *libvirt.Domain, name string, timeout time.Duration) (libvirt.DomainState, error) {
	deadline := time.Now().Add(timeout)
	for {
		state, _, err := dom.GetState()
		if err != nil {
			return 0, fmt.Errorf("waiting for domain %s to shut off: %w", name, err)
		}
		if libvirtDomainIsShutOff(state) {
			return state, nil
		}
		if time.Now().After(deadline) {
			return state, fmt.Errorf("waiting for domain %s to shut off: timed out with state %s", name, domainStateString(state))
		}

		time.Sleep(vmShutdownPollInterval)
	}
}

func libvirtDomainIsShutOff(state libvirt.DomainState) bool {
	return state == libvirt.DOMAIN_SHUTOFF || state == libvirt.DOMAIN_SHUTDOWN
}

func libvirtUndefineDomain(dom *libvirt.Domain) error {
	attempts := []libvirt.DomainUndefineFlagsValues{
		libvirt.DOMAIN_UNDEFINE_MANAGED_SAVE | libvirt.DOMAIN_UNDEFINE_SNAPSHOTS_METADATA | libvirt.DOMAIN_UNDEFINE_CHECKPOINTS_METADATA | libvirt.DOMAIN_UNDEFINE_NVRAM,
		libvirt.DOMAIN_UNDEFINE_SNAPSHOTS_METADATA | libvirt.DOMAIN_UNDEFINE_CHECKPOINTS_METADATA | libvirt.DOMAIN_UNDEFINE_NVRAM,
		libvirt.DOMAIN_UNDEFINE_SNAPSHOTS_METADATA | libvirt.DOMAIN_UNDEFINE_NVRAM,
		libvirt.DOMAIN_UNDEFINE_SNAPSHOTS_METADATA | libvirt.DOMAIN_UNDEFINE_CHECKPOINTS_METADATA,
		libvirt.DOMAIN_UNDEFINE_SNAPSHOTS_METADATA,
		libvirt.DOMAIN_UNDEFINE_NVRAM,
		0,
	}

	var lastErr error
	for _, flags := range attempts {
		var err error
		if flags == 0 {
			err = dom.Undefine()
		} else {
			err = dom.UndefineFlags(flags)
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
	return errors.As(err, &libvirtErr) && libvirtErr.Code == libvirt.ERR_NO_DOMAIN
}

// domainStateString converts a libvirt domain state to a human-readable string.
func domainStateString(state libvirt.DomainState) string {
	switch state {
	case libvirt.DOMAIN_RUNNING:
		return "running"
	case libvirt.DOMAIN_BLOCKED:
		return "blocked"
	case libvirt.DOMAIN_PAUSED:
		return "paused"
	case libvirt.DOMAIN_SHUTDOWN:
		return "shutdown"
	case libvirt.DOMAIN_SHUTOFF:
		return "shutoff"
	case libvirt.DOMAIN_CRASHED:
		return "crashed"
	case libvirt.DOMAIN_PMSUSPENDED:
		return "pmsuspended"
	default:
		return "unknown"
	}
}
