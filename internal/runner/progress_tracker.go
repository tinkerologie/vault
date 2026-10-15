package runner

import (
	"sync"
	"time"

	"github.com/tinkerologie/vault/internal/engine"
)

// Progress tracking (#456): turns engine.ProgressDetail reports into the
// phase, monotonic counters, transfer rate, and ETA the UI shows.

const (
	// rateSampleInterval is the minimum spacing between rate samples, so
	// bursty reads do not make the rate jump around.
	rateSampleInterval = time.Second
	// rateSmoothing is the EWMA weight given to each new sample.
	rateSmoothing = 0.3
	// etaMinElapsed and etaMinFraction gate the ETA: an estimate from the
	// first seconds of a phase, or the first sliver of its work, is noise.
	etaMinElapsed  = 30 * time.Second
	etaMinFraction = 0.01
	// reportInterval throttles same-phase reports: transfers report on every
	// read, which would otherwise take the status lock millions of times.
	reportInterval = 250 * time.Millisecond
)

// progressSnapshot is the tracker's view of the current item, copied into
// RunStatus and progress broadcasts.
type progressSnapshot struct {
	Phase          engine.Phase
	PhaseStartedAt time.Time
	BytesDone      int64
	BytesTotal     int64
	FilesDone      int64
	FilesTotal     int64
	Rate           float64 // bytes/s, or files/s when the phase has no byte data
	ETASeconds     int64   // 0 when no meaningful estimate exists
	Determinate    bool
	Percent        int // phase percentage; meaningful only when Determinate
	LastProgressAt time.Time
}

// progressTracker holds the progress state for the item currently running.
// Download workers report concurrently, so every method locks. A nil
// tracker (a Runner built without New, as some tests do) tracks nothing.
type progressTracker struct {
	mu  sync.Mutex
	now func() time.Time

	item string
	snap progressSnapshot

	sampleAt    time.Time
	sampleValue int64 // bytes (or files) at sampleAt
	rateOnFiles bool
}

func newProgressTracker(now func() time.Time) *progressTracker {
	if now == nil {
		now = time.Now
	}
	return &progressTracker{now: now}
}

// update applies a detail report for item and returns the new snapshot,
// whether the phase (or item) changed — the caller broadcasts phase changes
// immediately so they are never throttled away — and whether the report was
// applied at all. A same-phase report within reportInterval of the last one
// is throttled unless it completes the phase: its counters still fold into
// the snapshot (Runner.Status reads it), but rate, ETA and the throttle clock
// wait for the next applied report. An indeterminate phase's last file count
// is therefore never lost.
func (t *progressTracker) update(item string, d engine.ProgressDetail) (snap progressSnapshot, changed, applied bool) {
	if t == nil {
		return progressSnapshot{Phase: d.Phase, LastProgressAt: time.Now()}, false, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()

	changed = item != t.item || d.Phase != t.snap.Phase
	final := (d.BytesTotal > 0 && d.BytesDone >= d.BytesTotal) || (d.FilesTotal > 0 && d.FilesDone >= d.FilesTotal)
	if !changed && !final && now.Sub(t.snap.LastProgressAt) < reportInterval {
		t.foldCounts(d)
		return t.snap, false, false
	}
	if changed {
		// A new phase or item starts from scratch: counters, rate, and ETA
		// from the previous one would be meaningless here.
		t.item = item
		t.snap = progressSnapshot{Phase: d.Phase, PhaseStartedAt: now}
		t.sampleAt = time.Time{}
		t.rateOnFiles = false
	}

	done, total := t.foldCounts(d)
	s := &t.snap
	s.LastProgressAt = now
	t.sampleRate(now, done)
	s.ETASeconds = 0
	if s.Determinate && s.Rate > 0 && now.Sub(s.PhaseStartedAt) >= etaMinElapsed &&
		float64(done) >= etaMinFraction*float64(total) && done < total {
		s.ETASeconds = int64(float64(total-done)/s.Rate + 0.5)
	}
	return t.snap, changed, true
}

// foldCounts merges d's counters into the snapshot and recomputes the
// percentage, returning the done and total values in the rate's unit.
// The caller holds t.mu.
func (t *progressTracker) foldCounts(d engine.ProgressDetail) (done, total int64) {
	s := &t.snap
	// High-water marks: a retried upload restarts its byte count from zero,
	// but the displayed progress must never move backward.
	s.BytesDone = max(s.BytesDone, d.BytesDone)
	s.FilesDone = max(s.FilesDone, d.FilesDone)
	s.BytesTotal = max(s.BytesTotal, d.BytesTotal)
	s.FilesTotal = max(s.FilesTotal, d.FilesTotal)
	// Work beyond the expected total (a file that grew mid-backup, a
	// retried part) raises the total rather than overflowing it.
	if s.BytesTotal > 0 {
		s.BytesTotal = max(s.BytesTotal, s.BytesDone)
	}
	if s.FilesTotal > 0 {
		s.FilesTotal = max(s.FilesTotal, s.FilesDone)
	}

	// Measure in bytes once byte work is known, otherwise in files. A phase
	// can learn its file total late, so the unit is chosen per report; a
	// switch restarts rate sampling, as the old samples are in other units.
	onFiles := s.BytesDone == 0 && s.BytesTotal == 0 && (s.FilesDone > 0 || s.FilesTotal > 0)
	if onFiles != t.rateOnFiles {
		t.rateOnFiles = onFiles
		t.sampleAt = time.Time{}
		s.Rate = 0
	}
	done, total = s.BytesDone, s.BytesTotal
	if t.rateOnFiles {
		done, total = s.FilesDone, s.FilesTotal
	}
	s.Determinate = total > 0
	s.Percent = 0
	if s.Determinate {
		// A phase reads at most 99% until the next phase begins, so a
		// finished transfer still waiting on its commit never shows 100%.
		if done >= total {
			s.Percent = 99
		} else {
			s.Percent = min(int(done*100/total), 99)
		}
	}
	return done, total
}

// sampleRate folds a new (time, value) point into the EWMA rate.
func (t *progressTracker) sampleRate(now time.Time, value int64) {
	if t.sampleAt.IsZero() {
		t.sampleAt, t.sampleValue = now, value
		return
	}
	elapsed := now.Sub(t.sampleAt)
	if elapsed < rateSampleInterval {
		return
	}
	inst := float64(value-t.sampleValue) / elapsed.Seconds()
	if inst < 0 {
		inst = 0
	}
	if t.snap.Rate == 0 {
		t.snap.Rate = inst
	} else {
		t.snap.Rate = rateSmoothing*inst + (1-rateSmoothing)*t.snap.Rate
	}
	t.sampleAt, t.sampleValue = now, value
}

// snapshot returns the current state for item, or false when the tracker
// holds nothing for it.
func (t *progressTracker) snapshot(item string) (progressSnapshot, bool) {
	if t == nil {
		return progressSnapshot{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.item == "" || t.item != item {
		return progressSnapshot{}, false
	}
	return t.snap, true
}

// reset forgets all state, e.g. when the runner moves to another item.
func (t *progressTracker) reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.item = ""
	t.snap = progressSnapshot{}
	t.sampleAt = time.Time{}
	t.mu.Unlock()
}
