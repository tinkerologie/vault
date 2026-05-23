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
	}
}

// Start begins writing the heartbeat file. The goroutine exits when ctx
// is cancelled. Write failures are rate-limited to one log line per
// warnEvery so a chronically failing path does not spam syslog.
func (h *Heartbeat) Start(ctx context.Context) {
	go func() {
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
}

func (h *Heartbeat) write() {
	if err := os.MkdirAll(filepath.Dir(h.path), 0o750); err != nil {
		h.warn("mkdir", err)
		return
	}
	payload := fmt.Sprintf("%s v%s\n",
		time.Now().UTC().Format(time.RFC3339), h.version)
	if err := os.WriteFile(h.path, []byte(payload), 0o640); err != nil {
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
