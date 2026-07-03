package runner

import "testing"

// TestZFSSnapshotFromRPMeta pins the #180 metadata round-trip: the parent
// snapshot for an incremental/differential ZFS run must resolve from the
// parent restore point's zfs_snapshots map, per item, and must return ""
// (→ engine downgrades to full) for legacy metadata that predates the fix.
func TestZFSSnapshotFromRPMeta(t *testing.T) {
	meta := `{"items":2,"zfs_snapshots":{"tank/appdata":"tank/appdata@vault_backup_20260703_120000","tank/vms":"tank/vms@vault_backup_20260703_120001"}}`
	legacy := `{"items":1,"backup_type":"differential"}`
	cases := []struct {
		name, meta, item, want string
	}{
		{"appdata", meta, "tank/appdata", "tank/appdata@vault_backup_20260703_120000"},
		{"vms", meta, "tank/vms", "tank/vms@vault_backup_20260703_120001"},
		{"unknown-item", meta, "tank/other", ""},
		{"legacy-metadata", legacy, "tank/appdata", ""},
		{"empty-metadata", "", "tank/appdata", ""},
		{"malformed-metadata", "not-json", "tank/appdata", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := zfsSnapshotFromRPMeta(c.meta, c.item); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
