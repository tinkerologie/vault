package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ruaandeysel/vault/internal/api/handlers"
	"github.com/ruaandeysel/vault/internal/crypto"
	"github.com/ruaandeysel/vault/internal/db"
	"github.com/ruaandeysel/vault/internal/replication"
	"github.com/ruaandeysel/vault/internal/runner"
	"github.com/ruaandeysel/vault/internal/ws"
)

// ServerConfig holds configuration options for the API server.
type ServerConfig struct {
	Addr      string
	APIKey    string
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

	// cachedKey holds the resolved API key (from DB or CLI fallback).
	// Protected by keyMu; refreshed lazily by keyResolver().
	cachedKey string
	keyExpiry time.Time
	keyMu     sync.Mutex
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

// keyResolver returns a KeyResolver that checks the DB for an API key (cached
// for 5 seconds) and falls back to the static config key.
func (s *Server) keyResolver() KeyResolver {
	const ttl = 5 * time.Second
	return func() string {
		s.keyMu.Lock()
		defer s.keyMu.Unlock()

		if time.Now().Before(s.keyExpiry) {
			return s.cachedKey
		}

		// Try DB-stored key first.
		sealed, _ := s.db.GetSetting("api_key_sealed", "")
		if sealed != "" && len(s.config.ServerKey) > 0 {
			if key, err := crypto.Unseal(s.config.ServerKey, sealed); err == nil {
				s.cachedKey = key
				s.keyExpiry = time.Now().Add(ttl)
				return s.cachedKey
			}
		}

		// Fall back to static CLI key.
		s.cachedKey = s.config.APIKey
		s.keyExpiry = time.Now().Add(ttl)
		return s.cachedKey
	}
}

// InvalidateKeyCache forces the next keyResolver call to re-read from the DB.
func (s *Server) InvalidateKeyCache() {
	s.keyMu.Lock()
	s.keyExpiry = time.Time{}
	s.keyMu.Unlock()
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

func (s *Server) Start() error {
	return s.serve(context.Background())
}

func (s *Server) StartWithContext(ctx context.Context) error {
	return s.serve(ctx)
}

func (s *Server) serve(ctx context.Context) error {
	addrs := s.listenAddrs()
	servers := make([]*http.Server, 0, len(addrs))
	listeners := make([]net.Listener, 0, len(addrs))

	for _, addr := range addrs {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			for _, existing := range listeners {
				existing.Close()
			}
			return fmt.Errorf("listen on %s: %w", addr, err)
		}
		listeners = append(listeners, ln)
		servers = append(servers, &http.Server{
			Addr:         addr,
			Handler:      s.router,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
		})
	}

	shutdownAll := func(parent context.Context) {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if parent != nil {
			shutdownCtx, cancel = context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		}
		defer cancel()
		for _, srv := range servers {
			if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("server shutdown error: %v", err)
			}
		}
	}

	if ctx != nil {
		go func() {
			<-ctx.Done()
			shutdownAll(ctx)
		}()
	}

	errCh := make(chan error, len(servers))
	for i, srv := range servers {
		ln := listeners[i]
		if s.config.TLSCert != "" && s.config.TLSKey != "" {
			log.Printf("Vault API server listening on %s (TLS)", ln.Addr().String())
			go func(server *http.Server, listener net.Listener) {
				errCh <- server.ServeTLS(listener, s.config.TLSCert, s.config.TLSKey)
			}(srv, ln)
			continue
		}

		log.Printf("Vault API server listening on %s", ln.Addr().String())
		go func(server *http.Server, listener net.Listener) {
			errCh <- server.Serve(listener)
		}(srv, ln)
	}

	closed := false
	for range servers {
		err := <-errCh
		if errors.Is(err, http.ErrServerClosed) {
			closed = true
			continue
		}
		if err != nil {
			shutdownAll(ctx)
			return err
		}
	}

	if closed {
		return http.ErrServerClosed
	}

	return nil
}

func (s *Server) listenAddrs() []string {
	addrs := []string{s.config.Addr}
	loopbackAddr, ok := loopbackListenerFor(s.config.Addr)
	if !ok {
		return addrs
	}

	return append([]string{loopbackAddr}, addrs...)
}

func loopbackListenerFor(addr string) (string, bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "", false
	}

	normalized := strings.Trim(strings.TrimSpace(host), "[]")
	if normalized == "" || strings.EqualFold(normalized, "localhost") {
		return "", false
	}

	ip := net.ParseIP(normalized)
	if ip != nil {
		if ip.IsLoopback() || ip.IsUnspecified() {
			return "", false
		}
	}

	return net.JoinHostPort("127.0.0.1", port), true
}

func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
