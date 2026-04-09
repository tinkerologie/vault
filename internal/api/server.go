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

	settingsHandler *handlers.SettingsHandler
	browseHandler   *handlers.BrowseHandler

	// configChangeHook is called after any handler mutates persistent
	// configuration. It flushed the DB to USB flash.
	configChangeHook handlers.ConfigChangeHook
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
// runs in a goroutine to avoid blocking the HTTP response.
func (s *Server) SetConfigChangeHook(fn handlers.ConfigChangeHook) {
	s.configChangeHook = fn
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

func (s *Server) Start() error {
	srv := &http.Server{
		Addr:         s.config.Addr,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	if s.config.TLSCert != "" && s.config.TLSKey != "" {
		log.Printf("Vault API server listening on %s (TLS)", s.config.Addr)
		return srv.ListenAndServeTLS(s.config.TLSCert, s.config.TLSKey)
	}
	log.Printf("Vault API server listening on %s", s.config.Addr)
	return srv.ListenAndServe()
}

func (s *Server) StartWithContext(ctx context.Context) error {
	srv := &http.Server{
		Addr:         s.config.Addr,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		<-ctx.Done()
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
