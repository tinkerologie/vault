package diagnostics

import (
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
)

// TestCollector458_TableFailuresBecomeEntries hides every table the collector
// reads so each section records its failure as an entry instead of aborting
// the bundle.
func TestCollector458_TableFailuresBecomeEntries(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	for _, tbl := range []string{"storage_destinations", "jobs", "job_runs", "activity_log", "replication_sources", "verify_runs", "settings"} {
		if _, err := d.Exec(`ALTER TABLE ` + tbl + ` RENAME TO ` + tbl + `_hidden`); err != nil {
			t.Fatalf("rename %s: %v", tbl, err)
		}
	}
	c := NewCollector(d, nil, "v458", nil)
	c.SetDedupStatsFunc(func(db.StorageDestination) (int64, int64, int64, int64, int64, time.Time, int64, error) {
		return 0, 0, 0, 0, 0, time.Time{}, 0, nil
	})
	bundle, err := c.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var msgs []string
	for _, e := range bundle.Entries {
		msgs = append(msgs, e.Message)
	}
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{
		"Failed to list storage destinations",
		"Failed to list jobs",
		"Failed to list recent runs",
		"Failed to list activity logs",
		"Failed to list replication sources",
		"Failed to list recent verify runs",
		"Failed to re-list destinations for dedup stats",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing entry %q in:\n%s", want, joined)
		}
	}
	// Unreadable settings fall back to defaults.
	if !bundle.Settings.ContainerBackupEnabled || bundle.Settings.EncryptionConfigured {
		t.Errorf("settings fallback = %+v", bundle.Settings)
	}
}

func TestCollector458_SettingsBooleansAndOverrides(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	staging := t.TempDir()
	for k, v := range map[string]string{
		"container_backup_enabled": "off",
		"vm_backup_enabled":        "0",
		"flash_backup_enabled":     "yes",
		"staging_dir_override":     staging,
		"snapshot_path_override":   staging + "/snap.db",
	} {
		if err := d.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	c := NewCollector(d, nil, "v", nil)
	s := c.collectSettings()
	if s.ContainerBackupEnabled || s.VMBackupEnabled || !s.FlashBackupEnabled || s.StagingDirOverride != staging {
		t.Errorf("settings = %+v", s)
	}
	found := false
	for _, du := range c.collectDiskUsage() {
		if du.Path == staging {
			found = true
		}
	}
	if !found {
		t.Errorf("staging dir %s missing from disk usage probes", staging)
	}
}

func TestRedact458_EdgeShapes(t *testing.T) {
	t.Parallel()
	if got := RedactJSON(`[{"password":"p"},[{"token":"t"}],1]`); strings.Contains(got, `"p"`) || strings.Contains(got, `"t"`) {
		t.Errorf("nested slice not redacted: %s", got)
	}
	if got := RedactJSON(`"just a string"`); got != `"just a string"` {
		t.Errorf("scalar JSON = %s", got)
	}
	if got := RedactURL("http://[::1"); got != "http://[::1" {
		t.Errorf("unparsable URL = %q", got)
	}
	if got := RedactURL("https://user:pw@example.com/x"); got == "" {
		t.Errorf("credential URL = %q", got)
	}
}
