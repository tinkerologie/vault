package runner

import (
	"log"
	"sync"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// Breaker is a per-destination circuit breaker.
//
// Closed: requests pass.
// Open:   runner.RunJob short-circuits with a "skipped" run.
//
// Closed -> Open transition fires when consecutive_failures (persisted on
// storage_destinations) reaches failThreshold.
//
// Open -> Closed transition fires after closeSuccesses consecutive
// successes (in-memory counter — not persisted; a daemon restart
// loses the count but the daily health check re-establishes it).
//
// Manual close via Breaker.ManualClose resets state immediately.
type Breaker struct {
	failThreshold  int
	closeSuccesses int

	mu                   sync.Mutex
	consecutiveSuccesses map[int64]int // dest.ID -> count while in open state
}

// NewBreaker constructs a Breaker with the given thresholds.
func NewBreaker(failThreshold, closeSuccesses int) *Breaker {
	if failThreshold <= 0 {
		failThreshold = 3
	}
	if closeSuccesses <= 0 {
		closeSuccesses = 2
	}
	return &Breaker{
		failThreshold:        failThreshold,
		closeSuccesses:       closeSuccesses,
		consecutiveSuccesses: make(map[int64]int),
	}
}

// NextState is a pure helper for tests. Given the current state and the
// failure count AFTER incrementing for this failure, returns the new
// state, the failure count, and whether it transitioned to open.
func (b *Breaker) NextState(currentState string, newFailCount int, success bool) (state string, count int, opened bool) {
	if success {
		return "closed", 0, false
	}
	if currentState == "open" {
		return "open", newFailCount, false
	}
	if newFailCount >= b.failThreshold {
		return "open", newFailCount, true
	}
	return "closed", newFailCount, false
}

// RecordFailure persists an incremented failure count for the destination
// and opens the breaker if the threshold is reached. Returns the new
// state and whether the breaker just transitioned to open.
func (b *Breaker) RecordFailure(database *db.DB, dest db.StorageDestination) (state string, opened bool) {
	newCount := dest.ConsecutiveFailures + 1
	state, _, opened = b.NextState(dest.BreakerState, newCount, false)
	if opened {
		if err := database.OpenBreaker(dest.ID, newCount); err != nil {
			log.Printf("breaker: OpenBreaker(%d): %v", dest.ID, err)
		}
		b.resetSuccessCounter(dest.ID)
		return
	}
	if err := database.RecordDestinationFailure(dest.ID, newCount); err != nil {
		log.Printf("breaker: RecordDestinationFailure(%d): %v", dest.ID, err)
	}
	return
}

// RecordSuccess resets the failure count. If the breaker was open, tracks
// consecutive successes and closes once the threshold is reached.
func (b *Breaker) RecordSuccess(database *db.DB, dest db.StorageDestination) (state string) {
	state, _, _ = b.recordSuccessFor(dest.ID, dest.BreakerState)
	if state == "closed" {
		if err := database.CloseBreaker(dest.ID); err != nil {
			log.Printf("breaker: CloseBreaker(%d): %v", dest.ID, err)
		}
	} else {
		if err := database.RecordDestinationSuccess(dest.ID); err != nil {
			log.Printf("breaker: RecordDestinationSuccess(%d): %v", dest.ID, err)
		}
	}
	return
}

// ManualClose forcibly closes the breaker and resets all counters.
func (b *Breaker) ManualClose(database *db.DB, destID int64) error {
	b.resetSuccessCounter(destID)
	return database.CloseBreaker(destID)
}

// IsOpen reports whether the destination's breaker is currently open.
func (b *Breaker) IsOpen(dest db.StorageDestination) bool {
	return dest.BreakerState == "open"
}

func (b *Breaker) recordSuccessFor(destID int64, currentState string) (string, int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if currentState != "open" {
		delete(b.consecutiveSuccesses, destID)
		return "closed", 0, false
	}
	b.consecutiveSuccesses[destID]++
	count := b.consecutiveSuccesses[destID]
	if count >= b.closeSuccesses {
		delete(b.consecutiveSuccesses, destID)
		return "closed", count, true
	}
	return "open", count, false
}

func (b *Breaker) resetSuccessCounter(destID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.consecutiveSuccesses, destID)
}

// BreakerOpenError describes a refused operation. Wrapping caller can
// errors.Is against it. Used by handlers if needed.
type BreakerOpenError struct {
	DestID   int64
	DestName string
	OpenedAt time.Time
}

func (e *BreakerOpenError) Error() string {
	if e.OpenedAt.IsZero() {
		return "destination breaker is open"
	}
	return "destination breaker is open since " + e.OpenedAt.Format(time.RFC3339)
}
