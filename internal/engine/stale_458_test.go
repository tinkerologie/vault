package engine

import (
	"path/filepath"
	"testing"
)

func TestLiveInventoryStatus458(t *testing.T) {
	t.Parallel()

	existing := t.TempDir()
	avail := LiveInventory{
		Containers: map[string]bool{"plex": true}, ContainersAvailable: true,
		VMs: map[string]bool{"win11": true}, VMsAvailable: true,
		ZFS: map[string]bool{"tank/data": true}, ZFSAvailable: true,
		// StatExists left nil: the os.Stat default is used.
	}
	var unavailable LiveInventory

	tests := []struct {
		name     string
		inv      LiveInventory
		itemType string
		item     string
		settings map[string]any
		want     StaleStatus
	}{
		{"vm present", avail, "vm", "win11", nil, StatusPresent},
		{"vm missing", avail, "vm", "gone", nil, StatusMissing},
		{"vm engine down", unavailable, "vm", "win11", nil, StatusUnknown},
		{"zfs present", avail, "zfs", "tank/data", nil, StatusPresent},
		{"zfs engine down", unavailable, "zfs", "tank/data", nil, StatusUnknown},
		{"container engine down", unavailable, "container", "plex", nil, StatusUnknown},
		{"folder exists via default stat", avail, "folder", "f", map[string]any{"path": existing}, StatusPresent},
		{"folder gone via default stat", avail, "folder", "f", map[string]any{"path": filepath.Join(existing, "nope")}, StatusMissing},
		{"plugin by explicit plg path", avail, "plugin", "p", map[string]any{"plg_path": filepath.Join(existing, "x.plg")}, StatusMissing},
		{"plugin by id", avail, "plugin", "p", map[string]any{"id": "definitely-not-installed-458.plg"}, StatusMissing},
		{"unknown type", avail, "script", "x", nil, StatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.inv.Status(tt.itemType, tt.item, tt.settings); got != tt.want {
				t.Errorf("Status(%s, %s) = %v, want %v", tt.itemType, tt.item, got, tt.want)
			}
		})
	}

	if statExists("") {
		t.Error(`statExists("") = true`)
	}
}
