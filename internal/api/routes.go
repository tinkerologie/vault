package api

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ruaan-deysel/vault/internal/api/handlers"
	mcpserver "github.com/ruaan-deysel/vault/internal/mcp"
	"github.com/ruaan-deysel/vault/web"
)

func (s *Server) setupRoutes() *chi.Mux {
	r := chi.NewRouter()
	r.Use(QuietRequestLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Heartbeat("/ping"))
	r.Use(BodySizeLimit(maxRequestBodySize))

	r.Route("/api/v1", func(r chi.Router) {
		// Settings handler is shared between public and authenticated routes.
		settingsH := handlers.NewSettingsHandler(s.db, s.config.ServerKey)
		s.settingsHandler = settingsH

		// Public endpoints.
		r.Get("/health", s.handleHealth)

		// All API routes.
		r.Get("/ws", s.hub.HandleWS)

		healthH := handlers.NewHealthHandler(s.db)
		r.Get("/health/summary", healthH.Summary)

		storageH := handlers.NewStorageHandler(s.db, s.runner)
		r.Route("/storage", func(r chi.Router) {
			// Storage CRUD is allowed in replica mode — replicas need
			// storage destinations configured for replication targets.
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
			if s.config.ReadOnly {
				r.Use(ReadOnlyGuard)
			}
			r.Get("/", jobH.List)
			r.Post("/", jobH.Create)
			r.Get("/next-runs", jobH.AllNextRuns)
			r.Get("/{id}", jobH.Get)
			r.Put("/{id}", jobH.Update)
			r.Delete("/{id}", jobH.Delete)
			r.Get("/{id}/history", jobH.GetHistory)
			r.Get("/{id}/restore-points", jobH.GetRestorePoints)
			r.Post("/{id}/run", jobH.RunNow)
			r.Post("/{id}/cancel", jobH.Cancel)
			r.Post("/{id}/restore", jobH.Restore)
			r.Get("/{id}/next-run", jobH.NextRun)
		})

		r.Get("/runner/status", jobH.RunnerStatus)

		r.Route("/settings", func(r chi.Router) {
			r.Get("/", settingsH.List)
			r.Put("/", settingsH.Update)
			r.Get("/encryption", settingsH.GetEncryptionStatus)
			r.Post("/encryption", settingsH.SetEncryption)
			r.Post("/encryption/verify", settingsH.VerifyEncryption)
			r.Get("/encryption/passphrase", settingsH.GetEncryptionPassphrase)
			r.Get("/staging", settingsH.GetStagingInfo)
			r.Put("/staging", settingsH.SetStagingOverride)
			r.Post("/discord/test", settingsH.TestDiscordWebhook)
			r.Get("/database", settingsH.GetDatabaseInfo)
			r.Put("/database", settingsH.SetSnapshotPath)
			r.Get("/diagnostics", settingsH.GetDiagnostics)
		})

		browseH := handlers.NewBrowseHandler()
		s.browseHandler = browseH
		r.Get("/browse", browseH.List)

		activityH := handlers.NewActivityHandler(s.db)
		r.Route("/activity", func(r chi.Router) {
			r.Get("/", activityH.List)
			r.Delete("/", activityH.Purge)
		})

		historyH := handlers.NewHistoryHandler(s.db)
		r.Delete("/history", historyH.Purge)

		// Discovery endpoints are only relevant in daemon mode.
		if !s.config.ReadOnly {
			discoverH := handlers.NewDiscoverHandler()
			r.Get("/containers", discoverH.ListContainers)
			r.Get("/vms", discoverH.ListVMs)
			r.Get("/folders", discoverH.ListFolders)
			r.Get("/plugins", discoverH.ListPlugins)
			r.Get("/zfs", discoverH.ListZFSDatasets)
		}

		presetsH := handlers.NewPresetsHandler()
		r.Get("/presets/exclusions", presetsH.GetExclusions)

		replH := handlers.NewReplicationHandler(s.db, s.Syncer, s.config.ServerKey, func() error {
			if s.schedReload != nil {
				return s.schedReload()
			}
			return nil
		})
		r.Route("/replication", func(r chi.Router) {
			r.Get("/", replH.List)
			r.Post("/", replH.Create)
			r.Post("/test-url", replH.TestURL)
			r.Get("/{id}", replH.Get)
			r.Put("/{id}", replH.Update)
			r.Delete("/{id}", replH.Delete)
			r.Post("/{id}/test", replH.TestConnection)
			r.Post("/{id}/sync", replH.SyncNow)
			r.Get("/{id}/jobs", replH.ListReplicatedJobs)
		})

		recoveryH := handlers.NewRecoveryHandler(s.db, s.config.Version)
		r.Get("/recovery/plan", recoveryH.GetPlan)

		// MCP is only available in daemon mode.
		if !s.config.ReadOnly {
			mcpSrv := mcpserver.New(s.db, s.runner, mcpserver.Config{
				Version:  s.config.Version,
				ReadOnly: s.config.ReadOnly,
			})
			r.Handle("/mcp", mcpSrv.HTTPHandler())
			r.Handle("/mcp/*", mcpSrv.HTTPHandler())
		}
	})

	// Serve embedded SPA — static files and SPA catch-all for client-side routing.
	distFS, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		panic("failed to create sub filesystem for web dist: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(distFS))

	// Prepare runtime config injection so the SPA respects Unraid settings
	// (e.g., 12h / 24h time format) even when accessed directly on the daemon port.
	injectedIndex := buildInjectedIndex(distFS)

	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		// Try to serve the file directly (css, js, images, etc.).
		if path != "" {
			if f, err := distFS.Open(path); err == nil {
				_ = f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// Fall back to injected index.html for SPA client-side routing.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(injectedIndex)
	})

	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	mode := "daemon"
	if s.config.ReadOnly {
		mode = "replica"
	}
	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": s.config.Version,
		"mode":    mode,
	})
}

// buildInjectedIndex reads index.html from the embedded SPA filesystem and
// injects a runtime config script tag so the SPA can detect Unraid settings
// (e.g., time format) even when accessed directly on the daemon port.
func buildInjectedIndex(distFS fs.FS) []byte {
	raw, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		return raw // serve as-is if read fails
	}

	cfg := map[string]string{
		"mode":       "direct",
		"timeFormat": detectUnraidTimeFormat(),
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return raw
	}
	script := fmt.Sprintf("<script>window.__VAULT_RUNTIME_CONFIG__=%s;</script>", cfgJSON)

	html := strings.Replace(string(raw), "</head>", script+"\n</head>", 1)
	return []byte(html)
}

// detectUnraidTimeFormat reads Unraid's display time preference from
// /boot/config/plugins/dynamix/dynamix.cfg. Returns "24h", "12h", or "auto".
func detectUnraidTimeFormat() string {
	const cfgPath = "/boot/config/plugins/dynamix/dynamix.cfg"
	return detectTimeFormatFromPath(cfgPath)
}

// detectTimeFormatFromPath reads a dynamix.cfg INI file and returns the time
// format preference. Extracted for testability.
func detectTimeFormatFromPath(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "auto"
	}
	return parseTimeFormatINI(string(data))
}

// parseTimeFormatINI parses Unraid dynamix.cfg INI content and returns the
// time format: "24h", "12h", or "auto" if not determinable.
// It checks [display][time] first, then falls back to [notify][time].
// Unraid 7.x stores the user-facing time format in [notify][time] while
// [display][date] uses strftime-style "%c" (locale-dependent).
func parseTimeFormatINI(content string) string {
	sections := map[string]map[string]string{}
	currentSection := ""
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = line[1 : len(line)-1]
			if sections[currentSection] == nil {
				sections[currentSection] = map[string]string{}
			}
			continue
		}
		if currentSection != "" {
			if k, v, ok := strings.Cut(line, "="); ok {
				sections[currentSection][strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
			}
		}
	}

	// Check [display][time] first, then fall back to [notify][time].
	timeFmt := sections["display"]["time"]
	if timeFmt == "" {
		timeFmt = sections["notify"]["time"]
	}
	if timeFmt == "" {
		return "auto"
	}

	// PHP date format: H or G = 24-hour clock.
	if strings.ContainsAny(timeFmt, "HG") {
		return "24h"
	}
	return "12h"
}
