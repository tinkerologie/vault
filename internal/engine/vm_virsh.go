//go:build linux && !cgo

package engine

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// VMHandler implements Handler for libvirt-managed virtual machines
// using the virsh command-line tool (no CGO/libvirt C bindings required).
type VMHandler struct{}

// NewVMHandler creates a new VMHandler. It verifies that virsh is available.
func NewVMHandler() (*VMHandler, error) {
	if _, err := exec.LookPath("virsh"); err != nil {
		return nil, fmt.Errorf("virsh not found: %w", err)
	}
	// Verify connectivity.
	if err := virshRun("connect", "qemu:///system"); err != nil {
		// Try a simple list to verify virsh works.
		if err2 := virshRun("list", "--all"); err2 != nil {
			return nil, fmt.Errorf("virsh cannot connect to qemu:///system: %w", err2)
		}
	}
	return &VMHandler{}, nil
}

// ListItems enumerates all libvirt domains using virsh.
func (h *VMHandler) ListItems() ([]BackupItem, error) {
	// Get all domain names.
	out, err := virshOutput("list", "--all", "--name")
	if err != nil {
		return nil, fmt.Errorf("listing domains: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	items := make([]BackupItem, 0, len(lines))
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}

		// Get UUID.
		uuid, _ := virshOutput("domuuid", name)
		uuid = strings.TrimSpace(uuid)

		// Get state.
		stateOut, _ := virshOutput("domstate", name)
		state := strings.TrimSpace(stateOut)

		items = append(items, BackupItem{
			Name: name,
			Type: "vm",
			Settings: map[string]any{
				"uuid":  uuid,
				"state": state,
			},
		})
	}
	return items, nil
}

// Backup performs a backup of a virtual machine to destDir using virsh.
func (h *VMHandler) Backup(item BackupItem, destDir string, progress ProgressFunc) (*BackupResult, error) {
	result := &BackupResult{ItemName: item.Name}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("creating dest dir: %w", err)
	}

	// Get domain state.
	stateOut, err := virshOutput("domstate", item.Name)
	if err != nil {
		return nil, fmt.Errorf("getting domain state: %w", err)
	}
	state := strings.TrimSpace(stateOut)

	// Save domain XML.
	progress(item.Name, 10, "saving domain XML")
	xmlDesc, err := virshOutput("dumpxml", item.Name, "--security-info", "--inactive")
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
		if state == "running" {
			backupMode = "snapshot"
		} else {
			backupMode = "cold"
		}
	}

	switch backupMode {
	case "snapshot":
		if err := h.backupSnapshot(item.Name, disks, copyDisks, destDir, progress, result); err != nil {
			return nil, err
		}
	case "cold":
		if err := h.backupCold(item.Name, state, copyDisks, destDir, progress, result); err != nil {
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

// backupSnapshot performs a live snapshot-based backup using virsh.
func (h *VMHandler) backupSnapshot(name string, disks []domainDisk, copyDisks []domainDisk, destDir string, progress ProgressFunc, result *BackupResult) error {
	// Clean up stale snapshot overlays from a previous failed backup.
	// If a prior run crashed after creating the snapshot but before
	// blockcommit ran, the domain XML now points at .snap overlays.
	// We blockcommit each stale overlay back into the base image so
	// that diskPaths points at real data files again.
	cleanDisks, err := h.cleanStaleSnapshots(name, disks, progress)
	if err != nil {
		// If any disk path ends in .snap, we cannot safely proceed — creating
		// a snapshot would produce a .snap.snap double overlay.
		for _, disk := range disks {
			if strings.HasSuffix(disk.Path, ".snap") {
				return fmt.Errorf("stale snapshot cleanup failed and disk paths contain .snap overlays: %w", err)
			}
		}
		progress(name, 15, fmt.Sprintf("warning: stale snapshot cleanup: %v", err))
		cleanDisks = disks
	}

	// If cleanup changed the disk paths (stale overlays were resolved),
	// re-save domain.xml so a future restore uses the correct base paths.
	if len(cleanDisks) > 0 && !sameDomainDisks(cleanDisks, disks) {
		xmlDesc, xmlErr := virshOutput("dumpxml", name, "--security-info", "--inactive")
		if xmlErr == nil {
			xmlDesc, xmlErr = stripDomainBackingStores(xmlDesc)
		}
		if xmlErr == nil {
			xmlPath := filepath.Join(destDir, "domain.xml")
			if writeErr := os.WriteFile(xmlPath, []byte(xmlDesc), 0644); writeErr != nil {
				log.Printf("engine: warning: failed to re-save domain.xml after cleanup: %v", writeErr)
			}
		}
	}
	disks = cleanDisks

	progress(name, 20, "creating external snapshot")

	// Build snapshot XML.
	snapshotXML, err := buildSnapshotXML(name, disks)
	if err != nil {
		return fmt.Errorf("building snapshot XML: %w", err)
	}

	// Write snapshot XML to temp file.
	tmpFile, err := os.CreateTemp("", "vault-snapshot-*.xml")
	if err != nil {
		return fmt.Errorf("creating snapshot XML temp file: %w", err)
	}
	if _, err := tmpFile.WriteString(snapshotXML); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return fmt.Errorf("writing snapshot XML: %w", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	// Create snapshot.
	if err := virshRun("snapshot-create", name, tmpFile.Name(),
		"--disk-only", "--atomic", "--no-metadata"); err != nil {
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

	// Blockcommit snapshot overlays back.
	progress(name, 75, "committing snapshot changes")
	for _, disk := range disks {
		snapshotOverlay := disk.Path + ".snap"
		if err := virshRun("blockcommit", name, disk.Target,
			"--active", "--delete", "--wait", "--verbose"); err != nil {
			progress(name, 80, fmt.Sprintf("warning: blockcommit for %s: %v", filepath.Base(disk.Path), err))
		}
		if err := os.Remove(snapshotOverlay); err != nil && !os.IsNotExist(err) {
			progress(name, 80, fmt.Sprintf("warning: failed to remove snapshot overlay %s: %v", snapshotOverlay, err))
		}
	}

	return nil
}

// backupCold performs a cold (shutdown) backup using virsh.
func (h *VMHandler) backupCold(name, state string, disks []domainDisk, destDir string, progress ProgressFunc, result *BackupResult) error {
	wasRunning := state == "running" || state == "paused"

	if wasRunning {
		progress(name, 15, "shutting down domain")
		if err := virshRun("shutdown", name); err != nil {
			return fmt.Errorf("shutting down domain: %w", err)
		}

		// Wait for domain to shut down (up to 5 minutes).
		progress(name, 20, "waiting for shutdown")
		deadline := time.Now().Add(vmShutdownTimeout)
		for time.Now().Before(deadline) {
			stateOut, _ := virshOutput("domstate", name)
			if strings.TrimSpace(stateOut) == "shut off" {
				break
			}
			time.Sleep(vmShutdownPollInterval)
		}

		stateOut, _ := virshOutput("domstate", name)
		if strings.TrimSpace(stateOut) != "shut off" {
			progress(name, 25, "forcing domain stop")
			if err := virshRun("destroy", name); err != nil {
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
		if err := virshRun("start", name); err != nil {
			return fmt.Errorf("starting domain: %w", err)
		}
	}

	return nil
}

// Restore restores a VM from a backup directory using virsh.
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
			continue
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

	// Restore NVRAM.
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

	// Define domain from XML.
	progress(item.Name, 90, "defining domain")
	tmpXML, err := os.CreateTemp("", "vault-domain-*.xml")
	if err != nil {
		return fmt.Errorf("creating temp XML file: %w", err)
	}
	if _, err := tmpXML.WriteString(plan.DomainXML); err != nil {
		tmpXML.Close()
		os.Remove(tmpXML.Name())
		return fmt.Errorf("writing domain XML: %w", err)
	}
	tmpXML.Close()
	defer os.Remove(tmpXML.Name())

	if err := virshRun("define", tmpXML.Name()); err != nil {
		return fmt.Errorf("defining domain: %w", err)
	}

	progress(item.Name, 100, "restore complete")
	return nil
}

func (h *VMHandler) reconcileExistingDomainForRestore(name string, progress ProgressFunc) error {
	exists, err := virshDomainExists(name)
	if err != nil {
		return fmt.Errorf("checking existing domain: %w", err)
	}
	if !exists {
		return nil
	}

	progress(name, 10, "cleaning up existing domain")
	state, err := virshDomainState(name)
	if err != nil {
		return fmt.Errorf("getting existing domain state: %w", err)
	}

	if !virshDomainIsShutOff(state) {
		progress(name, 12, "stopping existing domain")
		shutdownErr := virshRun("shutdown", name)
		if shutdownErr == nil {
			state, err = virshWaitForDomainShutOff(name, vmShutdownTimeout)
			if err != nil {
				return err
			}
		}

		if shutdownErr != nil || !virshDomainIsShutOff(state) {
			progress(name, 14, "forcing existing domain stop")
			if err := virshRun("destroy", name); err != nil {
				if shutdownErr != nil {
					return fmt.Errorf("stopping existing domain: shutdown failed: %v; destroy failed: %w", shutdownErr, err)
				}
				return fmt.Errorf("forcing existing domain stop: %w", err)
			}

			if _, err := virshWaitForDomainShutOff(name, 30*time.Second); err != nil {
				return err
			}
		}
	}

	progress(name, 16, "removing existing domain definition")
	if err := virshRemoveManagedSave(name); err != nil {
		return fmt.Errorf("removing managed save: %w", err)
	}
	if err := virshUndefineDomain(name); err != nil {
		return err
	}

	return nil
}

// virshRun executes a virsh command and returns an error if it fails.
func virshRun(args ...string) error {
	cmd := exec.Command("virsh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("virsh %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}

// virshOutput executes a virsh command and returns its stdout.
func virshOutput(args ...string) (string, error) {
	cmd := exec.Command("virsh", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("virsh %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func virshDomainExists(name string) (bool, error) {
	out, err := virshOutput("list", "--all", "--name")
	if err != nil {
		return false, fmt.Errorf("listing domains: %w", err)
	}

	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}

	return false, nil
}

func virshDomainState(name string) (string, error) {
	out, err := virshOutput("domstate", name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func virshWaitForDomainShutOff(name string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		state, err := virshDomainState(name)
		if err != nil {
			return "", fmt.Errorf("waiting for domain %s to shut off: %w", name, err)
		}
		if virshDomainIsShutOff(state) {
			return state, nil
		}
		if time.Now().After(deadline) {
			return state, fmt.Errorf("waiting for domain %s to shut off: timed out with state %q", name, state)
		}

		time.Sleep(vmShutdownPollInterval)
	}
}

func virshDomainIsShutOff(state string) bool {
	switch strings.TrimSpace(state) {
	case "shut off", "shutoff":
		return true
	default:
		return false
	}
}

func virshRemoveManagedSave(name string) error {
	if err := virshRun("managedsave-remove", name); err != nil {
		errText := err.Error()
		if strings.Contains(errText, "has no managed save image") ||
			strings.Contains(errText, "no managed save image") ||
			strings.Contains(errText, "managed save image is not present") {
			return nil
		}
		return err
	}

	return nil
}

func virshUndefineDomain(name string) error {
	attempts := [][]string{
		{"undefine", name, "--managed-save", "--snapshots-metadata", "--checkpoints-metadata", "--nvram"},
		{"undefine", name, "--snapshots-metadata", "--checkpoints-metadata", "--nvram"},
		{"undefine", name, "--snapshots-metadata", "--nvram"},
		{"undefine", name, "--snapshots-metadata", "--checkpoints-metadata"},
		{"undefine", name, "--snapshots-metadata"},
		{"undefine", name, "--nvram"},
		{"undefine", name},
	}

	var lastErr error
	for _, args := range attempts {
		if err := virshRun(args...); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	return fmt.Errorf("undefining existing domain %s: %w", name, lastErr)
}

// stripSnapSuffix strips all trailing ".snap" suffixes from a path,
// returning the base image path (e.g. "img.qcow2.snap.snap" → "img.qcow2").
func stripSnapSuffix(path string) string {
	for strings.HasSuffix(path, ".snap") {
		path = strings.TrimSuffix(path, ".snap")
	}
	return path
}

// cleanStaleSnapshots detects disk paths that are leftover snapshot overlays
// (ending in ".snap") from a previous failed backup and resolves them back
// to the base image. Handles chains of any depth (e.g. .snap.snap.snap).
//
// Strategy:
//  1. If the overlay file exists on disk, try blockcommit to merge it back.
//  2. If the overlay is missing or blockcommit fails, rewrite the domain XML
//     to point directly at the base image and remove stale overlay files.
func (h *VMHandler) cleanStaleSnapshots(name string, disks []domainDisk, progress ProgressFunc) ([]domainDisk, error) {
	// Quick check: any stale overlays at all?
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

		// If the active overlay file exists on disk, try blockcommit first.
		if _, statErr := os.Stat(disk.Path); statErr == nil {
			if err := virshRun("blockcommit", name, disk.Target,
				"--active", "--delete", "--wait", "--verbose"); err != nil {
				log.Printf("engine: blockcommit failed for %s: %v, will redefine XML", disk.Path, err)
				needRedefine = true
			} else {
				// Blockcommit succeeded. Remove overlay if --delete didn't.
				if err := os.Remove(disk.Path); err != nil && !os.IsNotExist(err) {
					log.Printf("engine: warning: failed to remove overlay %s: %v", disk.Path, err)
				}
				continue
			}
		} else {
			log.Printf("engine: stale overlay %s not on disk, will redefine XML to use %s", disk.Path, base)
			needRedefine = true
		}

		// Clean up any intermediate overlay files that may be lying around.
		cur := disk.Path
		for cur != base {
			if err := os.Remove(cur); err == nil {
				log.Printf("engine: removed stale overlay file %s", cur)
			}
			cur = strings.TrimSuffix(cur, ".snap")
		}
	}

	// If any disk needed XML rewrite (overlay missing or blockcommit failed),
	// dump the domain XML, patch the disk source paths, and redefine.
	if needRedefine {
		progress(name, 14, "redefining domain XML to remove stale overlays")
		xmlDesc, err := virshOutput("dumpxml", name, "--inactive")
		if err != nil {
			return nil, fmt.Errorf("reading domain XML for redefine: %w", err)
		}

		// Replace each stale overlay path (at any depth) with the base in the XML.
		patchedXML := xmlDesc
		for _, disk := range disks {
			if !strings.HasSuffix(disk.Path, ".snap") {
				continue
			}
			base := stripSnapSuffix(disk.Path)
			// Replace from longest to shortest to avoid partial matches.
			// Walking from the full path down strips each layer.
			cur := disk.Path
			for cur != base {
				patchedXML = strings.ReplaceAll(patchedXML, cur, base)
				cur = strings.TrimSuffix(cur, ".snap")
			}
		}

		// Write patched XML and redefine the domain.
		tmpFile, err := os.CreateTemp("", "vault-redefine-*.xml")
		if err != nil {
			return nil, fmt.Errorf("creating temp XML for redefine: %w", err)
		}
		if _, err := tmpFile.WriteString(patchedXML); err != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return nil, fmt.Errorf("writing patched XML: %w", err)
		}
		tmpFile.Close()
		defer os.Remove(tmpFile.Name())

		if err := virshRun("define", tmpFile.Name()); err != nil {
			return nil, fmt.Errorf("redefining domain with patched XML: %w", err)
		}
		log.Printf("engine: redefined domain %s to remove stale snapshot overlay paths", name)
	}

	// Re-read domain XML to confirm the current paths.
	xmlDesc, err := virshOutput("dumpxml", name, "--inactive")
	if err != nil {
		return nil, fmt.Errorf("re-reading domain XML after cleanup: %w", err)
	}
	finalDisks, _, err := parseDomainDisksWithTargets(xmlDesc)
	if err != nil {
		return nil, fmt.Errorf("re-parsing domain XML after cleanup: %w", err)
	}

	// Verify no paths still have .snap suffixes.
	for _, disk := range finalDisks {
		if strings.HasSuffix(disk.Path, ".snap") {
			return nil, fmt.Errorf("disk %s still has .snap suffix after cleanup", disk.Path)
		}
	}

	return finalDisks, nil
}
