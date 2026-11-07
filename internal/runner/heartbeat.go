package runner

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Heartbeat writes a timestamp + daemon version to a file at a fixed
// interval. External monitoring (Unraid notifications, monit, custom
// watchdog) can detect a hung daemon by checking the file mtime.
type Heartbeat struct {
	path     string
	version  string
	interval time.Duration

	mu        sync.Mutex
	lastWarn  time.Time
	warnEvery time.Duration

	// Tests may replace writeFile before Start, never while a writer is running.
	writeFile func(string, []byte, os.FileMode) error
}

// NewHeartbeat constructs a Heartbeat. The default warn-suppression window
// is 1 hour — write errors are logged at most that often to avoid log spam.
func NewHeartbeat(path, version string, interval time.Duration) *Heartbeat {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Heartbeat{
		path:      path,
		version:   version,
		interval:  interval,
		warnEvery: time.Hour,
		writeFile: os.WriteFile,
	}
}

// Start writes immediately, including for an already-cancelled context, then
// writes on each interval until ctx is cancelled. Cancellation does not interrupt
// an in-flight write. The returned channel closes after this writer has exited
// and stopped its ticker. Write failures are rate-limited to one log per warnEvery.
func (h *Heartbeat) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Immediate first write so external monitors see liveness right
		// away, not after one interval.
		h.write()
		ticker := time.NewTicker(h.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.write()
			}
		}
	}()
	return done
}

func (h *Heartbeat) write() {
	if err := os.MkdirAll(filepath.Dir(h.path), 0o750); err != nil {
		h.warn("mkdir", err)
		return
	}
	payload := fmt.Sprintf("%s v%s\n",
		time.Now().UTC().Format(time.RFC3339), h.version)
	if err := h.writeFile(h.path, []byte(payload), 0o640); err != nil {
		h.warn("write", err)
		return
	}
}

func (h *Heartbeat) warn(op string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Since(h.lastWarn) < h.warnEvery {
		return
	}
	h.lastWarn = time.Now()
	log.Printf("heartbeat: %s %s failed: %v", op, h.path, err)
}
