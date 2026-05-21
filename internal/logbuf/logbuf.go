// Package logbuf provides an in-memory byte ring buffer for capturing
// recent log output so it can be embedded in diagnostics bundles.
//
// The buffer is plugged into log.SetOutput at daemon startup via
// io.MultiWriter(os.Stderr, ring), so every line that hits the standard
// logger is mirrored into a fixed-capacity circular byte buffer. Snapshot
// returns the most recent N bytes in order.
//
// The buffer is byte-oriented (not line-oriented) for simplicity; a
// partial line at the head of the snapshot is acceptable for a
// diagnostics dump and matches what `tail -c N /var/log/vault.log`
// would produce. Callers that need line-aligned output can trim the
// returned slice at the first newline.
package logbuf

import "sync"

// Ring is a thread-safe fixed-capacity byte ring buffer.
type Ring struct {
	mu      sync.Mutex
	buf     []byte
	pos     int
	wrapped bool
}

// New creates a ring buffer with the given byte capacity. A capacity of
// 0 returns a nil-safe no-op buffer whose Write discards bytes and
// Snapshot returns an empty slice; this lets callers wire the buffer
// unconditionally and disable it via configuration.
func New(capBytes int) *Ring {
	if capBytes <= 0 {
		return &Ring{}
	}
	return &Ring{buf: make([]byte, capBytes)}
}

// Write appends p to the ring, evicting the oldest bytes when capacity
// is exceeded. Always returns len(p), nil so callers in an
// io.MultiWriter chain never see a short-write.
func (r *Ring) Write(p []byte) (int, error) {
	written := len(p)
	if r == nil || len(r.buf) == 0 {
		return written, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(p) > 0 {
		n := copy(r.buf[r.pos:], p)
		r.pos += n
		if r.pos == len(r.buf) {
			r.pos = 0
			r.wrapped = true
		}
		p = p[n:]
	}
	return written, nil
}

// Snapshot returns a copy of the buffer's contents in oldest-to-newest
// order. Safe to call concurrently with Write.
func (r *Ring) Snapshot() []byte {
	if r == nil || len(r.buf) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.wrapped {
		out := make([]byte, r.pos)
		copy(out, r.buf[:r.pos])
		return out
	}
	out := make([]byte, len(r.buf))
	// Tail (older) then head (newer).
	n := copy(out, r.buf[r.pos:])
	copy(out[n:], r.buf[:r.pos])
	return out
}
