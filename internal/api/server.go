package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ruaan-deysel/vault/internal/api/handlers"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/replication"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// ServerConfig holds configuration options for the API server.
type ServerConfig struct {
	Addr      string
	TLSCert   string
	TLSKey    string
	ServerKey []byte // AES-256 key for sealing secrets at rest.
	Version   string // Application version injected at build time.
	ReadOnly  bool   // When true, write endpoints return 403 (replica mode).
}

type Server struct {
	db          *db.DB
	hub         *ws.Hub
	runner      *runner.Runner
	syncer      *replication.Syncer
	router      *chi.Mux
	config      ServerConfig
	schedReload ScheduleReloader

	// nextRunResolver looks up the next scheduled run time for a job.
	nextRunResolver func(jobID int64) (string, bool)

	settingsHandler    *handlers.SettingsHandler
	browseHandler      *handlers.BrowseHandler
	jobHandler         *handlers.JobHandler
	storageHandler     *handlers.StorageHandler
	replicationHandler *handlers.ReplicationHandler

	// configChangeHook is called after any handler mutates persistent
	// configuration. It flushes the DB to USB flash.
	configChangeHook handlers.ConfigChangeHook

	// preShutdownHook is called immediately upon SIGTERM, before the
	// runner drain begins. Used by the daemon to flush the DB snapshot
	// + USB backup so configuration is safely on flash even if the
	// rc.vault SIGKILL escalation fires during the drain window
	// (issue #108).
	preShutdownHook func()

	// startupDiagnostics is a snapshot of startup-time facts (pool
	// mount status, hybrid mode, configuration validation result).
	// Exposed via /api/v1/health so support can diagnose persistence
	// issues from a single endpoint (issue #108).
	startupDiagnostics *StartupDiagnostics
}

// StartupDiagnostics carries the facts daemon startup learned about
// the environment and database state at boot. Read-only after the
// daemon finishes initialisation; surfaced verbatim by /health.
type StartupDiagnostics struct {
	HybridMode       bool                     `json:"hybrid_mode"`
	DetectedPool     string                   `json:"detected_pool,omitempty"`
	PoolMounted      bool                     `json:"pool_mounted"`
	PoolRetryWaitMs  int64                    `json:"pool_retry_wait_ms"`
	RestorationInfo  *db.RestorationInfo      `json:"restoration,omitempty"`
	HasConfiguration bool                     `json:"has_configuration"`
	Configuration    *db.ConfigurationSummary `json:"configuration,omitempty"`
	UnraidBootKind   string                   `json:"unraid_boot_kind,omitempty"` // "flash" | "internal" | "unknown"
}

func NewServer(database *db.DB, cfg ServerConfig) *Server {
	s := &Server{
		db:     database,
		hub:    ws.NewHub(),
		config: cfg,
	}
	s.runner = runner.New(database, s.hub, cfg.ServerKey)
	go s.hub.Run()
	s.router = s.setupRoutes()
	return s
}

// ScheduleReloader is a function that reloads the scheduler after job changes.
type ScheduleReloader = func() error

// SetScheduleReloader sets the function called to reload the scheduler
// after job CRUD operations. Must be called before serving requests.
func (s *Server) SetScheduleReloader(fn ScheduleReloader) {
	s.schedReload = fn
}

// SetNextRunResolver sets the function used by job handlers to look up next run times.
func (s *Server) SetNextRunResolver(fn func(jobID int64) (string, bool)) {
	s.nextRunResolver = fn
}

// SetConfigChangeHook registers a function called after any handler mutates
// persistent configuration (jobs, storage, settings, replication). The hook
// is forwarded to each CRUD handler so every mutation endpoint can fire it
// (typically used by the daemon to flush the DB snapshot to USB flash).
func (s *Server) SetConfigChangeHook(fn handlers.ConfigChangeHook) {
	s.configChangeHook = fn
	if s.jobHandler != nil {
		s.jobHandler.SetConfigChangeHook(fn)
	}
	if s.settingsHandler != nil {
		s.settingsHandler.SetConfigChangeHook(fn)
	}
	if s.storageHandler != nil {
		s.storageHandler.SetConfigChangeHook(fn)
	}
	if s.replicationHandler != nil {
		s.replicationHandler.SetConfigChangeHook(fn)
	}
}

// SetPreShutdownHook registers a function called immediately on SIGTERM,
// before the runner drain. The daemon uses this to flush the DB snapshot
// and USB backup so configuration survives even when rc.vault escalates
// to SIGKILL during a long-running drain (issue #108). The hook runs
// synchronously in the shutdown goroutine; keep it under a few seconds.
func (s *Server) SetPreShutdownHook(fn func()) {
	s.preShutdownHook = fn
}

// SetStartupDiagnostics records boot-time persistence facts that the
// daemon learned during initialisation. Exposed via /api/v1/health so
// support can verify configuration survived an Unraid restart/upgrade
// without parsing the daemon log (issue #108).
func (s *Server) SetStartupDiagnostics(d *StartupDiagnostics) {
	s.startupDiagnostics = d
}

// Hub returns the WebSocket hub for external use (e.g., scheduler).
func (s *Server) Hub() *ws.Hub {
	return s.hub
}

// Runner returns the backup runner for external use (e.g., scheduler).
func (s *Server) Runner() *runner.Runner {
	return s.runner
}

// SetReplicationSyncer sets the replication syncer for use by API handlers.
func (s *Server) SetReplicationSyncer(syncer *replication.Syncer) {
	s.syncer = syncer
}

// Syncer returns the replication syncer for use by API handlers.
func (s *Server) Syncer() *replication.Syncer {
	return s.syncer
}

// SettingsHandler returns the settings handler for external configuration.
func (s *Server) SettingsHandler() *handlers.SettingsHandler {
	return s.settingsHandler
}

// BrowseHandler returns the browse handler for external configuration.
func (s *Server) BrowseHandler() *handlers.BrowseHandler {
	return s.browseHandler
}

func (s *Server) StartWithContext(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.config.Addr,
		Handler:           s.router,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		// Phase 0: persist configuration to flash immediately. The
		// runner drain below can take up to 30 s, and rc.vault may
		// escalate to SIGKILL before drain completes; flushing the DB
		// snapshot and USB backup first guarantees that even a hard
		// kill leaves a current configuration on the /boot/ flash drive
		// (issue #108).
		if s.preShutdownHook != nil {
			start := time.Now()
			s.preShutdownHook()
			log.Printf("server shutdown: pre-drain flush completed in %v", time.Since(start).Round(time.Millisecond))
		}

		// Phase 1: drain the runner — let the active per-file upload finish.
		// 30 s ceiling; if exceeded, the existing context cancel still kills
		// mid-flight work.
		drainCtx, drainCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		if err := s.runner.Drain(drainCtx); err != nil {
			log.Printf("server shutdown: runner drain incomplete: %v", err)
		} else {
			log.Println("server shutdown: runner drained cleanly")
		}
		drainCancel()

		// Phase 2: HTTP server shutdown.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("server shutdown error: %v", err)
		}
	}()

	if s.config.TLSCert != "" && s.config.TLSKey != "" {
		log.Printf("Vault API server listening on %s (TLS)", s.config.Addr)
		return srv.ListenAndServeTLS(s.config.TLSCert, s.config.TLSKey)
	}
	log.Printf("Vault API server listening on %s", s.config.Addr)
	return srv.ListenAndServe()
}

func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Warning: failed to write JSON response: %v", err)
	}
}
