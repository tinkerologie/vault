package scheduler

import (
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

func newGateTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func addReplSource(t *testing.T, d *db.DB) {
	t.Helper()
	if _, err := d.CreateReplicationSource(db.ReplicationSource{
		Name:     "src-" + t.Name(),
		Type:     "vault",
		URL:      "http://example:24085",
		Schedule: "0 3 * * *",
		Enabled:  true,
	}); err != nil {
		t.Fatalf("create repl source: %v", err)
	}
}

func TestReplicationEnabledDerive(t *testing.T) {
	d := newGateTestDB(t)
	if replicationEnabled(d) {
		t.Errorf("unset + no sources: want false")
	}
	addReplSource(t, d)
	if !replicationEnabled(d) {
		t.Errorf("unset + sources: want true")
	}
	if err := d.SetSetting("replication_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if replicationEnabled(d) {
		t.Errorf("explicit false: want false")
	}
	if err := d.SetSetting("replication_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if !replicationEnabled(d) {
		t.Errorf("explicit true: want true")
	}
}

func TestLoadReplicationSourcesGated(t *testing.T) {
	d := newGateTestDB(t)
	addReplSource(t, d)
	s := New(d, func(int64) {})
	s.SetReplicationRunner(func(int64) {})

	if err := d.SetSetting("replication_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if err := s.loadReplicationSources(); err != nil {
		t.Fatalf("loadReplicationSources: %v", err)
	}
	if len(s.replEntries) != 0 {
		t.Errorf("disabled: want 0 repl entries, got %d", len(s.replEntries))
	}

	if err := d.SetSetting("replication_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := s.loadReplicationSources(); err != nil {
		t.Fatalf("loadReplicationSources: %v", err)
	}
	if len(s.replEntries) != 1 {
		t.Errorf("enabled: want 1 repl entry, got %d", len(s.replEntries))
	}
}
