package runner

import (
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// scheduleRetryIfDue sets run.RetryNextAt if the run is eligible for a
// retry. Eligibility:
//   - not a manual run (opts.manual=false)
//   - destination breaker is closed (avoid retry storm into open breaker)
//   - retry_attempt < policy.Max
//
// Mutates run in place. Caller must persist via UpdateJobRun afterwards.
// The scheduler watcher (Task 8) polls retry_next_at to re-fire runs.
func (r *Runner) scheduleRetryIfDue(run *db.JobRun, job db.Job, dest db.StorageDestination, opts runOptions) {
	if opts.manual {
		return
	}
	if dest.BreakerState == "open" {
		return
	}
	globalMax, _ := r.db.GetSettingInt("retry_max_default", 2)
	globalDelaysStr, _ := r.db.GetSetting("retry_delays_default", "[900,3600,14400]")
	globalDelays := parseGlobalDelays(globalDelaysStr)
	if globalDelays == nil {
		globalDelays = []int{900, 3600, 14400}
	}
	policy := resolveRetryPolicy(job, globalMax, globalDelays)
	if run.RetryAttempt >= policy.Max {
		return
	}
	delay := policy.NextDelay(run.RetryAttempt)
	t := time.Now().Add(time.Duration(delay) * time.Second)
	run.RetryNextAt = &t
}
