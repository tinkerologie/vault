package engine

import "testing"

func TestStaleStatus(t *testing.T) {
	t.Parallel()
	inv := LiveInventory{
		Containers:          map[string]bool{"plex": true},
		VMs:                 map[string]bool{},
		ZFS:                 map[string]bool{"tank/data": true},
		ContainersAvailable: true,
		VMsAvailable:        false, // engine down
		ZFSAvailable:        true,
		StatExists: func(p string) bool {
			return p == "/mnt/user/appdata" || p == "/boot/config/plugins/foo.plg"
		},
	}
	cases := []struct {
		name     string
		itemType string
		item     string
		settings map[string]any
		want     StaleStatus
	}{
		{"container present", "container", "plex", nil, StatusPresent},
		{"container missing", "container", "ghost", nil, StatusMissing},
		{"vm engine down -> unknown", "vm", "anything", nil, StatusUnknown},
		{"zfs present", "zfs", "tank/data", nil, StatusPresent},
		{"zfs missing", "zfs", "tank/gone", nil, StatusMissing},
		{"folder present", "folder", "appdata", map[string]any{"path": "/mnt/user/appdata"}, StatusPresent},
		{"folder missing", "folder", "old", map[string]any{"path": "/mnt/user/old"}, StatusMissing},
		{"folder no path -> unknown", "folder", "x", map[string]any{}, StatusUnknown},
		{"plugin present via id", "plugin", "foo", map[string]any{"id": "foo"}, StatusPresent},
		{"plugin missing", "plugin", "bar", map[string]any{"id": "bar"}, StatusMissing},
		{"plugin present via plg_path", "plugin", "foo", map[string]any{"plg_path": "/boot/config/plugins/foo.plg"}, StatusPresent},
		{"unknown type", "weird", "x", nil, StatusUnknown},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := inv.Status(c.itemType, c.item, c.settings); got != c.want {
				t.Errorf("Status(%s,%s) = %v, want %v", c.itemType, c.item, got, c.want)
			}
		})
	}
}
