//go:build linux

package engine

import (
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

		items = append(items, BackupItem{
			Name: dom.Name,
			Type: "vm",
			Settings: map[string]any{
				"uuid":  formatDomainUUID(dom.UUID),
				"state": stateStr,
			},
		})
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

	disks, nvramPath, err := parseDomainDisksWithTargets(diskXMLDesc)
	if err != nil {
		return nil, fmt.Errorf("parsing domain XML: %w", err)
	}

	cleanDisks, err := h.cleanStaleSnapshots(item.Name, dom, disks, progress)
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
		if stateValue == libvirt.DomainRunning || stateValue == libvirt.DomainPaused {
			backupMode = "snapshot"
		} else {
			backupMode = "cold"
		}
	}

	if len(copyDisks) > 0 {
		backupDom, restoreDomainState, err := h.prepareDomainForBackup(item.Name, dom, stateValue, backupMode, progress)
		if err != nil {
			return nil, err
		}

		if err := h.backupDisks(backupDom, item.Name, copyDisks, destDir, progress, result); err != nil {
			if restoreErr := restoreDomainState(); restoreErr != nil {
				return nil, fmt.Errorf("%v; restoring domain state: %w", err, restoreErr)
			}
			return nil, err
		}

		if err := restoreDomainState(); err != nil {
			return nil, fmt.Errorf("restoring domain state: %w", err)
		}
	} else {
		progress(item.Name, 80, "no VM disks selected for backup")
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

func (h *VMHandler) backupDisks(dom libvirt.Domain, name string, disks []domainDisk, destDir string, progress ProgressFunc, result *BackupResult) error {
	backupXML, artifacts, err := buildBackupXML(destDir, disks)
	if err != nil {
		return fmt.Errorf("building backup XML: %w", err)
	}

	progress(name, 25, "starting libvirt backup job")
	if err := h.conn.DomainBackupBegin(dom, backupXML, nil, 0); err != nil {
		return fmt.Errorf("starting backup job: %w", err)
	}

	if err := h.waitForBackupCompletion(dom, name, artifacts, progress); err != nil {
		return err
	}

	for _, artifact := range artifacts {
		result.Files = append(result.Files, backupFileInfo(artifact.TargetPath))
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
		if err := copyFileWithProgress(srcFile, disk.TargetPath, func(copied int64) {
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
	dom, err := h.conn.DomainDefineXMLFlags(plan.DomainXML, 0)
	if err != nil {
		return fmt.Errorf("defining domain: %w", err)
	}
	_ = dom

	progress(item.Name, 100, "restore complete")
	return nil
}

func (h *VMHandler) reconcileExistingDomainForRestore(name string, progress ProgressFunc) error {
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
			stateValue, err = h.waitForLibvirtDomainShutOff(dom, name, vmShutdownTimeout)
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

			if _, err := h.waitForLibvirtDomainShutOff(dom, name, 30*time.Second); err != nil {
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

func (h *VMHandler) waitForLibvirtDomainShutOff(dom libvirt.Domain, name string, timeout time.Duration) (libvirt.DomainState, error) {
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

		time.Sleep(vmShutdownPollInterval)
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

// prepareDomainForBackup normalizes the domain into a libvirt state that can
// run DomainBackupBegin while preserving the caller-visible power state as
// closely as possible.
//
// libvirt backup jobs require an active domain. For cold backups of guests that
// are already shut off, or of running guests that we intentionally stop first,
// we start a temporary paused boot session, run the backup job against that
// paused VM, then tear the session back down. Guests that were already paused
// can use their existing paused state directly without another reboot cycle.
func (h *VMHandler) prepareDomainForBackup(name string, dom libvirt.Domain, state libvirt.DomainState, backupMode string, progress ProgressFunc) (libvirt.Domain, func() error, error) {
	if state == libvirt.DomainShutoff || state == libvirt.DomainShutdown {
		progress(name, 20, "starting domain paused for backup")
		backupDom, err := h.conn.DomainCreateWithFlags(dom, uint32(libvirt.DomainStartPaused))
		if err != nil {
			return libvirt.Domain{}, nil, fmt.Errorf("starting domain paused for backup: %w", err)
		}

		return backupDom, func() error {
			if err := h.conn.DomainDestroy(backupDom); err != nil && !isLibvirtNoDomainError(err) {
				return err
			}
			return nil
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
		stateAfterShutdown, err := h.waitForLibvirtDomainShutOff(dom, name, vmShutdownTimeout)
		if err != nil {
			return libvirt.Domain{}, nil, err
		}
		state = stateAfterShutdown
	}

	if shutdownErr != nil || !libvirtDomainIsShutOff(state) {
		progress(name, 22, "forcing domain stop for cold backup")
		if err := h.conn.DomainDestroy(dom); err != nil {
			if shutdownErr != nil {
				return libvirt.Domain{}, nil, fmt.Errorf("stopping domain for cold backup: shutdown failed: %v; destroy failed: %w", shutdownErr, err)
			}
			return libvirt.Domain{}, nil, fmt.Errorf("forcing domain stop for cold backup: %w", err)
		}

		if _, err := h.waitForLibvirtDomainShutOff(dom, name, 30*time.Second); err != nil {
			return libvirt.Domain{}, nil, err
		}
	}

	progress(name, 24, "starting paused backup session")
	backupDom, err := h.conn.DomainCreateWithFlags(dom, uint32(libvirt.DomainStartPaused))
	if err != nil {
		return libvirt.Domain{}, nil, fmt.Errorf("starting paused domain for cold backup: %w", err)
	}

	return backupDom, func() error {
		if err := h.conn.DomainDestroy(backupDom); err != nil && !isLibvirtNoDomainError(err) {
			return fmt.Errorf("stopping paused backup session: %w", err)
		}
		if err := h.conn.DomainCreate(dom); err != nil {
			return fmt.Errorf("restarting domain after cold backup: %w", err)
		}
		return nil
	}, nil
}

func (h *VMHandler) waitForBackupCompletion(dom libvirt.Domain, name string, artifacts []vmBackupArtifact, progress ProgressFunc) error {
	const backupTimeout = 2 * time.Hour

	deadline := time.Now().Add(backupTimeout)
	for {
		jobType, params, err := h.conn.DomainGetJobStats(dom, 0)
		if err != nil {
			completedType, completedParams, completedErr := h.conn.DomainGetJobStats(dom, libvirt.DomainJobStatsCompleted|libvirt.DomainJobStatsKeepCompleted)
			if completedErr == nil {
				return backupJobError(libvirt.DomainJobType(completedType), completedParams)
			}
			return fmt.Errorf("getting backup job stats: %w", err)
		}

		typeValue := libvirt.DomainJobType(jobType)
		switch typeValue {
		case libvirt.DomainJobCompleted, libvirt.DomainJobFailed, libvirt.DomainJobCancelled:
			return backupJobError(typeValue, params)
		case libvirt.DomainJobNone:
			completedType, completedParams, completedErr := h.conn.DomainGetJobStats(dom, libvirt.DomainJobStatsCompleted|libvirt.DomainJobStatsKeepCompleted)
			if completedErr == nil {
				return backupJobError(libvirt.DomainJobType(completedType), completedParams)
			}
			if backupArtifactsExist(artifacts) {
				progress(name, 85, "backup job completed")
				return nil
			}
		default:
			progress(name, backupProgressPercent(params), backupProgressMessage(params))
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("waiting for backup completion: timed out after %s", backupTimeout)
		}

		time.Sleep(vmShutdownPollInterval)
	}
}

func backupJobError(jobType libvirt.DomainJobType, params []libvirt.TypedParam) error {
	success, ok := typedParamBool(params, "success")
	if ok && success {
		return nil
	}

	errMsg := typedParamString(params, "errmsg", "error")
	if errMsg == "" {
		errMsg = jobType.String()
	}

	switch jobType {
	case libvirt.DomainJobCompleted:
		if ok && !success {
			return fmt.Errorf("backup job failed: %s", errMsg)
		}
		return nil
	case libvirt.DomainJobFailed:
		return fmt.Errorf("backup job failed: %s", errMsg)
	case libvirt.DomainJobCancelled:
		return fmt.Errorf("backup job cancelled: %s", errMsg)
	default:
		return fmt.Errorf("backup job ended unexpectedly: %s", errMsg)
	}
}

func backupArtifactsExist(artifacts []vmBackupArtifact) bool {
	for _, artifact := range artifacts {
		info, err := os.Stat(artifact.TargetPath)
		if err != nil || info.Size() == 0 {
			return false
		}
	}

	return len(artifacts) > 0
}

func backupProgressPercent(params []libvirt.TypedParam) int {
	processed, okProcessed := typedParamUint64(params, "fileprocessed", "diskprocessed", "dataprocessed")
	total, okTotal := typedParamUint64(params, "filetotal", "disktotal", "datatotal")
	if !okProcessed || !okTotal || total == 0 {
		return 50
	}

	percent := int((processed * 100) / total)
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	return 35 + (percent * 50 / 100)
}

func backupProgressMessage(params []libvirt.TypedParam) string {
	processed, okProcessed := typedParamUint64(params, "fileprocessed", "diskprocessed", "dataprocessed")
	total, okTotal := typedParamUint64(params, "filetotal", "disktotal", "datatotal")
	if okProcessed && okTotal && total > 0 {
		return fmt.Sprintf("backup in progress: %d/%d bytes", processed, total)
	}

	return "backup in progress"
}

func typedParamBool(params []libvirt.TypedParam, keys ...string) (bool, bool) {
	for _, param := range params {
		switch normalizeTypedParamField(param.Field) {
		case keys[0]:
			return typedParamValueBool(param.Value), true
		}
		for _, key := range keys[1:] {
			if normalizeTypedParamField(param.Field) == key {
				return typedParamValueBool(param.Value), true
			}
		}
	}

	return false, false
}

func typedParamUint64(params []libvirt.TypedParam, keys ...string) (uint64, bool) {
	for _, param := range params {
		normalized := normalizeTypedParamField(param.Field)
		for _, key := range keys {
			if normalized != key {
				continue
			}

			switch value := param.Value.I.(type) {
			case uint64:
				return value, true
			case int64:
				if value >= 0 {
					return uint64(value), true
				}
			case uint32:
				return uint64(value), true
			case int32:
				if value >= 0 {
					return uint64(value), true
				}
			case int:
				if value >= 0 {
					return uint64(value), true
				}
			case uint:
				return uint64(value), true
			}
		}
	}

	return 0, false
}

func typedParamString(params []libvirt.TypedParam, keys ...string) string {
	for _, param := range params {
		normalized := normalizeTypedParamField(param.Field)
		for _, key := range keys {
			if normalized != key {
				continue
			}

			if value, ok := param.Value.I.(string); ok {
				return value
			}
		}
	}

	return ""
}

func typedParamValueBool(value libvirt.TypedParamValue) bool {
	switch typed := value.I.(type) {
	case bool:
		return typed
	case int32:
		return typed != 0
	case uint32:
		return typed != 0
	case int:
		return typed != 0
	case uint:
		return typed != 0
	default:
		return false
	}
}

func normalizeTypedParamField(field string) string {
	var builder strings.Builder
	builder.Grow(len(field))
	for _, r := range field {
		if r >= 'A' && r <= 'Z' {
			builder.WriteRune(r + ('a' - 'A'))
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	return builder.String()
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

func (h *VMHandler) waitForDomainBlockJobReady(name string, dom libvirt.Domain, target string, timeout time.Duration, progress ProgressFunc) error {
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

		progress(name, 13, fmt.Sprintf("waiting for stale block job on %s: %d/%d bytes", target, cur, end))
		if time.Now().After(deadline) {
			return fmt.Errorf("waiting for stale block job on %s: timed out after %s", target, timeout)
		}

		time.Sleep(vmShutdownPollInterval)
	}
}

func (h *VMHandler) pivotDomainBlockJob(name string, dom libvirt.Domain, target string, progress ProgressFunc) error {
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

		time.Sleep(vmShutdownPollInterval)
	}
}

func (h *VMHandler) resolveStaleSnapshotDisk(name string, dom libvirt.Domain, disk domainDisk, progress ProgressFunc) (bool, error) {
	found, jobType, cur, end, err := h.getDomainBlockJobInfo(dom, disk.Target)
	if err != nil {
		return false, err
	}

	if found {
		if !domainBlockJobIsCommit(jobType) {
			return false, fmt.Errorf("disk %s has active block job %s", disk.Target, jobType)
		}
		if !domainBlockJobIsReady(cur, end) {
			if err := h.waitForDomainBlockJobReady(name, dom, disk.Target, vmShutdownTimeout, progress); err != nil {
				return false, err
			}
		}
		return true, h.pivotDomainBlockJob(name, dom, disk.Target, progress)
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
	if err := h.waitForDomainBlockJobReady(name, dom, disk.Target, vmShutdownTimeout, progress); err != nil {
		return false, err
	}

	return true, h.pivotDomainBlockJob(name, dom, disk.Target, progress)
}

// cleanStaleSnapshots detects disk paths that are leftover snapshot overlays
// from a previous failed backup and resolves them back to their base image.
func (h *VMHandler) cleanStaleSnapshots(name string, dom libvirt.Domain, disks []domainDisk, progress ProgressFunc) ([]domainDisk, error) {
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

		resolved, err := h.resolveStaleSnapshotDisk(name, dom, disk, progress)
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
