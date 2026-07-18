package storage

import (
	"sync"
	"sync/atomic"

	"golang.org/x/time/rate"
)

// Adaptive ("auto") throttle: one process-wide token bucket shared by every
// network adapter, whose limit is adjusted at runtime by the runner's control
// loop (issue #237). This is deliberately separate from the per-destination
// static throttle_mbps cap — the two compose, with the stricter limit
// winning naturally since both wrap the same read path.
//
// The same wrapper counts Vault's own uploaded bytes so the control loop can
// subtract them from interface totals to estimate EXTERNAL traffic (Plex
// remote streams, etc.).

var (
	autoMu       sync.Mutex
	autoLimiter  *rate.Limiter
	autoUploaded atomic.Int64
)

// SetAutoThrottleLimit installs or retunes the global adaptive limit.
// bytesPerSec <= 0 disables the adaptive throttle entirely.
func SetAutoThrottleLimit(bytesPerSec float64) {
	autoMu.Lock()
	defer autoMu.Unlock()
	if bytesPerSec <= 0 {
		autoLimiter = nil
		return
	}
	if autoLimiter == nil {
		autoLimiter = rate.NewLimiter(rate.Limit(bytesPerSec), int(bytesPerSec))
	} else {
		autoLimiter.SetLimit(rate.Limit(bytesPerSec))
		autoLimiter.SetBurst(int(bytesPerSec))
	}
}

// AutoThrottleUploadedBytes returns the cumulative bytes Vault has pushed
// through auto-throttled adapters since daemon start.
func AutoThrottleUploadedBytes() int64 { return autoUploaded.Load() }

func currentAutoLimiter() *rate.Limiter {
	autoMu.Lock()
	defer autoMu.Unlock()
	return autoLimiter
}

// wrapAutoThrottled applies the global adaptive limiter to a network
// adapter. No-op (returns adapter unchanged) while the adaptive throttle is
// disabled — the check is per-call inside the reader so newly created
// adapters pick up runtime enable/disable without reconnection.
func wrapAutoThrottled(adapter Adapter) Adapter {
	if adapter == nil {
		return adapter
	}
	return &throttledAdapter{inner: adapter, limiter: nil, dynamic: true}
}
