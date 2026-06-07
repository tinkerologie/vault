package engine

import (
	"os"
	"path/filepath"
	"strings"
)

// StaleStatus classifies whether a backup job item still exists on the system.
type StaleStatus int

const (
	// StatusUnknown means we could not determine existence (e.g. the relevant
	// engine was unavailable). Callers MUST NOT treat Unknown as stale — this
	// prevents a transient Docker/libvirt outage from flagging every item.
	StatusUnknown StaleStatus = iota
	// StatusPresent means the item currently exists.
	StatusPresent
	// StatusMissing means the item no longer exists and is a remediation candidate.
	StatusMissing
)

// pluginsDirPath is where Unraid stores plugin .plg files.
const pluginsDirPath = "/boot/config/plugins"

// LiveInventory is a one-shot snapshot of what currently exists on the system,
// used to classify job items. Name lookups cover container/vm/zfs; folders and
// plugins are probed on the filesystem via StatExists.
type LiveInventory struct {
	Containers map[string]bool
	VMs        map[string]bool
	ZFS        map[string]bool

	ContainersAvailable bool
	VMsAvailable        bool
	ZFSAvailable        bool

	// StatExists reports whether a filesystem path exists. Injectable for tests;
	// defaults to an os.Stat-based check in GatherInventory.
	StatExists func(path string) bool
}

// statExists is the default existence probe.
func statExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// GatherInventory queries the live system once. Each item type degrades
// independently: if a handler cannot be constructed or ListItems fails, that
// type's *Available flag stays false and its items classify as Unknown (never
// Missing).
func GatherInventory() LiveInventory {
	inv := LiveInventory{
		Containers: map[string]bool{},
		VMs:        map[string]bool{},
		ZFS:        map[string]bool{},
		StatExists: statExists,
	}
	if ch, err := NewContainerHandler(); err == nil {
		if items, err := ch.ListItems(); err == nil {
			for _, it := range items {
				inv.Containers[it.Name] = true
			}
			inv.ContainersAvailable = true
		}
	}
	if vh, err := NewVMHandler(); err == nil {
		if items, err := vh.ListItems(); err == nil {
			for _, it := range items {
				inv.VMs[it.Name] = true
			}
			inv.VMsAvailable = true
		}
	}
	if zh, err := NewZFSHandler(); err == nil {
		if items, err := zh.ListItems(); err == nil {
			for _, it := range items {
				inv.ZFS[it.Name] = true
			}
			inv.ZFSAvailable = true
		}
	}
	return inv
}

// Status classifies a single job item. settings is the item's parsed settings
// map (for folder path / plugin id).
func (inv LiveInventory) Status(itemType, name string, settings map[string]any) StaleStatus {
	stat := inv.StatExists
	if stat == nil {
		stat = statExists
	}
	switch itemType {
	case "container":
		if !inv.ContainersAvailable {
			return StatusUnknown
		}
		return present(inv.Containers[name])
	case "vm":
		if !inv.VMsAvailable {
			return StatusUnknown
		}
		return present(inv.VMs[name])
	case "zfs":
		if !inv.ZFSAvailable {
			return StatusUnknown
		}
		return present(inv.ZFS[name])
	case "folder":
		path, _ := settings["path"].(string)
		if path == "" {
			return StatusUnknown
		}
		return present(stat(path))
	case "plugin":
		plg, _ := settings["plg_path"].(string)
		if plg == "" {
			id, _ := settings["id"].(string)
			if id == "" {
				id = name
			}
			plg = filepath.Join(pluginsDirPath, strings.TrimSuffix(id, ".plg")+".plg")
		}
		return present(stat(plg))
	default:
		return StatusUnknown
	}
}

func present(ok bool) StaleStatus {
	if ok {
		return StatusPresent
	}
	return StatusMissing
}
