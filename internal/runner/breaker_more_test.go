package runner

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestBreakerRecordFailurePersistsCounter drives RecordFailure against a real
// DB so the persisted counter is observable and the open-transition path is
// exercised end-to-end.
func TestBreakerRecordFailurePersistsCounter(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	b := r.breaker
	if b == nil {
		t.Fatal("runner has no breaker")
	}

	// First failure: counter goes to 1, state stays closed.
	state, opened := b.RecordFailure(database, dest)
	if state != "closed" || opened {
		t.Fatalf("first failure: state=%s opened=%v, want closed/false", state, opened)
	}
	got, err := database.GetStorageDestination(dest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConsecutiveFailures != 1 {
		t.Errorf("after 1st failure ConsecutiveFailures=%d, want 1", got.ConsecutiveFailures)
	}

	// Second failure (count goes to 2): state stays closed.
	state, opened = b.RecordFailure(database, got)
	if state != "closed" || opened {
		t.Fatalf("second failure: state=%s opened=%v, want closed/false", state, opened)
	}

	got, _ = database.GetStorageDestination(dest.ID)
	if got.ConsecutiveFailures != 2 {
		t.Errorf("after 2nd failure ConsecutiveFailures=%d, want 2", got.ConsecutiveFailures)
	}

	// Third failure (count goes to 3 = threshold): opens.
	state, opened = b.RecordFailure(database, got)
	if state != "open" || !opened {
		t.Fatalf("third failure: state=%s opened=%v, want open/true", state, opened)
	}
	got, _ = database.GetStorageDestination(dest.ID)
	if got.BreakerState != "open" {
		t.Errorf("BreakerState=%q after open transition, want open", got.BreakerState)
	}
	if got.ConsecutiveFailures != 3 {
		t.Errorf("ConsecutiveFailures=%d after open, want 3", got.ConsecutiveFailures)
	}
}

// TestBreakerRecordFailureStaysOpenWhenAlreadyOpen verifies the no-op
// path when state is already "open".
func TestBreakerRecordFailureStaysOpenWhenAlreadyOpen(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)
	// Force open state with 5 prior failures.
	if err := database.OpenBreaker(dest.ID, 5); err != nil {
		t.Fatal(err)
	}
	dest, _ = database.GetStorageDestination(dest.ID)

	state, opened := r.breaker.RecordFailure(database, dest)
	if opened {
		t.Errorf("should not re-emit opened when already open")
	}
	if state != "open" {
		t.Errorf("state should remain open, got %s", state)
	}
}

// TestBreakerRecordSuccessPersistsZero verifies that a success while
// closed resets ConsecutiveFailures to 0.
func TestBreakerRecordSuccessPersistsZero(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)
	// Seed a failure count.
	if err := database.RecordDestinationFailure(dest.ID, 2); err != nil {
		t.Fatal(err)
	}
	dest, _ = database.GetStorageDestination(dest.ID)

	state := r.breaker.RecordSuccess(database, dest)
	if state != "closed" {
		t.Errorf("state=%s, want closed", state)
	}
	got, _ := database.GetStorageDestination(dest.ID)
	if got.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures=%d, want 0", got.ConsecutiveFailures)
	}
}

// TestBreakerRecordSuccessClosesAfterTwo verifies the open->closed
// transition after the configured close-successes threshold.
func TestBreakerRecordSuccessClosesAfterTwo(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)
	if err := database.OpenBreaker(dest.ID, 5); err != nil {
		t.Fatal(err)
	}
	dest, _ = database.GetStorageDestination(dest.ID)

	// First success while open: stays open.
	state := r.breaker.RecordSuccess(database, dest)
	if state != "open" {
		t.Errorf("first success: state=%s, want open", state)
	}
	got, _ := database.GetStorageDestination(dest.ID)
	if got.BreakerState != "open" {
		t.Errorf("BreakerState=%q after 1 success, want open", got.BreakerState)
	}

	// Second success: closes.
	dest, _ = database.GetStorageDestination(dest.ID)
	state = r.breaker.RecordSuccess(database, dest)
	if state != "closed" {
		t.Errorf("second success: state=%s, want closed", state)
	}
	got, _ = database.GetStorageDestination(dest.ID)
	if got.BreakerState != "closed" {
		t.Errorf("BreakerState=%q after 2 successes, want closed", got.BreakerState)
	}
	if got.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures=%d after close, want 0", got.ConsecutiveFailures)
	}
}

// TestBreakerManualClose verifies the operator-triggered reset.
func TestBreakerManualClose(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)
	if err := database.OpenBreaker(dest.ID, 7); err != nil {
		t.Fatal(err)
	}

	if err := r.breaker.ManualClose(database, dest.ID); err != nil {
		t.Fatalf("ManualClose: %v", err)
	}
	got, _ := database.GetStorageDestination(dest.ID)
	if got.BreakerState != "closed" {
		t.Errorf("BreakerState=%q after ManualClose, want closed", got.BreakerState)
	}
	if got.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures=%d after ManualClose, want 0", got.ConsecutiveFailures)
	}
}

// TestBreakerResetSuccessCounter verifies the in-memory counter is cleared.
func TestBreakerResetSuccessCounter(t *testing.T) {
	t.Parallel()
	b := NewBreaker(3, 5)
	// Seed a partial success streak.
	b.recordSuccessFor(99, "open")
	b.recordSuccessFor(99, "open")
	// After reset, the next success-while-open should restart from 1.
	b.resetSuccessCounter(99)
	state, count, _ := b.recordSuccessFor(99, "open")
	if state != "open" || count != 1 {
		t.Errorf("after reset, recordSuccessFor(open) → state=%s count=%d, want open/1", state, count)
	}
}

// TestBreakerOpenErrorMessage exercises the Error method.
func TestBreakerOpenErrorMessage(t *testing.T) {
	t.Parallel()
	t.Run("zero time", func(t *testing.T) {
		t.Parallel()
		err := &BreakerOpenError{DestID: 1, DestName: "n"}
		got := err.Error()
		if got != "destination breaker is open" {
			t.Errorf("Error() = %q, want %q", got, "destination breaker is open")
		}
		// errors.Is should hit identity equality.
		if !errors.Is(err, err) {
			t.Error("BreakerOpenError not equal to itself")
		}
	})
	t.Run("with opened-at", func(t *testing.T) {
		t.Parallel()
		ts := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
		err := &BreakerOpenError{OpenedAt: ts}
		got := err.Error()
		if !strings.Contains(got, "2026-05-26T12:00:00Z") {
			t.Errorf("Error() = %q, missing RFC3339 timestamp", got)
		}
	})
}

// TestBreakerNewBreakerDefaults verifies the zero-input defaults.
func TestBreakerNewBreakerDefaults(t *testing.T) {
	t.Parallel()
	b := NewBreaker(0, 0)
	if b.failThreshold != 3 {
		t.Errorf("failThreshold=%d, want default 3", b.failThreshold)
	}
	if b.closeSuccesses != 2 {
		t.Errorf("closeSuccesses=%d, want default 2", b.closeSuccesses)
	}
	// Sanity guard against an explicit zero in the future.
	b2 := NewBreaker(5, 7)
	if b2.failThreshold != 5 || b2.closeSuccesses != 7 {
		t.Errorf("explicit values not retained: %+v", b2)
	}
}

// Suppress unused-import warning during incremental compilation when the
// type is not referenced elsewhere.
var _ db.StorageDestination
