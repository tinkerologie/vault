package runner

import (
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// TestRunHealthChecksAllOk exercises the happy path with one local
// destination. RunHealthChecks should persist an "ok" status.
func TestRunHealthChecksAllOk(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	r.RunHealthChecks()

	got, err := database.GetStorageDestination(dest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastHealthCheckStatus != "ok" {
		t.Errorf("LastHealthCheckStatus = %q, want ok (err=%q)", got.LastHealthCheckStatus, got.LastHealthCheckError)
	}
	if got.LastHealthCheckError != "" {
		t.Errorf("LastHealthCheckError = %q, want empty", got.LastHealthCheckError)
	}
}

// TestRunHealthChecksEmpty exercises the no-destination short-circuit.
// Should be a clean no-op (no panics, no logs interpreted as errors).
func TestRunHealthChecksEmpty(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	r.RunHealthChecks() // no destinations exist yet — must not panic.
}

// TestCheckStorageDestinationOK exercises the direct (one-shot) entry
// point against a healthy local destination.
func TestCheckStorageDestinationOK(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	status, msg := r.CheckStorageDestination(dest)
	if status != "ok" {
		t.Errorf("status = %q, want ok (msg=%q)", status, msg)
	}
	if msg != "" {
		t.Errorf("msg = %q, want empty", msg)
	}
	got, _ := database.GetStorageDestination(dest.ID)
	if got.LastHealthCheckStatus != "ok" {
		t.Errorf("LastHealthCheckStatus = %q, want ok", got.LastHealthCheckStatus)
	}
}

// TestCheckStorageDestinationAdapterConstructionFailure exercises the
// failure branch where NewAdapter itself returns an error (unknown type).
// recordBreakerOutcome should increment consecutive_failures.
func TestCheckStorageDestinationAdapterConstructionFailure(t *testing.T) {
	t.Parallel()
	r, database, _ := setupTestRunner(t)
	id, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "broken-type",
		Type:   "this-type-does-not-exist",
		Config: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	dest, _ := database.GetStorageDestination(id)

	status, msg := r.CheckStorageDestination(dest)
	if status != "failed" {
		t.Errorf("status = %q, want failed", status)
	}
	if msg == "" {
		t.Errorf("msg should be populated on failure")
	}

	got, _ := database.GetStorageDestination(id)
	if got.LastHealthCheckStatus != "failed" {
		t.Errorf("persisted status = %q, want failed", got.LastHealthCheckStatus)
	}
	if got.LastHealthCheckError == "" {
		t.Errorf("LastHealthCheckError empty, want populated")
	}
	// recordBreakerOutcome failure path should have incremented the counter.
	if got.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1", got.ConsecutiveFailures)
	}
}

// TestCheckStorageDestinationTestConnectionFailure exercises the path
// where adapter construction succeeds but TestConnection fails because
// the configured local path does not exist and cannot be Stat'd as a
// directory.
func TestCheckStorageDestinationTestConnectionFailure(t *testing.T) {
	t.Parallel()
	r, database, _ := setupTestRunner(t)

	// Point at a non-existent path. LocalAdapter.TestConnection returns an
	// error because os.Stat fails.
	missingDir := filepath.Join(t.TempDir(), "no-such-subdir-must-not-exist")
	id, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "bad-path",
		Type:   "local",
		Config: `{"path":"` + missingDir + `"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	dest, _ := database.GetStorageDestination(id)

	status, msg := r.CheckStorageDestination(dest)
	if status != "failed" {
		t.Errorf("status = %q, want failed", status)
	}
	if msg == "" {
		t.Errorf("msg should be populated on failure")
	}
}

// TestBroadcastStorageHealthDoesNotPanic verifies the WS broadcast helper
// is safe with a live hub and no subscribers, and is a clean no-op with
// nil hub (the New() constructor accepts nil hub).
func TestBroadcastStorageHealthDoesNotPanic(t *testing.T) {
	t.Parallel()

	t.Run("with live hub", func(t *testing.T) {
		t.Parallel()
		hub := ws.NewHub()
		go hub.Run()
		dbPath := filepath.Join(t.TempDir(), "vault.db")
		d, err := db.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		r := New(d, hub, nil)
		r.broadcastStorageHealth(1, "ok", "")
		r.broadcastStorageHealth(2, "failed", "synthetic")
	})

	t.Run("with nil hub", func(t *testing.T) {
		t.Parallel()
		dbPath := filepath.Join(t.TempDir(), "vault.db")
		d, err := db.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		r := New(d, nil, nil)
		// broadcast() short-circuits on r.hub == nil; this exercises that
		// branch through broadcastStorageHealth.
		r.broadcastStorageHealth(1, "ok", "")
	})
}

// TestRecordBreakerOutcomeNilBreaker verifies the nil-breaker guard so the
// daemon can run without a configured breaker (defensive; New() always
// constructs one, but the field is documented as optional).
func TestRecordBreakerOutcomeNilBreaker(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	r.breaker = nil // forcibly drop the breaker
	// Should be a clean no-op — no panic, no DB write.
	r.recordBreakerOutcome(123, true)
	r.recordBreakerOutcome(123, false)
}

// TestRecordBreakerOutcomeMissingDest covers the fetch-failure log path.
func TestRecordBreakerOutcomeMissingDest(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	// dest 99999 doesn't exist — recordBreakerOutcome logs and returns
	// without panicking.
	r.recordBreakerOutcome(99999, true)
	r.recordBreakerOutcome(99999, false)
}
