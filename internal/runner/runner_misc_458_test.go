package runner

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/ws"
)

// TestStallWatchdog458WarnsThenBacksOffAfterCancel drives the watchdog on a
// fake clock: an idle period past warn (but short of cancelAfter) logs a
// warning and keeps going; after an external cancel the "not unwound"
// warnings back off exponentially, skipping the ticks in between.
func TestStallWatchdog458WarnsThenBacksOffAfterCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		buf := &syncBuffer{}
		d458CaptureLog(t, buf)

		r := &Runner{}
		r.lastProgressMu.Lock()
		r.lastProgress = time.Now()
		r.lastProgressMu.Unlock()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		r.startStallWatchdog(ctx, done, cancel, 7, 0, time.Minute, 2*time.Minute, time.Hour)

		// Idle for 3 minutes: past warn, well short of cancelAfter.
		time.Sleep(3*time.Minute + time.Second)
		synctest.Wait()
		if ctx.Err() != nil {
			t.Fatal("watchdog cancelled before cancelAfter elapsed")
		}
		if !strings.Contains(buf.String(), "job 7 no progress") {
			t.Fatalf("expected an idle warning, log:\n%s", buf.String())
		}

		// External cancel; the run "fails to unwind" for 8 minutes.
		cancel()
		time.Sleep(8 * time.Minute)
		synctest.Wait()
		got := strings.Count(buf.String(), "has not unwound")
		// Warnings at roughly 1m, 2m, 4m after the cancel was noticed: the
		// ticks in between are skipped by the back-off.
		if got < 2 || got > 4 {
			t.Fatalf("not-unwound warnings = %d, want back-off (2..4), log:\n%s", got, buf.String())
		}
		close(done)
		synctest.Wait()
	})
}

// d458CaptureLog redirects the standard logger into buf for the test.
func d458CaptureLog(t *testing.T, buf *syncBuffer) {
	t.Helper()
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
}

// TestMarkRunStalled458DBErrorIsSwallowed covers the best-effort failure
// branch: a closed database must not panic the watchdog.
func TestMarkRunStalled458DBErrorIsSwallowed(t *testing.T) {
	r, d := newTestRunner(t)
	_ = d.Close()
	r.markRunStalled(42, 1, "stuck") // must not panic
	r.markRunStalled(0, 1, "ignored")
}

// TestBroadcast458MarshalFailureIsDropped covers the marshal-error branches
// of both broadcast helpers: an unencodable payload is logged and dropped.
func TestBroadcast458MarshalFailureIsDropped(t *testing.T) {
	r := &Runner{hub: ws.NewHub()} // hub not running: nothing may be queued
	bad := map[string]any{"ch": make(chan int)}
	r.broadcast(bad)
	r.broadcastProgress(bad)
	r.Broadcast(bad)
}

// TestPruneGracePeriod458Default covers the default grace period fallback.
func TestPruneGracePeriod458Default(t *testing.T) {
	r := &Runner{}
	if got := r.pruneGracePeriod(); got != defaultAutoPruneGracePeriod {
		t.Fatalf("pruneGracePeriod() = %v, want %v", got, defaultAutoPruneGracePeriod)
	}
	r.autoPruneGracePeriod = time.Minute
	if got := r.pruneGracePeriod(); got != time.Minute {
		t.Fatalf("pruneGracePeriod() override = %v, want 1m", got)
	}
}

// TestReconcileAutoInclude458ListerErrorAndNonContainers covers the lister
// failure early return and the skip of non-container inventory entries.
func TestReconcileAutoInclude458ListerErrorAndNonContainers(t *testing.T) {
	r, d := newTestRunner(t)
	jobID := d458Seed(t, d, "", d458Job{job: db.Job{AutoIncludeContainers: true}})
	job, _ := d.GetJob(jobID)

	r.containerLister = func() ([]engine.BackupItem, error) { return nil, errors.New("docker down") }
	var items []db.JobItem
	r.reconcileAutoIncludeContainers(job, &items)
	if len(items) != 0 {
		t.Fatalf("lister error added %d items", len(items))
	}

	r.containerLister = func() ([]engine.BackupItem, error) {
		return []engine.BackupItem{
			{Name: "a-vm", Type: "vm"},
			{Name: "web", Type: "container", Settings: map[string]any{"id": "abc123"}},
		}, nil
	}
	r.reconcileAutoIncludeContainers(job, &items)
	if len(items) != 1 || items[0].ItemName != "web" || items[0].ItemID != "abc123" {
		t.Fatalf("items = %+v, want only the web container", items)
	}
}

// TestRewrapDedupKey458ErrorPaths covers the adapter failure, the list
// failure, the non-dedup skip, and the "locked, needs passphrase" outcome.
func TestRewrapDedupKey458ErrorPaths(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	dest, _ := dedupBackup(t, runnerWithKey(database, testServerKey()), database, t.TempDir())
	// A plain destination is skipped by RewrapDedupKeys.
	createNamedLocalDest(t, database, "plain-458", t.TempDir())

	other := runnerWithKey(database, otherServerKey())
	status, err := other.rewrapDedupKey(dest, "")
	if status != "locked_needs_passphrase" || err == nil {
		t.Fatalf("rewrap without passphrase = %q, %v; want locked_needs_passphrase", status, err)
	}
	results, err := other.RewrapDedupKeys("")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].StorageID != dest.ID {
		t.Fatalf("results = %+v, want only the dedup destination", results)
	}

	bad := dest
	bad.Type = "no-such-type"
	if status, err := other.rewrapDedupKey(bad, "pw"); status != "error" || err == nil {
		t.Fatalf("bad adapter = %q, %v; want error", status, err)
	}

	// Corrupt repo.json: OpenRepo fails with something other than a key
	// mismatch.
	storageDir := storageDirOf(t, dest)
	if err := os.WriteFile(filepath.Join(storageDir, "_vault", "repo.json"), []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, err := other.rewrapDedupKey(dest, "pw"); status != "error" || err == nil {
		t.Fatalf("corrupt repo = %q, %v; want error", status, err)
	}
	if _, err := other.GetDedupStats(dest); err == nil {
		t.Fatal("GetDedupStats on a corrupt repo should fail")
	}
	if _, err := other.GetDedupStats(bad); err == nil {
		t.Fatal("GetDedupStats with a bad adapter type should fail")
	}

	_ = database.Close()
	if _, err := other.RewrapDedupKeys("pw"); err == nil {
		t.Fatal("RewrapDedupKeys on a closed DB should fail")
	}
}
