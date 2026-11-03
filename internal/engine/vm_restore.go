package engine

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/tinkerologie/vault/internal/safepath"
)

// vmShutdownTimeout and vmShutdownPollInterval are variables, not constants,
// only so the linux VM engine tests can shrink them; production never
// reassigns them.
var (
	vmShutdownTimeout      = 5 * time.Minute
	vmShutdownPollInterval = 2 * time.Second
)

// These helpers are consumed by linux-tagged VM implementations, but this file
// is untagged so shared restore logic can still be exercised from
// platform-neutral tests and linted on the host OS.
var (
	_ = vmShutdownTimeout
	_ = vmShutdownPollInterval
	_ vmRestoreDisk
	_ vmRestorePlan
	_ = buildVMRestorePlan
	_ = vmMetadataFileName
	_ vmBackupMetadata
	_ vmRestoreVerifyConfig
	_ = writeVMBackupMetadata
	_ = readVMRestoreMetadata
	_ = vmBackupMetadata.startAfterRestore
	_ = vmRestoreVerifyConfigFromSettings
	_ = normalizeVMRestoreVerifyConfig
	_ = vmRestoreVerifyTimeout
	_ = pickVMReadyAddressFromInterfaces
)

type vmRestoreDisk struct {
	Index      int
	BackupFile string
	TargetPath string
}

type vmRestorePlan struct {
	DomainXML       string
	Disks           []vmRestoreDisk
	NVRAMBackupFile string
	NVRAMTargetPath string
}

func buildVMRestorePlan(xmlData []byte, restoreDest string) (*vmRestorePlan, error) {
	sanitizedXML, err := stripDomainBackingStores(string(xmlData))
	if err != nil {
		return nil, fmt.Errorf("sanitizing domain XML: %w", err)
	}

	disks, nvramPath, err := parseDomainDisksWithTargets(sanitizedXML)
	if err != nil {
		return nil, fmt.Errorf("parsing domain XML: %w", err)
	}

	plan := &vmRestorePlan{
		DomainXML: sanitizedXML,
		Disks:     make([]vmRestoreDisk, 0, len(disks)),
	}

	if restoreDest != "" {
		if _, err := safepath.CheckCustomRestoreDestination(restoreDest); err != nil {
			return nil, err
		}
		resolvedRestoreDest, err := normalizeRestorePath(restoreDest)
		if err != nil {
			return nil, err
		}

		restoreDest = resolvedRestoreDest
	}

	for _, disk := range disks {
		targetPath := disk.Path
		if restoreDest != "" {
			name, err := normalizeRestoreComponent(filepath.Base(disk.Path))
			if err != nil {
				return nil, fmt.Errorf("disk path %q: %w", disk.Path, err)
			}
			targetPath = filepath.Join(restoreDest, name)
			plan.DomainXML = strings.ReplaceAll(plan.DomainXML, disk.Path, targetPath)
		}

		plan.Disks = append(plan.Disks, vmRestoreDisk{
			Index:      disk.Index,
			BackupFile: fmt.Sprintf("vdisk%d%s", disk.Index, filepath.Ext(disk.Path)),
			TargetPath: targetPath,
		})
	}

	nvramPath = strings.TrimSpace(nvramPath)
	if nvramPath != "" {
		plan.NVRAMBackupFile = filepath.Base(nvramPath)
		plan.NVRAMTargetPath = nvramPath
		if restoreDest != "" {
			name, err := normalizeRestoreComponent(filepath.Base(nvramPath))
			if err != nil {
				return nil, fmt.Errorf("NVRAM path %q: %w", nvramPath, err)
			}
			plan.NVRAMTargetPath = filepath.Join(restoreDest, name)
			plan.DomainXML = strings.ReplaceAll(plan.DomainXML, nvramPath, plan.NVRAMTargetPath)
		}
	}

	return plan, nil
}
