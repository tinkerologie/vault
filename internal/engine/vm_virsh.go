//go:build linux && !cgo

package engine

import (
	"encoding/xml"
	"fmt"
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
	xmlPath := filepath.Join(destDir, "domain.xml")
	if err := os.WriteFile(xmlPath, []byte(xmlDesc), 0644); err != nil {
		return nil, fmt.Errorf("writing domain XML: %w", err)
	}
	result.Files = append(result.Files, backupFileInfo(xmlPath))

	// Parse XML to find disk paths and NVRAM.
	diskPaths, nvramPath, err := parseDomainDisksVirsh(xmlDesc)
	if err != nil {
		return nil, fmt.Errorf("parsing domain XML: %w", err)
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
		if err := h.backupSnapshot(item.Name, diskPaths, destDir, progress, result); err != nil {
			return nil, err
		}
	case "cold":
		if err := h.backupCold(item.Name, state, diskPaths, destDir, progress, result); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported backup mode: %s", backupMode)
	}

	// Copy NVRAM file if present.
	if nvramPath != "" {
		progress(item.Name, 90, "copying NVRAM")
		if _, err := os.Stat(nvramPath); err == nil {
			nvramDest := filepath.Join(destDir, filepath.Base(nvramPath))
			if err := copyFileWithProgress(nvramPath, nvramDest, func(copied int64) {
				progress(item.Name, 92, fmt.Sprintf("copying NVRAM: %d bytes", copied))
			}); err != nil {
				return nil, fmt.Errorf("copying NVRAM: %w", err)
			}
			result.Files = append(result.Files, backupFileInfo(nvramDest))
		}
	}

	progress(item.Name, 100, "backup complete")
	result.Success = true
	return result, nil
}

// backupSnapshot performs a live snapshot-based backup using virsh.
func (h *VMHandler) backupSnapshot(name string, diskPaths []string, destDir string, progress ProgressFunc, result *BackupResult) error {
	progress(name, 20, "creating external snapshot")

	// Build snapshot XML.
	snapshotXML := buildSnapshotXMLVirsh(name, diskPaths)

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
	totalDisks := len(diskPaths)
	for i, diskPath := range diskPaths {
		pct := 30 + (i*40)/max(totalDisks, 1)
		progress(name, pct, fmt.Sprintf("copying disk %d/%d: %s", i+1, totalDisks, filepath.Base(diskPath)))

		destPath := filepath.Join(destDir, fmt.Sprintf("vdisk%d%s", i, filepath.Ext(diskPath)))
		if err := copyFileWithProgress(diskPath, destPath, func(copied int64) {
			progress(name, pct, fmt.Sprintf("copying disk %d/%d: %d bytes", i+1, totalDisks, copied))
		}); err != nil {
			return fmt.Errorf("copying disk %s: %w", diskPath, err)
		}
		result.Files = append(result.Files, backupFileInfo(destPath))
	}

	// Blockcommit snapshot overlays back.
	progress(name, 75, "committing snapshot changes")
	for i, diskPath := range diskPaths {
		target := fmt.Sprintf("vd%c", 'a'+i)
		snapshotOverlay := diskPath + ".snap"
		if err := virshRun("blockcommit", name, target,
			"--active", "--delete", "--wait", "--verbose"); err != nil {
			progress(name, 80, fmt.Sprintf("warning: blockcommit for %s: %v", filepath.Base(diskPath), err))
		}
		if err := os.Remove(snapshotOverlay); err != nil && !os.IsNotExist(err) {
			progress(name, 80, fmt.Sprintf("warning: failed to remove snapshot overlay %s: %v", snapshotOverlay, err))
		}
	}

	return nil
}

// backupCold performs a cold (shutdown) backup using virsh.
func (h *VMHandler) backupCold(name, state string, diskPaths []string, destDir string, progress ProgressFunc, result *BackupResult) error {
	wasRunning := state == "running" || state == "paused"

	if wasRunning {
		progress(name, 15, "shutting down domain")
		if err := virshRun("shutdown", name); err != nil {
			return fmt.Errorf("shutting down domain: %w", err)
		}

		// Wait for domain to shut down (up to 5 minutes).
		progress(name, 20, "waiting for shutdown")
		deadline := time.Now().Add(5 * time.Minute)
		for time.Now().Before(deadline) {
			stateOut, _ := virshOutput("domstate", name)
			if strings.TrimSpace(stateOut) == "shut off" {
				break
			}
			time.Sleep(2 * time.Second)
		}

		stateOut, _ := virshOutput("domstate", name)
		if strings.TrimSpace(stateOut) != "shut off" {
			progress(name, 25, "forcing domain stop")
			if err := virshRun("destroy", name); err != nil {
				return fmt.Errorf("force stopping domain: %w", err)
			}
		}
	}

	// Copy disk images.
	totalDisks := len(diskPaths)
	for i, diskPath := range diskPaths {
		pct := 30 + (i*50)/max(totalDisks, 1)
		progress(name, pct, fmt.Sprintf("copying disk %d/%d: %s", i+1, totalDisks, filepath.Base(diskPath)))

		destPath := filepath.Join(destDir, fmt.Sprintf("vdisk%d%s", i, filepath.Ext(diskPath)))
		if err := copyFileWithProgress(diskPath, destPath, func(copied int64) {
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

	// Parse the XML to find original disk paths.
	diskPaths, nvramPath, err := parseDomainDisksVirsh(string(xmlData))
	if err != nil {
		return fmt.Errorf("parsing domain XML: %w", err)
	}

	// Copy disk files back.
	progress(item.Name, 20, "restoring disk images")
	totalDisks := len(diskPaths)
	for i, diskPath := range diskPaths {
		pct := 20 + (i*50)/max(totalDisks, 1)
		srcFile := filepath.Join(sourceDir, fmt.Sprintf("vdisk%d%s", i, filepath.Ext(diskPath)))
		if _, err := os.Stat(srcFile); err != nil {
			continue
		}
		progress(item.Name, pct, fmt.Sprintf("restoring disk %d/%d", i+1, totalDisks))
		if err := os.MkdirAll(filepath.Dir(diskPath), 0755); err != nil {
			return fmt.Errorf("creating dir for disk %s: %w", diskPath, err)
		}
		if err := copyFileWithProgress(srcFile, diskPath, func(copied int64) {
			progress(item.Name, pct, fmt.Sprintf("restoring disk %d/%d: %d bytes", i+1, totalDisks, copied))
		}); err != nil {
			return fmt.Errorf("restoring disk %s: %w", diskPath, err)
		}
	}

	// Restore NVRAM.
	if nvramPath != "" {
		nvramSrc := filepath.Join(sourceDir, filepath.Base(nvramPath))
		if _, err := os.Stat(nvramSrc); err == nil {
			progress(item.Name, 80, "restoring NVRAM")
			if err := os.MkdirAll(filepath.Dir(nvramPath), 0755); err != nil {
				return fmt.Errorf("creating NVRAM dir: %w", err)
			}
			if err := copyFile(nvramSrc, nvramPath); err != nil {
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
	if _, err := tmpXML.Write(xmlData); err != nil {
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

// domainXMLVirsh is used for parsing domain XML to extract disk and NVRAM paths.
type domainXMLVirsh struct {
	XMLName xml.Name `xml:"domain"`
	Devices struct {
		Disks []struct {
			Device string `xml:"device,attr"`
			Source struct {
				File string `xml:"file,attr"`
			} `xml:"source"`
		} `xml:"disk"`
	} `xml:"devices"`
	OS struct {
		NVRAMs []struct {
			Path string `xml:",chardata"`
		} `xml:"nvram"`
	} `xml:"os"`
}

// parseDomainDisksVirsh extracts disk image paths and NVRAM path from domain XML.
func parseDomainDisksVirsh(xmlDesc string) (diskPaths []string, nvramPath string, err error) {
	var d domainXMLVirsh
	if err := xml.Unmarshal([]byte(xmlDesc), &d); err != nil {
		return nil, "", fmt.Errorf("unmarshalling domain XML: %w", err)
	}

	for _, disk := range d.Devices.Disks {
		if disk.Device == "disk" && disk.Source.File != "" {
			diskPaths = append(diskPaths, disk.Source.File)
		}
	}

	if len(d.OS.NVRAMs) > 0 {
		nvramPath = strings.TrimSpace(d.OS.NVRAMs[0].Path)
	}

	return diskPaths, nvramPath, nil
}

// buildSnapshotXMLVirsh creates XML for an external disk-only snapshot.
func buildSnapshotXMLVirsh(name string, diskPaths []string) string {
	var disksXML strings.Builder
	for _, dp := range diskPaths {
		snapPath := dp + ".snap"
		disksXML.WriteString(fmt.Sprintf(
			`<disk name="%s" snapshot="external"><source file="%s"/></disk>`,
			dp, snapPath))
	}

	return fmt.Sprintf(`<domainsnapshot>
  <name>vault-backup-%s</name>
  <description>Vault backup snapshot</description>
  <disks>%s</disks>
</domainsnapshot>`, name, disksXML.String())
}
