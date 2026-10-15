package jobs

import (
	"errors"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

func folderItem(path string) db.JobItem {
	return db.JobItem{ItemType: "folder", ItemName: "f", ItemID: "f", Settings: `{"path":"` + path + `"}`}
}

func createLocalDest(t *testing.T, d *db.DB, name, cfg string) int64 {
	t.Helper()
	id, err := d.CreateStorageDestination(db.StorageDestination{Name: name, Type: "local", Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestValidationErrorUnwrapAndIsValidation_458(t *testing.T) {
	cause := errors.New("root cause")
	err := invalidCause("schedule", cause, "bad")
	if !errors.Is(err, cause) {
		t.Fatal("Unwrap should expose the cause")
	}
	if ve, ok := IsValidation(err); !ok || ve.Field != "schedule" {
		t.Fatalf("IsValidation = %+v, %v", ve, ok)
	}
	if ve, ok := IsValidation(errors.New("plain")); ok || ve != nil {
		t.Fatalf("plain error classified as validation: %+v", ve)
	}
}

func TestFolderPathPolicy_458(t *testing.T) {
	i, _, e := newIntake(t)
	var seen []string
	i.SetPathValidator(func(p string) error {
		seen = append(seen, p)
		if strings.HasPrefix(p, "/mnt/") {
			return nil
		}
		return errors.New("outside")
	})
	flushes := 0
	i.SetConfigChangeHook(func() { flushes++ })

	// Accepted folder path; non-folder and pathless folder items are skipped.
	items := []db.JobItem{
		folderItem("/mnt/user/data"),
		item("plex"),
		{ItemType: "folder", ItemName: "rel", ItemID: "rel"},
	}
	if _, _, err := i.Create(validJob(), items); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(seen) != 1 || seen[0] != "/mnt/user/data" {
		t.Fatalf("validator saw %v", seen)
	}
	if flushes != 1 || e.flushes != 0 {
		t.Fatalf("SetConfigChangeHook not honoured: hook=%d original=%d", flushes, e.flushes)
	}

	// Rejected path.
	j := validJob()
	j.Name = "bad-path"
	_, _, err := i.Create(j, []db.JobItem{folderItem("/etc")})
	if ve, ok := IsValidation(err); !ok || ve.Field != "items" {
		t.Fatalf("outside path = %v", err)
	}

	// Malformed settings JSON.
	j.Name = "bad-json"
	_, _, err = i.Create(j, []db.JobItem{{ItemType: "folder", ItemName: "x", Settings: "{"}})
	if ve, ok := IsValidation(err); !ok || !strings.Contains(ve.Message, "valid JSON") {
		t.Fatalf("bad JSON = %v", err)
	}
}

func TestStorageDestChecksAndOverlap_458(t *testing.T) {
	i, d, _ := newIntake(t)

	j := validJob()
	j.StorageDestID = 9999
	_, _, err := i.Create(j, nil)
	if ve, ok := IsValidation(err); !ok || ve.Field != "storage_dest_id" {
		t.Fatalf("missing dest = %v", err)
	}

	localID := createLocalDest(t, d, "local", `{"path":"/mnt/user/backups"}`)
	j.StorageDestID = localID
	_, _, err = i.Create(j, []db.JobItem{folderItem("/mnt/user/backups/inner")})
	if _, ok := IsValidation(err); !ok {
		t.Fatalf("overlap should be a validation error, got %v", err)
	}

	// A local destination whose config is not JSON has no path to collide with.
	brokenID := createLocalDest(t, d, "broken", `not-json`)
	j.StorageDestID = brokenID
	if _, _, err := i.Create(j, []db.JobItem{folderItem("/mnt/user/backups/inner")}); err != nil {
		t.Fatalf("broken config should skip overlap: %v", err)
	}

	// Remote destinations are never checked for overlap.
	remoteID, err := d.CreateStorageDestination(db.StorageDestination{Name: "remote", Type: "sftp", Config: `{"path":"/mnt/user/backups"}`})
	if err != nil {
		t.Fatal(err)
	}
	j.Name = "remote-job"
	j.StorageDestID = remoteID
	created, _, err := i.Create(j, []db.JobItem{folderItem("/mnt/user/backups/inner")})
	if err != nil {
		t.Fatalf("remote dest: %v", err)
	}

	// Update: a destination switch onto the persisted items' tree is refused,
	// as is a bad folder path in replacement items.
	upd := created
	upd.StorageDestID = localID
	if _, _, err := i.Update(created.ID, upd, nil); err == nil {
		t.Fatal("update into overlapping dest should fail")
	}
	i.SetPathValidator(func(string) error { return errors.New("never") })
	if _, _, err := i.Update(created.ID, created, []db.JobItem{folderItem("/mnt/user/x")}); err == nil {
		t.Fatal("update with rejected folder path should fail")
	}
	bad := created
	bad.Name = ""
	if _, _, err := i.Update(created.ID, bad, nil); err == nil {
		t.Fatal("update with invalid job should fail")
	}
}

func TestIntakeServerFaultsOnClosedDB_458(t *testing.T) {
	i, d, _ := newIntake(t)
	destID := createLocalDest(t, d, "dest", `{"path":"/mnt/disk1/b"}`)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	j := validJob()
	j.StorageDestID = destID
	_, _, err := i.Create(j, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := IsValidation(err); ok {
		t.Fatalf("DB failure must not be blamed on the caller: %v", err)
	}
	if _, _, err := i.Update(1, validJob(), nil); err == nil || errors.Is(err, ErrJobNotFound) {
		t.Fatalf("Update on closed DB = %v", err)
	}
	if _, err := i.Delete(1, DeleteOptions{DeleteFiles: true}); err == nil {
		t.Fatal("Delete with files on closed DB should fail")
	}
	if _, err := i.Delete(1, DeleteOptions{}); err == nil {
		t.Fatal("Delete on closed DB should fail")
	}
}

func TestDeleteWithFiles_458(t *testing.T) {
	i, d, e := newIntake(t)
	destID := createLocalDest(t, d, "dest", `{"path":"/mnt/disk1/b"}`)
	j := validJob()
	j.StorageDestID = destID
	job, _, err := i.Create(j, nil)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: job.ID, Status: "success", RunType: "backup"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"rp/1", ""} {
		if _, err := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: job.ID, BackupType: "full", StoragePath: p}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := i.Delete(job.ID, DeleteOptions{DeleteFiles: true})
	if err != nil || !out.CleanupStarted || e.runner.cleanups != 1 {
		t.Fatalf("Delete = %+v, %v, cleanups=%d", out, err, e.runner.cleanups)
	}

	// Already-gone job: idempotent, no cleanup.
	out, err = i.Delete(job.ID, DeleteOptions{DeleteFiles: true})
	if err != nil || out.CleanupStarted {
		t.Fatalf("second Delete = %+v, %v", out, err)
	}

	// Orphaned job (no destination): record-only delete.
	j2 := validJob()
	j2.Name = "orphan"
	orphan, _, err := i.Create(j2, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err = i.Delete(orphan.ID, DeleteOptions{DeleteFiles: true})
	if err != nil || out.CleanupStarted {
		t.Fatalf("orphan Delete = %+v, %v", out, err)
	}
}

func TestDeleteWithoutRunner_458(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	i := New(d, nil, func() error { return errors.New("reload failed") }, nil, nil)
	job, _, err := i.Create(validJob(), nil)
	if err != nil {
		t.Fatalf("Create (reload error is only logged): %v", err)
	}
	out, err := i.Delete(job.ID, DeleteOptions{DeleteFiles: true})
	if err != nil || out.CleanupStarted {
		t.Fatalf("Delete = %+v, %v", out, err)
	}
}

func TestRollbackFailuresAreReported_458(t *testing.T) {
	t.Run("create rollback", func(t *testing.T) {
		i, d, _ := newIntake(t)
		failItemInserts(t, d)
		if _, err := d.Exec(`CREATE TRIGGER no_job_delete BEFORE DELETE ON jobs
			BEGIN SELECT RAISE(ABORT, 'nodelete'); END;`); err != nil {
			t.Fatal(err)
		}
		_, _, err := i.Create(validJob(), []db.JobItem{item("boom")})
		if err == nil || !strings.Contains(err.Error(), "rollback also failed") {
			t.Fatalf("Create = %v", err)
		}
	})

	t.Run("update restore re-insert", func(t *testing.T) {
		i, d, _ := newIntake(t)
		job, _, err := i.Create(validJob(), []db.JobItem{item("late")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`CREATE TRIGGER fail_some BEFORE INSERT ON job_items
			WHEN NEW.item_name IN ('boom', 'late')
			BEGIN SELECT RAISE(ABORT, 'fail'); END;`); err != nil {
			t.Fatal(err)
		}
		_, _, err = i.Update(job.ID, job, []db.JobItem{item("boom")})
		if err == nil || !strings.Contains(err.Error(), "rollback also failed") {
			t.Fatalf("Update = %v", err)
		}
	})

	t.Run("update delete items", func(t *testing.T) {
		i, d, _ := newIntake(t)
		job, _, err := i.Create(validJob(), []db.JobItem{item("a")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`CREATE TRIGGER no_item_delete BEFORE DELETE ON job_items
			BEGIN SELECT RAISE(ABORT, 'nodelete'); END;`); err != nil {
			t.Fatal(err)
		}
		if _, _, err := i.Update(job.ID, job, []db.JobItem{item("b")}); err == nil {
			t.Fatal("expected DeleteJobItems failure")
		}
	})

	t.Run("restoreItems delete fails", func(t *testing.T) {
		i, d, _ := newIntake(t)
		// Row triggers only fire when a row is deleted, so seed one.
		job, _, err := i.Create(validJob(), []db.JobItem{item("keep")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`CREATE TRIGGER no_item_delete BEFORE DELETE ON job_items
			BEGIN SELECT RAISE(ABORT, 'nodelete'); END;`); err != nil {
			t.Fatal(err)
		}
		cause := errors.New("cause")
		err = i.restoreItems(job.ID, nil, cause)
		if !errors.Is(err, cause) || !strings.Contains(err.Error(), "rollback also failed") {
			t.Fatalf("restoreItems = %v", err)
		}
	})
}
