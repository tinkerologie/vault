package db

import (
	"path/filepath"
	"testing"
)

// TestContainerModeDataMigration proves the #261 canonicalisation actually
// rewrites legacy rows at upgrade, and leaves every other value alone. Without
// it, a job holding "all_at_once" would be rejected by the API validator on
// every save and so become impossible to edit — even toggling Enabled sends
// the whole job back through validation.
func TestContainerModeDataMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Write the legacy value directly, as MCP/import/replication could have.
	for _, tc := range []struct{ name, mode string }{
		{"legacy", "all_at_once"},
		{"sequential", "one_by_one"},
		{"batch", "stop_all"},
	} {
		if _, err := d.Exec(
			"INSERT INTO jobs (name, container_mode, backup_type_chain) VALUES (?, ?, 'full')",
			tc.name, tc.mode,
		); err != nil {
			t.Fatalf("seeding %s: %v", tc.name, err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopening runs the migrations, as a daemon upgrade would.
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = d2.Close() }()

	want := map[string]string{
		"legacy":     "stop_all", // converted
		"sequential": "one_by_one",
		"batch":      "stop_all",
	}
	for name, expected := range want {
		var got string
		if err := d2.QueryRow("SELECT container_mode FROM jobs WHERE name = ?", name).Scan(&got); err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if got != expected {
			t.Errorf("job %q container_mode = %q, want %q", name, got, expected)
		}
	}

	// Re-running must be a no-op, not an error: Open runs it on every start.
	if err := d2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	d3, err := Open(path)
	if err != nil {
		t.Fatalf("third open: %v", err)
	}
	defer func() { _ = d3.Close() }()
	var stillBatch string
	if err := d3.QueryRow("SELECT container_mode FROM jobs WHERE name = 'legacy'").Scan(&stillBatch); err != nil {
		t.Fatalf("re-reading legacy: %v", err)
	}
	if stillBatch != "stop_all" {
		t.Errorf("after a second migration pass, container_mode = %q, want stop_all", stillBatch)
	}
}
