//go:build linux

package engine

import (
	"encoding/xml"
	"fmt"
)

// domainCheckpointXML is the libvirt checkpoint definition used to record a
// dirty-bitmap point on each disk. Subsequent incremental backups can then
// reference this checkpoint via <incremental> in the backup job XML so libvirt
// emits only the blocks dirty since that point.
type domainCheckpointXML struct {
	XMLName     xml.Name               `xml:"domaincheckpoint"`
	Name        string                 `xml:"name"`
	Description string                 `xml:"description,omitempty"`
	Disks       []domainCheckpointDisk `xml:"disks>disk"`
}

type domainCheckpointDisk struct {
	Name       string `xml:"name,attr"`
	Checkpoint string `xml:"checkpoint,attr,omitempty"` // "bitmap" to track changes
}

// buildCheckpointXML builds an XML body suitable for DomainCheckpointCreateXML.
// disks should be the same disks selected for the corresponding backup so the
// dirty bitmap is created on each backed-up disk.
func buildCheckpointXML(name, description string, disks []domainDisk) (string, error) {
	if name == "" {
		return "", fmt.Errorf("checkpoint name required")
	}
	cp := domainCheckpointXML{
		Name:        name,
		Description: description,
		Disks:       make([]domainCheckpointDisk, 0, len(disks)),
	}
	for _, d := range disks {
		if d.Target == "" {
			continue
		}
		cp.Disks = append(cp.Disks, domainCheckpointDisk{
			Name:       d.Target,
			Checkpoint: "bitmap",
		})
	}
	out, err := xml.MarshalIndent(cp, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshalling checkpoint XML: %w", err)
	}
	return string(out), nil
}
