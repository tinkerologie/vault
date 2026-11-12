package runner

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/webdav"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
)

func TestDatabaseBackupIncludesCommittedWAL(t *testing.T) {
	for _, tc := range []struct {
		name, passphrase, extension string
	}{
		{name: "plaintext", extension: ".db"},
		{name: "encrypted", passphrase: "database backup fixture", extension: ".age"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, database := a458Runner(t)
			jobDir, extraDir := t.TempDir(), t.TempDir()
			dest := a458LocalDest(t, database, "job destination", jobDir)
			extra := a458LocalDest(t, database, "catalogue destination", extraDir)
			extra.BackupDatabaseEnabled = true
			if err := database.UpdateStorageDestination(extra); err != nil {
				t.Fatal(err)
			}
			a458SetSetting(t, database, "encryption_passphrase", tc.passphrase)
			jobID, err := database.CreateJob(db.Job{Name: "WAL fixture", StorageDestID: dest.ID})
			if err != nil {
				t.Fatal(err)
			}
			runID, err := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"})
			if err != nil {
				t.Fatal(err)
			}
			for _, pragma := range []string{"PRAGMA wal_autocheckpoint=0", "PRAGMA busy_timeout=0", "PRAGMA wal_checkpoint(TRUNCATE)"} {
				if _, err := database.Exec(pragma); err != nil {
					t.Fatal(err)
				}
			}

			reader, err := sql.Open("sqlite", database.Path())
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			tx, err := reader.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var count int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM restore_points").Scan(&count); err != nil || count != 0 {
				t.Fatalf("pinning snapshot: restore points=%d, error=%v", count, err)
			}
			const storagePath = "job/committed-in-wal"
			rpID, err := database.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: storagePath})
			if err != nil {
				t.Fatal(err)
			}
			var busy, frames, checkpointed int
			err = database.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed)
			if err != nil || busy != 1 || frames <= checkpointed {
				t.Fatalf("expected committed WAL frames behind a busy checkpoint: %d %d %d, error=%v", busy, frames, checkpointed, err)
			}

			if err := r.backupDatabase(t.Context(), dest); err != nil {
				t.Fatal(err)
			}
			var canonical []byte
			var datedName string
			for _, root := range []string{jobDir, extraDir} {
				dir := filepath.Join(root, dbBackupBaseDir)
				latest := filepath.Join(dir, "vault.db.latest"+tc.extension)
				entries, err := filepath.Glob(filepath.Join(dir, "vault.db.2*"+tc.extension))
				if err != nil || len(entries) != 1 {
					t.Fatalf("expected one timestamped snapshot, got %v: %v", entries, err)
				}
				if datedName == "" {
					datedName = filepath.Base(entries[0])
				}
				if filepath.Base(entries[0]) != datedName {
					t.Fatal("destinations used different timestamps for one export")
				}
				for _, path := range []string{entries[0], latest} {
					data := readDatabaseExport(t, path, tc.passphrase)
					if canonical == nil {
						canonical = data
					}
					if !bytes.Equal(canonical, data) {
						t.Error("timestamped, latest, and fan-out snapshots differ")
					}
					isolated := filepath.Join(t.TempDir(), "recovered.db")
					if err := os.WriteFile(isolated, data, 0o600); err != nil {
						t.Fatal(err)
					}
					recovered, err := db.Open(isolated)
					if err != nil {
						t.Fatal(err)
					}
					rp, err := recovered.GetRestorePoint(rpID)
					_ = recovered.Close()
					if err != nil || rp.JobID != jobID || rp.StoragePath != storagePath {
						t.Errorf("recovered %s is missing committed restore point %d: %+v, %v", filepath.Base(path), rpID, rp, err)
					}
				}
			}
		})
	}
}

func readDatabaseExport(t *testing.T, path, passphrase string) []byte {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- temporary test artifact
	if err != nil {
		t.Fatal(err)
	}
	if passphrase == "" {
		return data
	}
	decrypted, err := crypto.DecryptReader(passphrase, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer decrypted.Close()
	result, err := io.ReadAll(decrypted)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDatabaseBackupSnapshotFailurePreservesLatestAndReportsWarning(t *testing.T) {
	r, database := a458Runner(t)
	root := t.TempDir()
	dest := a458LocalDest(t, database, "job destination", root)
	extraRoot := t.TempDir()
	extra := a458LocalDest(t, database, "catalogue destination", extraRoot)
	extra.BackupDatabaseEnabled = true
	if err := database.UpdateStorageDestination(extra); err != nil {
		t.Fatal(err)
	}
	jobID, err := database.CreateJob(db.Job{Name: "Snapshot failure fixture", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.backupDatabase(t.Context(), dest); err != nil {
		t.Fatal(err)
	}
	latest := filepath.Join(dbBackupBaseDir, "vault.db.latest.db")
	previous := readDatabaseExport(t, filepath.Join(root, latest), "")
	r.backupDatabaseSnapshot = func(_ context.Context, _ string) error {
		return errors.New("fixture snapshot failure from https://user:fixture-value@example.invalid/private")
	}

	r.finalizeDatabaseBackup(t.Context(), dest, jobID, runID)

	for _, path := range []string{root, extraRoot} {
		if got := readDatabaseExport(t, filepath.Join(path, latest), ""); !bytes.Equal(got, previous) {
			t.Errorf("snapshot failure changed the previous latest copy in %s", path)
		}
		entries, err := os.ReadDir(filepath.Join(path, dbBackupBaseDir))
		if err != nil || len(entries) != 2 {
			t.Errorf("snapshot failure changed the published copies in %s: %v", path, err)
		}
	}
	run, err := database.GetJobRun(runID)
	if err != nil || run.Status != "success" {
		t.Errorf("workload outcome changed to %q: %v", run.Status, err)
	}
	runLogs, err := database.ListRunLogEntries(t.Context(), runID, 0, 10)
	if err != nil || len(runLogs) != 1 || runLogs[0].Level != runLogLevelWarn ||
		!strings.Contains(runLogs[0].Message, "fixture snapshot failure") ||
		strings.Contains(runLogs[0].Message, "fixture-value") {
		t.Errorf("expected one redacted export warning: %+v, %v", runLogs, err)
	}
	activity, err := database.ListActivityLogs(10, "backup", 0)
	if err != nil || len(activity) != 1 || activity[0].Level != "warn" ||
		activity[0].Message != "Database Backup failed" ||
		strings.Contains(activity[0].Details, "fixture-value") ||
		strings.Contains(activity[0].Details, "run_log") {
		t.Errorf("expected a separate redacted activity warning: %+v, %v", activity, err)
	}
}

func TestDatabaseBackupTimestampedFailurePreservesLatest(t *testing.T) {
	r, database := a458Runner(t)
	root := t.TempDir()
	dest := a458LocalDest(t, database, "job destination", root)
	dir := filepath.Join(root, dbBackupBaseDir)
	const stamp = "2026-11-12T01-00-00"
	if err := os.MkdirAll(filepath.Join(dir, "vault.db."+stamp+".db"), 0o700); err != nil {
		t.Fatal(err)
	}
	latest := filepath.Join(dir, "vault.db.latest.db")
	const previous = "previous snapshot"
	if err := os.WriteFile(latest, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := database.BackupToFile(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	err := r.backupDatabaseToDest(t.Context(), dest, snapshot, stamp, "")
	if err == nil || !strings.Contains(err.Error(), "timestamped write") {
		t.Fatalf("expected a timestamped write failure, got %v", err)
	}
	if got := readDatabaseExport(t, latest, ""); string(got) != previous {
		t.Error("timestamped write failure replaced the previous latest copy")
	}
}

func TestDatabaseBackupLatestFailureSkipsPrune(t *testing.T) {
	r, database := a458Runner(t)
	root := t.TempDir()
	dest := a458LocalDest(t, database, "job destination", root)
	dir := filepath.Join(root, dbBackupBaseDir)
	if err := os.MkdirAll(filepath.Join(dir, "vault.db.latest.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	const oldest = "vault.db.2025-11-12T01-00-00.db"
	if err := os.WriteFile(filepath.Join(dir, oldest), []byte("oldest snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := range dbBackupsKept - 1 {
		name := "vault.db." + time.Date(2026, 10, 1+i, 1, 0, 0, 0, time.UTC).Format("2006-01-02T15-04-05") + ".db"
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := database.BackupToFile(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	const stamp = "2026-11-12T01-00-00"
	err := r.backupDatabaseToDest(t.Context(), dest, snapshot, stamp, "")
	if err == nil || !strings.Contains(err.Error(), "latest write") {
		t.Fatalf("expected latest write failure, got %v", err)
	}
	if got, want := readDatabaseExport(t, filepath.Join(dir, "vault.db."+stamp+".db"), ""), readDatabaseExport(t, snapshot, ""); !bytes.Equal(got, want) {
		t.Error("timestamped snapshot differs after latest write failed")
	}
	if _, err := os.Stat(filepath.Join(dir, oldest)); err != nil {
		t.Errorf("latest write failure pruned the previous oldest snapshot: %v", err)
	}
}

func TestDatabaseBackupCancellationStopsPublication(t *testing.T) {
	t.Run("before Destination open", func(t *testing.T) {
		r, database := a458Runner(t)
		root := t.TempDir()
		dest := a458LocalDest(t, database, "job destination", root)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		r.backupDatabaseSnapshot = func(ctx context.Context, path string) error {
			err := database.BackupToFile(ctx, path)
			cancel()
			return err
		}
		if err := r.backupDatabase(ctx, dest); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation after snapshot, got %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, dbBackupBaseDir)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("canceled export touched the Destination: %v", err)
		}
		if err := r.backupDatabaseToDest(ctx, dest, "", "", ""); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Destination open returned %v", err)
		}
	})

	for _, tc := range []struct {
		name, uploadPrefix string
		expectedDated      int
	}{
		{"during timestamped upload", "vault.db.2", 0},
		{"during latest upload", "vault.db.latest", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, database := a458Runner(t)
			root := t.TempDir()
			extraRoot := t.TempDir()
			extra := a458LocalDest(t, database, "opted-in destination", extraRoot)
			extra.BackupDatabaseEnabled = true
			if err := database.UpdateStorageDestination(extra); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, dbBackupBaseDir)
			if err := os.Mkdir(dir, 0750); err != nil {
				t.Fatal(err)
			}
			latest := filepath.Join(dir, "vault.db.latest.db")
			previous := []byte("previous snapshot")
			if err := os.WriteFile(latest, previous, 0600); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			aborted := make(chan struct{})
			dav := &webdav.Handler{FileSystem: webdav.Dir(root), LockSystem: webdav.NewMemLS()}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodPut || !strings.HasPrefix(filepath.Base(req.URL.Path), tc.uploadPrefix) {
					dav.ServeHTTP(w, req)
					return
				}
				_, _ = io.Copy(io.Discard, req.Body)
				cancel()
				select {
				case <-req.Context().Done():
					close(aborted)
				case <-time.After(5 * time.Second):
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}))
			defer server.Close()
			dest := db.StorageDestination{Type: "webdav", Config: `{"url":"` + server.URL + `"}`}
			if err := r.backupDatabase(ctx, dest); !errors.Is(err, context.Canceled) {
				t.Fatalf("expected cancellation during upload, got %v", err)
			}
			select {
			case <-aborted:
			case <-time.After(5 * time.Second):
				t.Fatal("WebDAV PUT did not observe cancellation")
			}
			if got := readDatabaseExport(t, latest, ""); !bytes.Equal(got, previous) {
				t.Error("canceled upload changed latest")
			}
			dated, err := filepath.Glob(filepath.Join(dir, "vault.db.2*.db"))
			if err != nil || len(dated) != tc.expectedDated {
				t.Errorf("expected %d timestamped snapshots, got %v (%v)", tc.expectedDated, dated, err)
			}
			if _, err := os.Stat(filepath.Join(extraRoot, dbBackupBaseDir)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("canceled export wrote to the opted-in Destination: %v", err)
			}
		})
	}
}

func TestDatabaseBackupReportsStagingAndDestinationListingErrors(t *testing.T) {
	r, database := a458Runner(t)
	root := t.TempDir()
	dest := a458LocalDest(t, database, "job destination", root)
	t.Run("private staging unavailable", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMPDIR", file)
		t.Setenv("TMP", file)
		if err := r.backupDatabase(t.Context(), dest); err == nil || !strings.Contains(err.Error(), "staging database snapshot") {
			t.Fatalf("expected staging error, got %v", err)
		}
	})
	t.Run("opted-in Destination listing unavailable", func(t *testing.T) {
		if _, err := database.Exec("ALTER TABLE storage_destinations RENAME TO fixture_destinations"); err != nil {
			t.Fatal(err)
		}
		if err := r.backupDatabase(t.Context(), dest); err == nil || !strings.Contains(err.Error(), "listing opt-in destinations") {
			t.Fatalf("expected Destination listing error, got %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, dbBackupBaseDir, "vault.db.latest.db")); err != nil {
			t.Fatalf("job Destination did not receive its snapshot: %v", err)
		}
	})
}
