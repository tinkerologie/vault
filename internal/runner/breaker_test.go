package runner

import (
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

func TestBreakerOpensOnThirdFailure(t *testing.T) {
	b := NewBreaker(3, 2)

	// Two failures keep state closed.
	state, _, opened := b.NextState("closed", 2, false)
	if opened || state != "closed" {
		t.Errorf("opened too early at 2 failures (state=%s opened=%v)", state, opened)
	}

	// 3rd failure opens.
	state, _, opened = b.NextState("closed", 3, false)
	if !opened || state != "open" {
		t.Errorf("expected open at 3 failures, got state=%s opened=%v", state, opened)
	}
}

func TestBreakerStaysOpenOnFurtherFailure(t *testing.T) {
	b := NewBreaker(3, 2)
	state, _, opened := b.NextState("open", 5, false)
	if opened {
		t.Errorf("should not re-emit opened when already open")
	}
	if state != "open" {
		t.Errorf("state stuck closed when should be open: %s", state)
	}
}

func TestBreakerClosesAfterTwoSuccesses(t *testing.T) {
	b := NewBreaker(3, 2)

	state, _, _ := b.recordSuccessFor(42, "open")
	if state != "open" {
		t.Errorf("first success while open should stay open, got %s", state)
	}
	state, _, _ = b.recordSuccessFor(42, "open")
	if state != "closed" {
		t.Errorf("second success should close breaker, got %s", state)
	}
}

func TestBreakerImmediateCloseOnSuccessWhileClosed(t *testing.T) {
	b := NewBreaker(3, 2)
	state, _, _ := b.recordSuccessFor(99, "closed")
	if state != "closed" {
		t.Errorf("success while closed should stay closed, got %s", state)
	}
}

func TestBreakerIsOpen(t *testing.T) {
	b := NewBreaker(3, 2)
	if b.IsOpen(db.StorageDestination{BreakerState: "open"}) != true {
		t.Errorf("IsOpen returned false for open state")
	}
	if b.IsOpen(db.StorageDestination{BreakerState: "closed"}) != false {
		t.Errorf("IsOpen returned true for closed state")
	}
}
