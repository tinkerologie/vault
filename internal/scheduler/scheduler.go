package scheduler

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/ruaan-deysel/vault/internal/db"
)

// JobRunner is called when a backup job is due.
type JobRunner func(jobID int64)

// ReplicationRunner is called when a replication source sync is due.
type ReplicationRunner func(sourceID int64)

// HealthChecker is called on a daily cron tick to verify every configured
// storage destination is still reachable. The hook lives on Scheduler so
// the daemon can wire in Runner.RunHealthChecks without scheduler depending
// on the runner package.
type HealthChecker func()

// VerifyRunner is called when a job's scheduled verification is due. The
// daemon wires this in to Runner.RunScheduledVerify so the scheduler does
// not need to depend on the runner package.
type VerifyRunner func(jobID int64, mode string)

// RetryDispatcher is called once per due retry. The daemon wires this
// into Runner.RunJobRetry. Kept as a hook so scheduler does not depend
// on the runner package.
type RetryDispatcher func(jobID, originalRunID int64, attempt int)

// Scheduler manages cron entries for backup jobs and replication sources.
type Scheduler struct {
	cron              *cron.Cron
	db                *db.DB
	runner            JobRunner
	replicationRunner ReplicationRunner
	healthChecker     HealthChecker
	verifyRunner      VerifyRunner
	retryDispatcher   RetryDispatcher
	entries           map[int64]cron.EntryID
	lastDayEntries    map[int64]cron.EntryID // daily-trigger entries for L (last day) schedules
	verifyEntries     map[int64]cron.EntryID
	replEntries       map[int64]cron.EntryID
	mu                sync.Mutex
}

// New creates a Scheduler for backup jobs.
func New(database *db.DB, runner JobRunner) *Scheduler {
	return &Scheduler{
		cron:           cron.New(),
		db:             database,
		runner:         runner,
		entries:        make(map[int64]cron.EntryID),
		lastDayEntries: make(map[int64]cron.EntryID),
		verifyEntries:  make(map[int64]cron.EntryID),
		replEntries:    make(map[int64]cron.EntryID),
	}
}

// SetReplicationRunner sets the callback for replication sync scheduling.
func (s *Scheduler) SetReplicationRunner(fn ReplicationRunner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replicationRunner = fn
}

// SetHealthChecker installs the daily storage-destination health check hook.
// Must be called before Start() for the daily cron entry to be registered.
func (s *Scheduler) SetHealthChecker(fn HealthChecker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.healthChecker = fn
}

// SetVerifyRunner installs the per-job scheduled-verification callback.
// Must be called before Start() / Reload() for verify entries to register.
func (s *Scheduler) SetVerifyRunner(fn VerifyRunner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verifyRunner = fn
}

// SetRetryDispatcher installs the callback used by the retry watcher.
// Must be called before Start() for the watcher cron entry to register.
func (s *Scheduler) SetRetryDispatcher(fn RetryDispatcher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryDispatcher = fn
}

func (s *Scheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs, err := s.db.ListJobs()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.Enabled && job.Schedule != "" {
			s.addJob(job)
		}
		// Scheduled verification is independent of the backup schedule:
		// a user can run nightly backups but only verify weekly.
		if job.Enabled && job.VerifySchedule != "" && s.verifyRunner != nil {
			s.addVerifyJob(job)
		}
	}

	// Schedule replication sources.
	if err := s.loadReplicationSources(); err != nil {
		log.Printf("Warning: failed to load replication sources: %v", err)
	}

	// Daily storage-destination health check (Feature F). Runs at 03:30
	// local time \xe2\x80\x94 deliberately after typical backup windows so a
	// failing destination is caught before the next morning's backup.
	if s.healthChecker != nil {
		if _, err := s.cron.AddFunc("30 3 * * *", s.healthChecker); err != nil {
			log.Printf("Warning: failed to schedule daily storage health check: %v", err)
		}
	}

	// Retry watcher: every minute, claim due retries and dispatch them.
	if s.retryDispatcher != nil {
		if _, err := s.cron.AddFunc("@every 1m", s.fireDueRetries); err != nil {
			log.Printf("Warning: failed to schedule retry watcher: %v", err)
		}
	}

	s.cron.Start()
	log.Printf("Scheduler started with %d jobs, %d replication sources", len(s.entries), len(s.replEntries))
	return nil
}

func (s *Scheduler) Stop() {
	s.cron.Stop()
}

func (s *Scheduler) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove all existing job entries.
	for jobID, entryID := range s.entries {
		s.cron.Remove(entryID)
		delete(s.entries, jobID)
	}

	// Remove all existing last-day entries.
	for jobID, entryID := range s.lastDayEntries {
		s.cron.Remove(entryID)
		delete(s.lastDayEntries, jobID)
	}

	// Remove all existing verify entries.
	for jobID, entryID := range s.verifyEntries {
		s.cron.Remove(entryID)
		delete(s.verifyEntries, jobID)
	}

	// Remove all existing replication entries.
	for srcID, entryID := range s.replEntries {
		s.cron.Remove(entryID)
		delete(s.replEntries, srcID)
	}

	// Reload jobs from DB.
	jobs, err := s.db.ListJobs()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.Enabled && job.Schedule != "" {
			s.addJob(job)
		}
		if job.Enabled && job.VerifySchedule != "" && s.verifyRunner != nil {
			s.addVerifyJob(job)
		}
	}

	// Reload replication sources.
	if err := s.loadReplicationSources(); err != nil {
		log.Printf("Warning: failed to reload replication sources: %v", err)
	}
	return nil
}

func (s *Scheduler) addJob(job db.Job) {
	jobID := job.ID

	// Check for L (last day of month) in the day-of-month field.
	if schedule, ok := parseLastDaySchedule(job.Schedule); ok {
		entryID, err := s.cron.AddFunc(schedule, func() {
			if isLastDayOfMonth(time.Now()) {
				s.runner(jobID)
			}
		})
		if err != nil {
			log.Printf("Failed to schedule last-day job %d (%s): %v", job.ID, job.Name, err)
			return
		}
		s.lastDayEntries[job.ID] = entryID
		return
	}

	entryID, err := s.cron.AddFunc(job.Schedule, func() {
		s.runner(jobID)
	})
	if err != nil {
		log.Printf("Failed to schedule job %d (%s): %v", job.ID, job.Name, err)
		return
	}
	s.entries[job.ID] = entryID
}

// addVerifyJob registers the per-job scheduled verification cron entry.
// The runner picks the latest restore point and runs verify in the
// configured mode. "" mode defaults to "quick".
func (s *Scheduler) addVerifyJob(job db.Job) {
	jobID := job.ID
	mode := job.VerifyMode
	if mode == "" {
		mode = "quick"
	}
	entryID, err := s.cron.AddFunc(job.VerifySchedule, func() {
		s.verifyRunner(jobID, mode)
	})
	if err != nil {
		log.Printf("Failed to schedule verify for job %d (%s): %v", job.ID, job.Name, err)
		return
	}
	s.verifyEntries[job.ID] = entryID
}

// parseLastDaySchedule checks if a cron string contains L in the day-of-month
// field and returns a daily equivalent schedule (e.g. "0 2 L * *" → "0 2 * * *").
func parseLastDaySchedule(schedule string) (string, bool) {
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return "", false
	}
	if fields[2] != "L" {
		return "", false
	}
	// Replace L with * to create a daily trigger at the same time.
	fields[2] = "*"
	return strings.Join(fields, " "), true
}

// isLastDayOfMonth reports whether t falls on the last day of its month.
func isLastDayOfMonth(t time.Time) bool {
	tomorrow := t.AddDate(0, 0, 1)
	return tomorrow.Month() != t.Month()
}

// replicationEnabled reports whether scheduled replication should run. An
// explicit replication_enabled setting wins; when unset it derives from
// whether any replication sources exist (so existing users keep replication
// and fresh installs start hidden). Mirrors the UI derive in settings.svelte.js.
func replicationEnabled(d *db.DB) bool {
	v, _ := d.GetSetting("replication_enabled", "")
	switch v {
	case "true":
		return true
	case "false":
		return false
	}
	sources, err := d.ListReplicationSources()
	if err != nil {
		return false
	}
	return len(sources) > 0
}

// loadReplicationSources loads enabled replication sources into the cron scheduler.
func (s *Scheduler) loadReplicationSources() error {
	if s.replicationRunner == nil {
		return nil
	}

	if !replicationEnabled(s.db) {
		log.Printf("scheduler: replication disabled — skipping replication source scheduling")
		return nil
	}

	sources, err := s.db.ListReplicationSources()
	if err != nil {
		return err
	}
	for _, src := range sources {
		if src.Enabled && src.Schedule != "" {
			s.addReplicationSource(src)
		}
	}
	return nil
}

func (s *Scheduler) addReplicationSource(src db.ReplicationSource) {
	srcID := src.ID
	entryID, err := s.cron.AddFunc(src.Schedule, func() {
		s.replicationRunner(srcID)
	})
	if err != nil {
		log.Printf("Failed to schedule replication source %d (%s): %v", src.ID, src.Name, err)
		return
	}
	s.replEntries[src.ID] = entryID
}

// fireDueRetries is the cron tick body for the retry watcher. It claims
// every job_run whose retry_next_at has expired and dispatches each one
// via the configured RetryDispatcher.
func (s *Scheduler) fireDueRetries() {
	claimed, err := s.db.ClaimDueRetries()
	if err != nil {
		log.Printf("retry watcher: %v", err)
		return
	}
	for _, c := range claimed {
		log.Printf("retry watcher: dispatching job %d retry attempt %d (orig run %d)",
			c.JobID, c.AttemptSoFar+1, c.OriginalRunID)
		// Goroutine so a slow runner.RunJobRetry doesn't block the cron
		// loop for other retries. RunJobRetry itself is serialised by
		// the runner's internal mutex.
		c := c // capture
		go s.retryDispatcher(c.JobID, c.OriginalRunID, c.AttemptSoFar+1)
	}
}

func (s *Scheduler) NextRun(jobID int64) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check standard entries first, then last-day entries.
	entryID, ok := s.entries[jobID]
	if !ok {
		entryID, ok = s.lastDayEntries[jobID]
	}
	if !ok {
		return "", false
	}
	entry := s.cron.Entry(entryID)
	return entry.Next.Format("2006-01-02 15:04:05"), true
}
