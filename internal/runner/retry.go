package runner

import (
	"encoding/json"
	"log"

	"github.com/ruaan-deysel/vault/internal/db"
)

// RetryPolicy describes how many times a failed run should be retried
// and the wait (seconds) before each attempt. Delays[0] is the wait
// before the FIRST retry; attempts beyond len(Delays) reuse the last
// entry.
type RetryPolicy struct {
	Max    int
	Delays []int // seconds
}

// NextDelay returns the wait (seconds) before the given retry attempt.
// attempt is the 0-indexed retry number (0 = first retry).
// Clamps to the last entry if attempt exceeds len(Delays).
func (p RetryPolicy) NextDelay(attempt int) int {
	if len(p.Delays) == 0 {
		return 0
	}
	if attempt < 0 {
		return p.Delays[0]
	}
	if attempt >= len(p.Delays) {
		return p.Delays[len(p.Delays)-1]
	}
	return p.Delays[attempt]
}

// resolveRetryPolicy merges global defaults with a job's per-job overrides.
// Per-job override is independent for each field: if RetryMaxOverride is
// valid, the global Max is ignored; same for Delays. Invalid JSON in the
// override falls back to global with a warning log.
func resolveRetryPolicy(job db.Job, globalMax int, globalDelays []int) RetryPolicy {
	p := RetryPolicy{Max: globalMax, Delays: globalDelays}
	if job.RetryMaxOverride != nil {
		p.Max = int(*job.RetryMaxOverride)
	}
	if job.RetryDelaysOverride != nil && *job.RetryDelaysOverride != "" {
		var delays []int
		if err := json.Unmarshal([]byte(*job.RetryDelaysOverride), &delays); err != nil {
			log.Printf("retry: job %d has invalid retry_delays_override %q: %v",
				job.ID, *job.RetryDelaysOverride, err)
		} else {
			p.Delays = delays
		}
	}
	return p
}

// parseGlobalDelays parses the retry_delays_default settings string into
// []int. Returns nil and logs on parse error.
func parseGlobalDelays(s string) []int {
	if s == "" {
		return nil
	}
	var out []int
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		log.Printf("retry: invalid retry_delays_default %q: %v", s, err)
		return nil
	}
	return out
}
