package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tinkerologie/vault/internal/api/handlers"
)

func (s *Server) StartWithContext(ctx context.Context) error {
	return s.Run(ctx, nil)
}

// WorkerShutdown stops external dispatch before Runner admission closes and
// joins the remaining workers after HTTP and Runner work has finished.
type WorkerShutdown struct {
	WaitForDispatch func()
	Join            func()
}

// Run owns one server lifetime. startWorkers runs after the TLS credentials
// and listener are ready. Its context is canceled when serving stops.
// The returned shutdown steps coordinate worker cleanup with HTTP and Runner.
func (s *Server) Run(ctx context.Context, startWorkers func(context.Context) WorkerShutdown) error {
	srv := &http.Server{
		Addr:              s.config.Addr,
		Handler:           s.router,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		// Single source of truth: outbound connectivity probes derive their
		// own deadline from this so they always finish early enough for the
		// error response to be flushed (handlers.probeTimeoutHeadroom).
		WriteTimeout: handlers.ServerWriteTimeout,
		IdleTimeout:  60 * time.Second,
	}

	if err := CheckTLSPair(s.config.TLSCert, s.config.TLSKey); err != nil {
		return err
	}
	useTLS := s.config.TLSCert != ""
	if useTLS {
		cert, err := tls.LoadX509KeyPair(s.config.TLSCert, s.config.TLSKey)
		if err != nil {
			return fmt.Errorf("load TLS credentials: %w", err)
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	addr := s.config.Addr
	if addr == "" {
		addr = ":http"
		if useTLS {
			addr = ":https"
		}
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	if !s.runStarted.CompareAndSwap(false, true) {
		return errors.New("server runtime already started")
	}

	if useTLS {
		log.Printf("Vault API server listening on %s (TLS)", listener.Addr())
	} else {
		log.Printf("Vault API server listening on %s", listener.Addr())
		if !isLoopbackAddr(listener.Addr().String()) {
			log.Printf("Warning: the Vault API on %s uses plain HTTP, so API keys and data can be read on the network. Use it only on a network you trust, or start the daemon with --tls-cert and --tls-key", listener.Addr())
		}
	}
	return s.serveRuntime(ctx, listener, srv, useTLS, startWorkers)
}

func (s *Server) serveRuntime(ctx context.Context, listener net.Listener, srv *http.Server, useTLS bool, startWorkers func(context.Context) WorkerShutdown) error {
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
	go s.hub.Run()
	s.mountMgr.Start(workerCtx)
	var workers WorkerShutdown
	if startWorkers != nil && workerCtx.Err() == nil {
		workers = startWorkers(workerCtx)
	}
	if s.mcp != nil {
		s.mcp.SetShutdownContext(workerCtx)
		if srv.Handler != nil {
			srv.Handler = withMCPTransportShutdown(workerCtx, srv.Handler)
		}
	}

	served := make(chan error, 1)
	go func() {
		if useTLS {
			served <- srv.ServeTLS(listener, "", "")
		} else {
			served <- srv.Serve(listener)
		}
	}()

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-served:
	}
	cancelWorkers()

	// Persist configuration before waiting for backups. rc.vault can
	// escalate to SIGKILL if an in-flight writer cannot exit (issue #108).
	if s.preShutdownHook != nil {
		start := time.Now()
		s.preShutdownHook()
		log.Printf("server shutdown: pre-drain flush completed in %v", time.Since(start).Round(time.Millisecond))
	}

	// Stop HTTP admissions while the runner finishes. Shutdown must itself
	// return before dependencies can close; Serve returns earlier.
	cleanupCtx := context.WithoutCancel(ctx)
	httpDone := make(chan error, 1)
	go func() { httpDone <- srv.Shutdown(cleanupCtx) }()
	if workers.WaitForDispatch != nil {
		workers.WaitForDispatch()
	}
	drainRunner(cleanupCtx, s.runner.Drain)
	if err := <-httpDone; err != nil {
		log.Printf("server shutdown error: %v", err)
	}
	if serveErr == nil {
		serveErr = <-served
	}

	if s.mcp != nil {
		s.mcp.Close()
	}
	s.timedReads.Wait()
	s.mountMgr.Stop(cleanupCtx)
	s.hub.Stop()
	s.replicationHandler.WaitForSyncs()
	s.runner.WaitForBackgroundWork()
	if workers.Join != nil {
		workers.Join()
	}
	return serveErr
}

func drainRunner(ctx context.Context, drain func(context.Context) error) {
	drainCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := drain(drainCtx)
	cancel()
	if err != nil {
		log.Printf("server shutdown: runner still draining after 30s: %v", err)
		// The host owns forced termination. Never return while a writer can
		// still touch the database or storage.
		_ = drain(ctx)
	}
	log.Println("server shutdown: runner drained cleanly")
}

// withMCPTransportShutdown cancels MCP transports, which can stream on GET
// and POST. The runtime separately joins tool work through the SDK sessions.
func withMCPTransportShutdown(stop context.Context, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/mcp" || strings.HasPrefix(r.URL.Path, "/api/v1/mcp/") {
			ctx, cancel := context.WithCancel(r.Context())
			stopCancel := context.AfterFunc(stop, cancel)
			defer stopCancel()
			defer cancel()
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) trackTimedRead(next http.HandlerFunc, limit time.Duration, message string) http.HandlerFunc {
	timed := http.TimeoutHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer s.timedReads.Done()
		next(w, r)
	}), limit, message)
	return func(w http.ResponseWriter, r *http.Request) {
		// Reserve ownership before TimeoutHandler can launch work or return.
		s.timedReads.Add(1)
		timed.ServeHTTP(w, r)
	}
}
