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
	_ = parseDomainDisks
	_ = parseDomainDisksWithTargets
	_ = domainDiskPaths
	_ = sameDomainDisks
	_ = buildBackupXML
	_ = buildSnapshotXML
	_ = selectBackupDiskXML
	_ = stripDomainBackingStores
)

type domainDisk struct {
	Index  int
	Path   string
	Target string
}

type domainXMLDetails struct {
	XMLName xml.Name `xml:"domain"`
	Devices struct {
		Disks []struct {
			Device string `xml:"device,attr"`
			Source struct {
				File string `xml:"file,attr"`
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
	XMLName xml.Name           `xml:"domainbackup"`
	Disks   []domainBackupDisk `xml:"disks>disk"`
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
}

func parseDomainDisksWithTargets(xmlDesc string) ([]domainDisk, string, error) {
	var d domainXMLDetails
	if err := xml.Unmarshal([]byte(xmlDesc), &d); err != nil {
		return nil, "", fmt.Errorf("unmarshalling domain XML: %w", err)
	}

	disks := make([]domainDisk, 0, len(d.Devices.Disks))
	for _, disk := range d.Devices.Disks {
		if disk.Device != "disk" || disk.Source.File == "" {
			continue
		}

		disks = append(disks, domainDisk{
			Index:  len(disks),
			Path:   disk.Source.File,
			Target: strings.TrimSpace(disk.Target.Dev),
		})
	}

	var nvramPath string
	if len(d.OS.NVRAMs) > 0 {
		nvramPath = strings.TrimSpace(d.OS.NVRAMs[0].Path)
	}

	return disks, nvramPath, nil
}

func parseDomainDisks(xmlDesc string) ([]string, string, error) {
	disks, nvramPath, err := parseDomainDisksWithTargets(xmlDesc)
	if err != nil {
		return nil, "", err
	}

	return domainDiskPaths(disks), nvramPath, nil
}

func domainDiskPaths(disks []domainDisk) []string {
	paths := make([]string, 0, len(disks))
	for _, disk := range disks {
		paths = append(paths, disk.Path)
	}

	return paths
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
	backup := domainBackupXML{
		Disks: make([]domainBackupDisk, 0, len(disks)),
	}
	artifacts := make([]vmBackupArtifact, 0, len(disks))

	for _, disk := range disks {
		if disk.Target == "" {
			return "", nil, fmt.Errorf("disk %s has no target device", disk.Path)
		}

		backupFile := fmt.Sprintf("vdisk%d%s", disk.Index, filepath.Ext(disk.Path))
		targetPath := filepath.Join(destDir, backupFile)
		driverType := backupDriverType(disk.Path)

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
