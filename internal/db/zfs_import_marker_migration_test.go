package db

import (
	"path/filepath"
	"testing"
)

// An upgrade marks the ZFS items of jobs created by an import, once, from the
// run history that still exists then. Later restores read only the marker.
func TestZFSImportMarkerMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a database from before the migration.
	if _, err := d.Exec(`DELETE FROM settings WHERE key = ?`, settingZFSImportMarkerDone); err != nil {
		t.Fatal(err)
	}
	item := func(jobName, itemType, settings string, runs ...string) int64 {
		t.Helper()
		job, err := d.CreateJob(Job{Name: jobName, BackupTypeChain: "full"})
		if err != nil {
			t.Fatal(err)
		}
		id, err := d.AddJobItem(JobItem{JobID: job, ItemType: itemType, ItemName: "tank/" + jobName, Settings: settings})
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range runs {
			if _, err := d.CreateJobRun(JobRun{JobID: job, Status: st, BackupType: "full"}); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	imported := item("imported", "zfs", `{"keep":1}`, JobRunStatusImported, "completed")
	importedEmpty := item("importedempty", "zfs", ``, JobRunStatusImported)
	local := item("local", "zfs", `{}`, "completed", JobRunStatusImported)
	never := item("never", "zfs", `{}`)
	folder := item("folder", "folder", `{}`, JobRunStatusImported)
	broken := item("broken", "zfs", `{`, JobRunStatusImported)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	settings := func(id int64) string {
		t.Helper()
		var s string
		if err := d.QueryRow(`SELECT COALESCE(settings, '') FROM job_items WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for id, want := range map[int64]string{
		imported:      `{"keep":1,"imported":true}`,
		importedEmpty: `{"imported":true}`,
		local:         `{}`,
		never:         `{}`,
		folder:        `{}`,
		broken:        `{`,
	} {
		if got := settings(id); got != want {
			t.Errorf("item %d settings = %s, want %s", id, got, want)
		}
	}

	// It runs once: a job created by an import after the upgrade is marked by
	// the importer itself, and history is not consulted again.
	late := item("late", "zfs", `{}`, JobRunStatusImported)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if got := settings(late); got != `{}` {
		t.Errorf("migration ran again: %s", got)
	}
	if got := settings(imported); got != `{"keep":1,"imported":true}` {
		t.Errorf("marked item changed on reopen: %s", got)
	}
}
