package engine

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// These helpers are consumed by linux-tagged VM implementations, but this file
// is untagged so shared XML logic can still be exercised from platform-neutral
// tests and linted on the host OS.
var (
	_ = parseDomainDisks
	_ = parseDomainDisksWithTargets
	_ = domainDiskPaths
	_ = sameDomainDisks
	_ = buildSnapshotXML
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
