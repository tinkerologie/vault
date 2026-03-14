package scheduler

import (
	"log"
	"sync"

	"github.com/robfig/cron/v3"
	"github.com/ruaandeysel/vault/internal/db"
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
	replEntries       map[int64]cron.EntryID
	mu                sync.Mutex
}

// New creates a Scheduler for backup jobs.
func New(database *db.DB, runner JobRunner) *Scheduler {
	return &Scheduler{
		cron:        cron.New(),
		db:          database,
		runner:      runner,
		entries:     make(map[int64]cron.EntryID),
		replEntries: make(map[int64]cron.EntryID),
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
	entryID, err := s.cron.AddFunc(job.Schedule, func() {
		s.runner(jobID)
	})
	if err != nil {
		log.Printf("Failed to schedule job %d (%s): %v", job.ID, job.Name, err)
		return
	}
	s.entries[job.ID] = entryID
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
	entryID, ok := s.entries[jobID]
	if !ok {
		return "", false
	}
	entry := s.cron.Entry(entryID)
	return entry.Next.Format("2006-01-02 15:04:05"), true
}
