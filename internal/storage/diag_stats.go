package storage

import (
	"context"
	"path"
	"sync"
	"sync/atomic"
)

// DefaultDiagEventCap bounds how many individual storage events a DiagStats
// forwards to its callback. A flapping destination can retry thousands of
// times; past the cap only the counters move, so a run log stays readable.
const DefaultDiagEventCap = 20

// DiagEvent describes one recovered storage failure (a retry, a mid-stream
// resume, or a parallel-download part restart).
type DiagEvent struct {
	Op          string // "Read", "WriteFrom", "resume", "part", ...
	Object      string // object base name only, never the full remote path
	Attempt     int    // the attempt that failed (1-based)
	MaxAttempts int
	Err         string // raw error text; callers must redact before persisting
	// Suppressed marks the single notice sent when the event cap is first
	// exceeded; the other fields are empty. Later events only move counters.
	Suppressed bool
}

// DiagSnapshot is a point-in-time copy of a DiagStats' counters.
type DiagSnapshot struct {
	Retries          int64            // retry attempts scheduled by the retry middleware
	TransientErrors  int64            // transient failures seen (retried or not)
	TransientByOp    map[string]int64 // transient failures per operation
	Resumes          int64            // mid-stream resumes by the resumable reader
	PartRestarts     int64            // parallel-download parts restarted
	BytesRead        int64            // bytes delivered to the caller by instrumented readers
	RefetchedBytes   int64            // bytes read again because a part restarted
	SuppressedEvents int64            // events dropped after the event cap
}

// DiagStats collects storage-layer facts for one backup or restore run so the
// runner can report retries, resumes, and bytes actually read. It travels on
// the run's context (WithDiagStats) and through storage.Options.Diag; every
// method is safe for concurrent use and a no-op on a nil receiver, so code
// paths without a collector behave exactly as before.
type DiagStats struct {
	retries      atomic.Int64
	transient    atomic.Int64
	resumes      atomic.Int64
	partRestarts atomic.Int64
	bytesRead    atomic.Int64
	refetched    atomic.Int64
	suppressed   atomic.Int64

	mu      sync.Mutex
	byOp    map[string]int64
	events  int
	cap     int
	onEvent func(DiagEvent)
}

// NewDiagStats returns a collector that forwards up to eventCap events to
// onEvent (nil to only count). eventCap <= 0 uses DefaultDiagEventCap.
func NewDiagStats(onEvent func(DiagEvent), eventCap int) *DiagStats {
	if eventCap <= 0 {
		eventCap = DefaultDiagEventCap
	}
	return &DiagStats{byOp: map[string]int64{}, cap: eventCap, onEvent: onEvent}
}

type diagStatsKey struct{}

// WithDiagStats attaches d to ctx.
func WithDiagStats(ctx context.Context, d *DiagStats) context.Context {
	return context.WithValue(ctx, diagStatsKey{}, d)
}

// DiagStatsFrom returns the collector on ctx, or nil.
func DiagStatsFrom(ctx context.Context) *DiagStats {
	d, _ := ctx.Value(diagStatsKey{}).(*DiagStats)
	return d
}

// Snapshot copies the current counters.
func (d *DiagStats) Snapshot() DiagSnapshot {
	if d == nil {
		return DiagSnapshot{}
	}
	d.mu.Lock()
	byOp := make(map[string]int64, len(d.byOp))
	for k, v := range d.byOp {
		byOp[k] = v
	}
	d.mu.Unlock()
	return DiagSnapshot{
		Retries:          d.retries.Load(),
		TransientErrors:  d.transient.Load(),
		TransientByOp:    byOp,
		Resumes:          d.resumes.Load(),
		PartRestarts:     d.partRestarts.Load(),
		BytesRead:        d.bytesRead.Load(),
		RefetchedBytes:   d.refetched.Load(),
		SuppressedEvents: d.suppressed.Load(),
	}
}

// recordTransient counts a transient failure of op and, when it will be
// retried, a retry attempt; then forwards the event subject to the cap.
func (d *DiagStats) recordTransient(op, object string, attempt, maxAttempts int, err error, retrying bool) {
	if d == nil {
		return
	}
	d.transient.Add(1)
	if retrying {
		d.retries.Add(1)
	}
	d.mu.Lock()
	d.byOp[op]++
	d.mu.Unlock()
	if retrying {
		d.emit(DiagEvent{Op: op, Object: path.Base(object), Attempt: attempt, MaxAttempts: maxAttempts, Err: errText(err)})
	}
}

// recordResume counts a mid-stream resume of object.
func (d *DiagStats) recordResume(object string, attempt, maxAttempts int, err error) {
	if d == nil {
		return
	}
	d.resumes.Add(1)
	d.recordTransient("resume", object, attempt, maxAttempts, err, false)
	d.emit(DiagEvent{Op: "resume", Object: path.Base(object), Attempt: attempt, MaxAttempts: maxAttempts, Err: errText(err)})
}

// recordPartRestart counts a restarted parallel-download part and the bytes
// of the failed attempt, which will be fetched again.
func (d *DiagStats) recordPartRestart(object string, attempt, maxAttempts int, err error, refetched int64) {
	if d == nil {
		return
	}
	d.partRestarts.Add(1)
	d.refetched.Add(refetched)
	d.recordTransient("part", object, attempt, maxAttempts, err, false)
	d.emit(DiagEvent{Op: "part", Object: path.Base(object), Attempt: attempt, MaxAttempts: maxAttempts, Err: errText(err)})
}

// AddBytesRead counts bytes delivered to a caller. Exported for readers
// outside this package (the dedup restore path reads chunks through its
// repo rather than through an instrumented storage reader).
func (d *DiagStats) AddBytesRead(n int64) {
	if d == nil || n <= 0 {
		return
	}
	d.bytesRead.Add(n)
}

func (d *DiagStats) emit(ev DiagEvent) {
	d.mu.Lock()
	if d.events >= d.cap {
		fn := d.onEvent
		d.mu.Unlock()
		if d.suppressed.Add(1) == 1 && fn != nil {
			fn(DiagEvent{Suppressed: true})
		}
		return
	}
	d.events++
	fn := d.onEvent
	d.mu.Unlock()
	if fn != nil {
		fn(ev)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
