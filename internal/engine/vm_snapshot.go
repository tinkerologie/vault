package engine

import (
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
)

// These helpers are consumed by linux-tagged VM implementations, but this file
// is untagged so shared XML logic can still be exercised from platform-neutral
// tests and linted on the host OS.
var (
	_ = parseDomainDisksWithTargets
	_ = parseDomainDiskInventory
	_ = sameDomainDisks
	_ = buildBackupXML
	_ = buildSnapshotXML
	_ = selectBackupDiskXML
	_ = stripDomainBackingStores
	_ = allDisksQcow2
	_ = describeDomainDisks
	_ = formatSkippedDomainDisks
)

type domainDisk struct {
	Index  int
	Path   string
	Target string
	// Format is the disk image format from the domain XML <driver type=...>
	// (with an extension-based fallback). Unraid disk images are typically
	// named vdisk1.img regardless of format, so the extension alone is not
	// trustworthy.
	Format string
}

// skippedDomainDisk describes a <disk device="disk"> entry the push-backup
// path cannot handle (block-device or network backed), kept so backups can
// report them instead of silently dropping them.
type skippedDomainDisk struct {
	Target     string
	SourceType string
}

// domainDiskInventory is the parsed disk view of a domain: the file-backed
// disks eligible for backup, the disks that had to be skipped, and the NVRAM
// path if any.
type domainDiskInventory struct {
	Disks     []domainDisk
	Skipped   []skippedDomainDisk
	NVRAMPath string
}

type domainXMLDetails struct {
	XMLName xml.Name `xml:"domain"`
	Devices struct {
		Disks []struct {
			Type   string `xml:"type,attr"`
			Device string `xml:"device,attr"`
			Driver struct {
				Type string `xml:"type,attr"`
			} `xml:"driver"`
			Source struct {
				File string `xml:"file,attr"`
				Dev  string `xml:"dev,attr"`
			} `xml:"source"`
			Target struct {
				Dev string `xml:"dev,attr"`
			} `xml:"target"`
		} `xml:"disk"`
	} `xml:"devices"`
	OS struct {
		NVRAMs []struct {
			Path string `xml:",chardata"`
		} `xml:"nvram"`
	} `xml:"os"`
}

type domainSnapshotXML struct {
	XMLName     xml.Name             `xml:"domainsnapshot"`
	Name        string               `xml:"name"`
	Description string               `xml:"description"`
	Disks       []domainSnapshotDisk `xml:"disks>disk"`
}

type domainSnapshotDisk struct {
	Name     string               `xml:"name,attr"`
	Snapshot string               `xml:"snapshot,attr"`
	Source   domainSnapshotSource `xml:"source"`
}

type domainSnapshotSource struct {
	File string `xml:"file,attr"`
}

type domainBackupXML struct {
	XMLName     xml.Name           `xml:"domainbackup"`
	Incremental string             `xml:"incremental,omitempty"`
	Disks       []domainBackupDisk `xml:"disks>disk"`
}

type domainBackupDisk struct {
	Name   string              `xml:"name,attr"`
	Backup string              `xml:"backup,attr,omitempty"`
	Type   string              `xml:"type,attr,omitempty"`
	Target *domainBackupTarget `xml:"target,omitempty"`
	Driver *domainBackupDriver `xml:"driver,omitempty"`
}

type domainBackupTarget struct {
	File string `xml:"file,attr"`
}

type domainBackupDriver struct {
	Type string `xml:"type,attr,omitempty"`
}

type vmBackupArtifact struct {
	Disk       domainDisk
	BackupFile string
	TargetPath string
	Format     string
}

func parseDomainDisksWithTargets(xmlDesc string) ([]domainDisk, string, error) {
	inventory, err := parseDomainDiskInventory(xmlDesc)
	if err != nil {
		return nil, "", err
	}
	return inventory.Disks, inventory.NVRAMPath, nil
}

func parseDomainDiskInventory(xmlDesc string) (domainDiskInventory, error) {
	var d domainXMLDetails
	if err := xml.Unmarshal([]byte(xmlDesc), &d); err != nil {
		return domainDiskInventory{}, fmt.Errorf("unmarshalling domain XML: %w", err)
	}

	inventory := domainDiskInventory{
		Disks: make([]domainDisk, 0, len(d.Devices.Disks)),
	}
	for _, disk := range d.Devices.Disks {
		if disk.Device != "disk" {
			continue
		}
		if disk.Source.File == "" {
			sourceType := strings.TrimSpace(disk.Type)
			if sourceType == "" || sourceType == "file" {
				sourceType = "unknown"
			}
			inventory.Skipped = append(inventory.Skipped, skippedDomainDisk{
				Target:     strings.TrimSpace(disk.Target.Dev),
				SourceType: sourceType,
			})
			continue
		}

		format := strings.ToLower(strings.TrimSpace(disk.Driver.Type))
		if format == "" {
			format = backupDriverType(disk.Source.File)
		}

		inventory.Disks = append(inventory.Disks, domainDisk{
			Index:  len(inventory.Disks),
			Path:   disk.Source.File,
			Target: strings.TrimSpace(disk.Target.Dev),
			Format: format,
		})
	}

	if len(d.OS.NVRAMs) > 0 {
		inventory.NVRAMPath = strings.TrimSpace(d.OS.NVRAMs[0].Path)
	}

	return inventory, nil
}

// allDisksQcow2 reports whether every disk is qcow2-formatted, which gates
// libvirt checkpoint/incremental support. Falls back to the path extension
// for callers that did not populate Format.
func allDisksQcow2(disks []domainDisk) bool {
	for _, d := range disks {
		format := d.Format
		if format == "" {
			format = backupDriverType(d.Path)
		}
		if format != "qcow2" {
			return false
		}
	}
	return len(disks) > 0
}

func describeDomainDisks(disks []domainDisk) string {
	if len(disks) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(disks))
	for _, d := range disks {
		parts = append(parts, fmt.Sprintf("%s=%s(%s)", d.Target, d.Path, d.Format))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func formatSkippedDomainDisks(skipped []skippedDomainDisk) string {
	if len(skipped) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(skipped))
	for _, s := range skipped {
		parts = append(parts, fmt.Sprintf("%s(source type %s)", s.Target, s.SourceType))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func sameDomainDisks(left, right []domainDisk) bool {
	if len(left) != len(right) {
		return false
	}

	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}

	return true
}

func buildSnapshotXML(name string, disks []domainDisk) (string, error) {
	snapshot := domainSnapshotXML{
		Name:        "vault-backup-" + name,
		Description: "Vault backup snapshot",
		Disks:       make([]domainSnapshotDisk, 0, len(disks)),
	}

	for _, disk := range disks {
		if disk.Target == "" {
			return "", fmt.Errorf("disk %s has no target device", disk.Path)
		}

		snapshot.Disks = append(snapshot.Disks, domainSnapshotDisk{
			Name:     disk.Target,
			Snapshot: "external",
			Source: domainSnapshotSource{
				File: disk.Path + ".snap",
			},
		})
	}

	xmlBytes, err := xml.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshalling snapshot XML: %w", err)
	}

	return string(xmlBytes), nil
}

func buildBackupXML(destDir string, disks []domainDisk) (string, []vmBackupArtifact, error) {
	return buildBackupXMLWithParent(destDir, disks, "", false)
}

// buildBackupXMLWithParent constructs domainbackup XML for libvirt's push-mode
// backup job. When parentCheckpoint is non-empty, the resulting XML requests
// an incremental backup that only includes blocks dirty since that checkpoint.
// When forceQcow2 is true, all disks are written as qcow2 regardless of source
// format — required for incremental output and recommended for differential.
func buildBackupXMLWithParent(destDir string, disks []domainDisk, parentCheckpoint string, forceQcow2 bool) (string, []vmBackupArtifact, error) {
	backup := domainBackupXML{
		Incremental: parentCheckpoint,
		Disks:       make([]domainBackupDisk, 0, len(disks)),
	}
	artifacts := make([]vmBackupArtifact, 0, len(disks))

	for _, disk := range disks {
		if disk.Target == "" {
			return "", nil, fmt.Errorf("disk %s has no target device", disk.Path)
		}

		driverType := disk.Format
		if driverType == "" {
			driverType = backupDriverType(disk.Path)
		}
		if forceQcow2 {
			driverType = "qcow2"
		}

		// Output filename always uses .qcow2 when we force qcow2 so restore
		// can identify the format from the extension.
		ext := filepath.Ext(disk.Path)
		if forceQcow2 {
			ext = ".qcow2"
		}
		backupFile := fmt.Sprintf("vdisk%d%s", disk.Index, ext)
		targetPath := filepath.Join(destDir, backupFile)

		backup.Disks = append(backup.Disks, domainBackupDisk{
			Name:   disk.Target,
			Backup: "yes",
			Type:   "file",
			Target: &domainBackupTarget{File: targetPath},
			Driver: &domainBackupDriver{Type: driverType},
		})
		artifacts = append(artifacts, vmBackupArtifact{
			Disk:       disk,
			BackupFile: backupFile,
			TargetPath: targetPath,
			Format:     driverType,
		})
	}

	xmlBytes, err := xml.MarshalIndent(backup, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("marshalling backup XML: %w", err)
	}

	return string(xmlBytes), artifacts, nil
}

func backupDriverType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".qcow2", ".qcow":
		return "qcow2"
	default:
		return "raw"
	}
}

func selectBackupDiskXML(state libvirt.DomainState, liveXML, inactiveXML string) string {
	if state == libvirt.DomainShutoff || state == libvirt.DomainShutdown {
		return inactiveXML
	}

	return liveXML
}

func stripDomainBackingStores(xmlDesc string) (string, error) {
	const openTag = "<backingStore"

	var out strings.Builder
	searchFrom := 0
	for {
		start := strings.Index(xmlDesc[searchFrom:], openTag)
		if start == -1 {
			out.WriteString(xmlDesc[searchFrom:])
			return out.String(), nil
		}

		start += searchFrom
		out.WriteString(xmlDesc[searchFrom:start])

		length, err := backingStoreSectionLength(xmlDesc[start:])
		if err != nil {
			return "", err
		}

		searchFrom = start + length
	}
}

func backingStoreSectionLength(xmlFragment string) (int, error) {
	const (
		openTagPrefix = "<backingStore"
		closeTag      = "</backingStore>"
	)

	depth := 0
	position := 0
	for position < len(xmlFragment) {
		nextOpen := strings.Index(xmlFragment[position:], openTagPrefix)
		nextClose := strings.Index(xmlFragment[position:], closeTag)

		switch {
		case nextOpen == -1 && nextClose == -1:
			return 0, fmt.Errorf("unterminated backingStore section")
		case nextOpen != -1 && (nextClose == -1 || nextOpen < nextClose):
			openIndex := position + nextOpen
			endIndex := strings.IndexByte(xmlFragment[openIndex:], '>')
			if endIndex == -1 {
				return 0, fmt.Errorf("unterminated backingStore start tag")
			}
			endIndex += openIndex
			depth++
			position = endIndex + 1
			if isSelfClosingXMLTag(xmlFragment[openIndex : endIndex+1]) {
				depth--
				if depth == 0 {
					return position, nil
				}
			}
		case nextClose != -1:
			if depth == 0 {
				return 0, fmt.Errorf("unexpected backingStore closing tag")
			}
			position += nextClose + len(closeTag)
			depth--
			if depth == 0 {
				return position, nil
			}
		}
	}

	return 0, fmt.Errorf("unterminated backingStore section")
}

func isSelfClosingXMLTag(tag string) bool {
	trimmed := strings.TrimSpace(tag)
	return strings.HasSuffix(trimmed, "/>")
}
