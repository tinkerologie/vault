package api

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"

	"github.com/ruaan-deysel/vault/internal/api/handlers"
	mcpserver "github.com/ruaan-deysel/vault/internal/mcp"
	"github.com/ruaan-deysel/vault/internal/release"
	"github.com/ruaan-deysel/vault/web"
)

func (s *Server) setupRoutes() *chi.Mux {
	r := chi.NewRouter()
	r.Use(QuietRequestLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Heartbeat("/ping"))
	r.Use(BodySizeLimit(maxRequestBodySize))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*.myunraid.net", "http://localhost:*", "http://127.0.0.1:*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "X-API-Key"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Route("/api/v1", func(r chi.Router) {
		// Enforce API key authentication for non-loopback requests.
		// Loopback connections and the Unraid PHP proxy are exempt—
		// auth only applies when the daemon is exposed on a LAN address.
		r.Use(APIKeyAuth(s.db))

		// Settings handler is shared between public and authenticated routes.
		settingsH := handlers.NewSettingsHandler(s.db, s.config.ServerKey)
		s.settingsHandler = settingsH
		settingsH.SetScheduleReloadHook(func() error {
			if s.schedReload != nil {
				return s.schedReload()
			}
			return nil
		})

		// Public endpoints.
		r.Get("/health", s.handleHealth)
		r.Get("/meta/routes", s.handleMetaRoutes)

		// All API routes.
		r.Get("/ws", s.hub.HandleWS)

		healthH := handlers.NewHealthHandler(s.db)
		r.Get("/health/summary", healthH.Summary)

		// Release / About card endpoints. Always accessible (also in replica mode):
		// these are purely informational metadata, no DB writes.
		releaseCache := release.NewCache("", "ruaan-deysel/vault", time.Hour)
		releaseH := handlers.NewReleaseHandler(release.Raw(), releaseCache)
		r.Route("/release", func(r chi.Router) {
			r.Get("/changelog", releaseH.Changelog)
			r.Get("/latest", releaseH.Latest)
		})

		storageH := handlers.NewStorageHandler(s.db, s.runner)
		s.storageHandler = storageH
		r.Route("/storage", func(r chi.Router) {
			// Storage CRUD is allowed in replica mode — replicas need
			// storage destinations configured for replication targets.
			r.Get("/", storageH.List)
			r.Post("/", storageH.Create)
			r.Get("/{id}", storageH.Get)
			r.Put("/{id}", storageH.Update)
			r.Delete("/{id}", storageH.Delete)
			r.Post("/{id}/test", storageH.TestConnection)
			r.Post("/{id}/health-check", storageH.HealthCheck)
			r.Post("/{id}/capacity-check", storageH.RefreshCapacity)
			r.Post("/{id}/breaker/close", storageH.CloseBreaker)
			r.Post("/{id}/scan-orphans", storageH.ScanOrphans)
			r.Post("/{id}/delete-orphans", storageH.DeleteOrphans)
			r.Get("/{id}/dedup-stats", storageH.GetDedupStats)
			r.Post("/{id}/gc", storageH.RunDedupGC)
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
		s.jobHandler = jobH
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
			r.Get("/{id}/retention-preview", jobH.RetentionPreview)
			r.Delete("/{id}/restore-points/{rpid}", jobH.DeleteRestorePoint)
			r.Get("/{id}/restore-points/{rpid}/contents", jobH.RestorePointContents)
			r.Post("/{id}/restore-points/{rpid}/preflight", jobH.RestorePointPreflight)
			r.Post("/{id}/restore-points/{rpid}/verify", jobH.VerifyRestorePoint)
			r.Get("/{id}/restore-points/{rpid}/verify-runs", jobH.ListRestorePointVerifyRuns)
			r.Get("/{id}/verify-runs/{vrid}", jobH.GetVerifyRun)
			r.Post("/{id}/run", jobH.RunNow)
			r.Post("/{id}/cancel", jobH.Cancel)
			r.Post("/{id}/restore", jobH.Restore)
			r.Get("/{id}/next-run", jobH.NextRun)
			r.Get("/{id}/stale-items", jobH.GetStaleItems)
			r.Post("/{id}/stale-items/remove", jobH.RemoveStaleItems)
			r.Delete("/{id}/items/{itemId}", jobH.DeleteJobItem)
		})

		r.Get("/runner/status", jobH.RunnerStatus)

		r.Route("/settings", func(r chi.Router) {
			r.Get("/", settingsH.List)
			r.Put("/", settingsH.Update)
			r.Get("/encryption", settingsH.GetEncryptionStatus)
			r.Post("/encryption", settingsH.SetEncryption)
			r.With(httprate.LimitByIP(10, time.Minute)).Post("/encryption/verify", settingsH.VerifyEncryption)
			r.Get("/encryption/passphrase", settingsH.GetEncryptionPassphrase)
			r.Get("/staging", settingsH.GetStagingInfo)
			r.Put("/staging", settingsH.SetStagingOverride)
			r.Post("/discord/test", settingsH.TestDiscordWebhook)
			r.Get("/database", settingsH.GetDatabaseInfo)
			r.Put("/database", settingsH.SetSnapshotPath)
			r.Get("/diagnostics", settingsH.GetDiagnostics)

			// API key management.
			r.Get("/api-key", settingsH.GetAPIKeyStatus)
			r.With(httprate.LimitByIP(5, time.Minute)).Post("/api-key/generate", settingsH.GenerateAPIKey)
			r.Get("/api-key/key", settingsH.GetAPIKey)
			r.With(httprate.LimitByIP(5, time.Minute)).Post("/api-key/rotate", settingsH.RotateAPIKey)
			r.Delete("/api-key", settingsH.RevokeAPIKey)
		})

		browseH := handlers.NewBrowseHandler()
		s.browseHandler = browseH
		r.Get("/browse", browseH.List)
		r.Get("/path-exists", browseH.Exists)

		activityH := handlers.NewActivityHandler(s.db)
		r.Route("/activity", func(r chi.Router) {
			r.Get("/", activityH.List)
			r.Delete("/", activityH.Purge)
		})

		historyH := handlers.NewHistoryHandler(s.db)
		r.Delete("/history", historyH.Purge)
		r.Get("/history/trend", historyH.Trend)

		// Discovery endpoints are only relevant in daemon mode.
		if !s.config.ReadOnly {
			discoverH := handlers.NewDiscoverHandler()
			r.Get("/containers", discoverH.ListContainers)
			r.Get("/containers/{name}/mounts", discoverH.ContainerMounts)
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
		}, s.runner)
		s.replicationHandler = replH
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

		anomalyH := handlers.NewAnomalyHandler(s.db)
		s.anomalyHandler = anomalyH
		r.Route("/anomalies", func(r chi.Router) {
			r.Get("/", anomalyH.List)
			r.Post("/ack-bulk", anomalyH.AckBulk)
			r.Get("/{id}", anomalyH.Get)
			r.Post("/{id}/ack", anomalyH.Ack)
		})
		r.Get("/jobs/{id}/baseline", anomalyH.GetBaseline)
		r.Get("/destinations/{id}/capacity-trajectory", anomalyH.GetTrajectory)

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
	resp := map[string]any{
		"status":  "ok",
		"version": s.config.Version,
		"mode":    mode,
	}
	// Surface boot-time persistence diagnostics so operators (and
	// `make verify`) can confirm at a glance whether configuration
	// survived a restart or upgrade. Field is omitted entirely when
	// the daemon hasn't recorded any startup info yet (defensive —
	// in normal operation it's always set during daemon.RunE).
	if s.startupDiagnostics != nil {
		resp["startup"] = s.startupDiagnostics
	}
	respondJSON(w, http.StatusOK, resp)
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
	data, err := os.ReadFile(path) // #nosec G304 //nolint:gosec // path is a hardcoded constant from detectUnraidTimeFormat
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Warning: could not read Unraid time-format config %q: %v — falling back to \"auto\"", path, err)
		}
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
