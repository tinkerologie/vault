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

// Scheduler manages cron entries for backup jobs and replication sources.
type Scheduler struct {
	cron              *cron.Cron
	db                *db.DB
	runner            JobRunner
	replicationRunner ReplicationRunner
	entries           map[int64]cron.EntryID
	lastDayEntries    map[int64]cron.EntryID // daily-trigger entries for L (last day) schedules
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
		replEntries:    make(map[int64]cron.EntryID),
	}
}

// SetReplicationRunner sets the callback for replication sync scheduling.
func (s *Scheduler) SetReplicationRunner(fn ReplicationRunner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replicationRunner = fn
}

func (s *Scheduler) Start() error {
	jobs, err := s.db.ListJobs()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.Enabled && job.Schedule != "" {
			s.addJob(job)
		}
	}

	// Schedule replication sources.
	if err := s.loadReplicationSources(); err != nil {
		log.Printf("Warning: failed to load replication sources: %v", err)
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

// loadReplicationSources loads enabled replication sources into the cron scheduler.
func (s *Scheduler) loadReplicationSources() error {
	if s.replicationRunner == nil {
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
