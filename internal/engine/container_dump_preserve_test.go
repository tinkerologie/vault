package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRestoreDumpName pins the collision-resistant naming: the container name
// is sanitised to a single path component and prefixed onto the dump base name,
// falling back to the bare name when it yields nothing usable.
func TestRestoreDumpName(t *testing.T) {
	cases := []struct {
		name      string
		container string
		dumpBase  string
		want      string
	}{
		{"docker leading slash", "/mariadb", "database.sql", "mariadb-database.sql"},
		{"plain name", "mariadb", "database.sql.gz", "mariadb-database.sql.gz"},
		{"empty name", "", "database.sql", "database.sql"},
		{"root name", "/", "database.sql", "database.sql"},
		{"path-like name is flattened", "/nested/name", "database.sql", "name-database.sql"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := restoreDumpName(tc.container, tc.dumpBase); got != tc.want {
				t.Errorf("restoreDumpName(%q, %q) = %q, want %q", tc.container, tc.dumpBase, got, tc.want)
			}
		})
	}
}

// TestPreserveDatabaseDumpCustomDest copies the dump into an explicit restore
// destination under a container-prefixed name.
func TestPreserveDatabaseDumpCustomDest(t *testing.T) {
	staging := t.TempDir()
	dest := t.TempDir()
	dump := filepath.Join(staging, DatabaseDumpFile)
	if err := os.WriteFile(dump, []byte("-- dump --"), 0o600); err != nil {
		t.Fatalf("write dump: %v", err)
	}

	saved, err := preserveDatabaseDump(context.Background(), dump, dest, restoreInspect{Name: "/mariadb"})
	if err != nil {
		t.Fatalf("preserveDatabaseDump: %v", err)
	}
	want := filepath.Join(dest, "mariadb-"+DatabaseDumpFile)
	if saved != want {
		t.Fatalf("saved = %q, want %q", saved, want)
	}
	got, err := os.ReadFile(saved) // #nosec G304 — test-controlled path
	if err != nil {
		t.Fatalf("read saved dump: %v", err)
	}
	if string(got) != "-- dump --" {
		t.Errorf("saved content = %q, want %q", got, "-- dump --")
	}
}

// TestPreserveDatabaseDumpInPlace lands the dump beside the container's first
// restored bind mount when no custom destination was given.
func TestPreserveDatabaseDumpInPlace(t *testing.T) {
	staging := t.TempDir()
	appdata := t.TempDir()
	volSource := filepath.Join(appdata, "mariadb")
	if err := os.MkdirAll(volSource, 0o750); err != nil {
		t.Fatalf("mkdir vol: %v", err)
	}
	dump := filepath.Join(staging, DatabaseDumpFile)
	if err := os.WriteFile(dump, []byte("data"), 0o600); err != nil {
		t.Fatalf("write dump: %v", err)
	}

	var inspect restoreInspect
	inspectJSON := `{"Name":"/mariadb","Mounts":[{"Type":"bind","Source":"` + volSource + `","Destination":"/config"}]}`
	if err := json.Unmarshal([]byte(inspectJSON), &inspect); err != nil {
		t.Fatalf("unmarshal inspect: %v", err)
	}

	saved, err := preserveDatabaseDump(context.Background(), dump, "", inspect)
	if err != nil {
		t.Fatalf("preserveDatabaseDump: %v", err)
	}
	// Lands beside the mount source (its parent directory), which persists on
	// the host after the staging dir is cleaned up.
	want := filepath.Join(appdata, "mariadb-"+DatabaseDumpFile)
	if saved != want {
		t.Fatalf("saved = %q, want %q", saved, want)
	}
	if _, err := os.Stat(saved); err != nil {
		t.Errorf("saved dump not found: %v", err)
	}
}

// TestPreserveDatabaseDumpNoDestination returns "" (nowhere durable) when there
// is neither a custom destination nor a backupable mount to land beside.
func TestPreserveDatabaseDumpNoDestination(t *testing.T) {
	staging := t.TempDir()
	dump := filepath.Join(staging, DatabaseDumpFile)
	if err := os.WriteFile(dump, []byte("data"), 0o600); err != nil {
		t.Fatalf("write dump: %v", err)
	}
	saved, err := preserveDatabaseDump(context.Background(), dump, "", restoreInspect{Name: "/mariadb"})
	if err != nil {
		t.Fatalf("preserveDatabaseDump: %v", err)
	}
	if saved != "" {
		t.Errorf("saved = %q, want empty (no durable location)", saved)
	}
}

// TestPreserveDatabaseDumpSkipsFileMount ensures a leading file mount is not
// chosen for the in-place location; the first directory mount is used instead.
func TestPreserveDatabaseDumpSkipsFileMount(t *testing.T) {
	staging := t.TempDir()
	root := t.TempDir()

	// A file mount (bind of a single file) comes first, then a directory mount.
	fileMount := filepath.Join(root, "config.yml")
	if err := os.WriteFile(fileMount, []byte("cfg"), 0o600); err != nil {
		t.Fatalf("write file mount: %v", err)
	}
	dirParent := filepath.Join(root, "appdata")
	dirMount := filepath.Join(dirParent, "mariadb")
	if err := os.MkdirAll(dirMount, 0o750); err != nil {
		t.Fatalf("mkdir dir mount: %v", err)
	}
	dump := filepath.Join(staging, DatabaseDumpFile)
	if err := os.WriteFile(dump, []byte("data"), 0o600); err != nil {
		t.Fatalf("write dump: %v", err)
	}

	var inspect restoreInspect
	inspectJSON := `{"Name":"/mariadb","Mounts":[` +
		`{"Type":"bind","Source":"` + fileMount + `","Destination":"/config.yml"},` +
		`{"Type":"bind","Source":"` + dirMount + `","Destination":"/config"}]}`
	if err := json.Unmarshal([]byte(inspectJSON), &inspect); err != nil {
		t.Fatalf("unmarshal inspect: %v", err)
	}

	saved, err := preserveDatabaseDump(context.Background(), dump, "", inspect)
	if err != nil {
		t.Fatalf("preserveDatabaseDump: %v", err)
	}
	// Beside the directory mount (its parent), not beside the file mount.
	want := filepath.Join(dirParent, "mariadb-"+DatabaseDumpFile)
	if saved != want {
		t.Fatalf("saved = %q, want %q (file mount should be skipped)", saved, want)
	}
}
