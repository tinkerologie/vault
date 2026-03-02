package api

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ruaandeysel/vault/internal/api/handlers"
	mcpserver "github.com/ruaandeysel/vault/internal/mcp"
	"github.com/ruaandeysel/vault/web"
)

func (s *Server) setupRoutes() *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Heartbeat("/ping"))
	r.Use(BodySizeLimit(maxRequestBodySize))

	r.Route("/api/v1", func(r chi.Router) {
		// Settings handler is shared between public and authenticated routes.
		settingsH := handlers.NewSettingsHandler(s.db, s.config.ServerKey)
		settingsH.SetOnKeyChange(s.InvalidateKeyCache)

		// Public endpoints (no auth required).
		r.Get("/health", s.handleHealth)
		r.Get("/auth/status", s.handleAuthStatus)
		// Generate is public only when no key exists (handler enforces this).
		r.Post("/settings/api-key/generate", settingsH.GenerateAPIKey)

		// Authenticated API routes.
		// LocalUIBypass allows same-origin browser requests (the SPA) through
		// without an API key. External clients must provide a valid key.
		r.Group(func(r chi.Router) {
			r.Use(LocalUIBypass(s.keyResolver()))
			r.Get("/ws", s.hub.HandleWS)

			healthH := handlers.NewHealthHandler(s.db)
			r.Get("/health/summary", healthH.Summary)

			storageH := handlers.NewStorageHandler(s.db, s.runner)
			r.Route("/storage", func(r chi.Router) {
				r.Get("/", storageH.List)
				r.Post("/", storageH.Create)
				r.Get("/{id}", storageH.Get)
				r.Put("/{id}", storageH.Update)
				r.Delete("/{id}", storageH.Delete)
				r.Post("/{id}/test", storageH.TestConnection)
				r.Post("/{id}/scan", storageH.Scan)
				r.Post("/{id}/import", storageH.Import)
				r.Post("/{id}/restore-db", storageH.RestoreDB)
				r.Get("/{id}/jobs", storageH.DependentJobs)
				r.Get("/{id}/list", storageH.ListFiles)
				r.Get("/{id}/files", storageH.DownloadFile)
			})

			jobH := handlers.NewJobHandler(s.db, s.runner, func() error {
				if s.schedReload != nil {
					return s.schedReload()
				}
				return nil
			})
			if s.nextRunResolver != nil {
				jobH.SetNextRunResolver(s.nextRunResolver)
			}
			r.Route("/jobs", func(r chi.Router) {
				r.Get("/", jobH.List)
				r.Post("/", jobH.Create)
				r.Get("/next-runs", jobH.AllNextRuns)
				r.Get("/{id}", jobH.Get)
				r.Put("/{id}", jobH.Update)
				r.Delete("/{id}", jobH.Delete)
				r.Get("/{id}/history", jobH.GetHistory)
				r.Get("/{id}/restore-points", jobH.GetRestorePoints)
				r.Post("/{id}/run", jobH.RunNow)
				r.Post("/{id}/restore", jobH.Restore)
				r.Get("/{id}/next-run", jobH.NextRun)
			})

			r.Route("/settings", func(r chi.Router) {
				r.Get("/", settingsH.List)
				r.Put("/", settingsH.Update)
				r.Get("/encryption", settingsH.GetEncryptionStatus)
				r.Post("/encryption", settingsH.SetEncryption)
				r.Post("/encryption/verify", settingsH.VerifyEncryption)
				r.Get("/encryption/passphrase", settingsH.GetEncryptionPassphrase)
				r.Get("/api-key", settingsH.GetAPIKeyStatus)
				r.Post("/api-key/rotate", settingsH.RotateAPIKey)
				r.Delete("/api-key", settingsH.RevokeAPIKey)
			})

			browseH := handlers.NewBrowseHandler()
			r.Get("/browse", browseH.List)

			activityH := handlers.NewActivityHandler(s.db)
			r.Get("/activity", activityH.List)

			discoverH := handlers.NewDiscoverHandler()
			r.Get("/containers", discoverH.ListContainers)
			r.Get("/vms", discoverH.ListVMs)
			r.Get("/folders", discoverH.ListFolders)
			r.Get("/plugins", discoverH.ListPlugins)

			replH := handlers.NewReplicationHandler(s.db, s.Syncer, s.config.ServerKey, func() error {
				if s.schedReload != nil {
					return s.schedReload()
				}
				return nil
			})
			r.Route("/replication", func(r chi.Router) {
				r.Get("/", replH.List)
				r.Post("/", replH.Create)
				r.Get("/{id}", replH.Get)
				r.Put("/{id}", replH.Update)
				r.Delete("/{id}", replH.Delete)
				r.Post("/{id}/test", replH.TestConnection)
				r.Post("/{id}/sync", replH.SyncNow)
				r.Get("/{id}/jobs", replH.ListReplicatedJobs)
			})

			// MCP (Model Context Protocol) HTTP transport.
			mcpSrv := mcpserver.New(s.db, s.runner)
			r.Handle("/mcp", mcpSrv.HTTPHandler())
			r.Handle("/mcp/*", mcpSrv.HTTPHandler())
		})
	})

	// Serve embedded SPA — static files and SPA catch-all for client-side routing.
	distFS, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		panic("failed to create sub filesystem for web dist: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(distFS))
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		// Try to serve the file directly (css, js, images, etc.).
		if path != "" {
			if f, err := distFS.Open(path); err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// Fall back to index.html for SPA client-side routing.
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})

	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": s.config.Version,
	})
}

// handleAuthStatus reports whether API key authentication is enabled.
// This endpoint is unauthenticated so the SPA can check auth requirements.
// ui_auth_required is always false because the SPA uses origin-based bypass.
// auth_required indicates whether an API key exists (for 3rd-party clients).
func (s *Server) handleAuthStatus(w http.ResponseWriter, _ *http.Request) {
	// Auth is required for external clients if a DB key or CLI key is configured.
	required := s.db.HasAPIKey() || s.config.APIKey != ""
	respondJSON(w, http.StatusOK, map[string]any{
		"auth_required":    required,
		"ui_auth_required": false,
	})
}
