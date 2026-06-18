package api

// routeDocs maps "METHOD /path" (with /api/v1 stripped) to a human description
// for the Settings → API reference. meta_test.go fails the build if a
// registered route has no entry here, so the reference can never silently drift.
var routeDocs = map[string]string{
	// Health & Realtime
	"GET /health":            "Liveness check (mode + version)",
	"GET /health/summary":    "Aggregate system health",
	"GET /runner/status":     "Backup runner status",
	"GET /release/changelog": "Embedded CHANGELOG.md (drives the About modal)",
	"GET /release/latest":    "Latest GitHub release metadata (drives the update badge)",

	// Jobs
	"GET /jobs":                                       "List backup jobs",
	"POST /jobs":                                      "Create backup job",
	"GET /jobs/next-runs":                             "Next scheduled run for every job",
	"GET /jobs/{id}":                                  "Get job",
	"PUT /jobs/{id}":                                  "Update job",
	"DELETE /jobs/{id}":                               "Delete job",
	"GET /jobs/{id}/next-run":                         "Next scheduled run",
	"GET /jobs/{id}/history":                          "Job run history",
	"GET /jobs/{id}/restore-points":                   "List restore points",
	"GET /jobs/{id}/retention-preview":                "Preview which restore points a Long-Term Retention (LTR) policy would keep/prune",
	"DELETE /jobs/{id}/restore-points/{rpid}":         "Delete a restore point",
	"GET /jobs/{id}/restore-points/{rpid}/contents":   "Tar index sidecar contents for the file picker",
	"POST /jobs/{id}/restore-points/{rpid}/preflight": "Pre-restore go/no-go checks",
	"POST /jobs/{id}/run":                             "Run job now",
	"POST /jobs/{id}/cancel":                          "Cancel running job",
	"POST /jobs/{id}/restore":                         "Restore from a restore point",
	"GET /jobs/{id}/stale-items":                      "List items whose backing resource no longer exists (live scan)",
	"POST /jobs/{id}/stale-items/remove":              "Remove all still-missing items from the job (re-validates first)",
	"DELETE /jobs/{id}/items/{itemId}":                "Remove a single item from the job (existing restore points are kept)",

	// Verify
	"POST /jobs/{id}/restore-points/{rpid}/verify":     "Start a verify run (mode: quick or deep)",
	"GET /jobs/{id}/restore-points/{rpid}/verify-runs": "List recent verify runs for a restore point",
	"GET /jobs/{id}/verify-runs/{vrid}":                "Get one verify run",

	// Storage Destinations
	"GET /storage":                      "List storage destinations",
	"POST /storage":                     "Create storage destination",
	"GET /storage/{id}":                 "Get storage destination",
	"PUT /storage/{id}":                 "Update storage destination",
	"DELETE /storage/{id}":              "Delete storage destination",
	"POST /storage/{id}/test":           "Test connection",
	"POST /storage/{id}/health-check":   "Run on-demand health check (returns {status, error})",
	"POST /storage/{id}/capacity-check": "Refresh used / total / free capacity for this destination",
	"POST /storage/{id}/breaker/close":  "Manually close the destination circuit breaker (clear sticky failure state)",
	"POST /storage/{id}/scan":           "Scan for existing backups",
	"POST /storage/{id}/import":         "Import discovered backups",
	"POST /storage/{id}/restore-db":     "Restore Vault database from this destination",
	"GET /storage/{id}/jobs":            "Jobs targeting this destination",
	"GET /storage/{id}/list":            "List files at destination",
	"GET /storage/{id}/files":           "Download a file from destination",
	"POST /storage/{id}/scan-orphans":   "Scan for orphan files (dry-run)",
	"POST /storage/{id}/delete-orphans": "Delete listed orphan files (re-checks before deleting)",

	// Deduplication
	"GET /storage/{id}/dedup-stats": "Per-destination dedup stats (ratio, chunks, packs, reclaimable bytes)",
	"POST /storage/{id}/gc":         "Run mark-and-sweep GC (async; broadcasts dedup_gc_complete over WS)",

	// Anomalies
	"GET /anomalies":                             "List detected anomalies (filters: state, severity, scope; keyset paginated)",
	"POST /anomalies/ack-bulk":                   "Acknowledge several anomalies at once",
	"GET /anomalies/{id}":                        "Get one anomaly",
	"POST /anomalies/{id}/ack":                   "Acknowledge / dismiss / mark-expected one anomaly",
	"GET /jobs/{id}/baseline":                    "Per-job learned baseline (size/duration median + MAD, sample count)",
	"GET /destinations/{id}/capacity-trajectory": "Capacity samples + projected runway for a destination",

	// Settings
	"GET /settings":               "Get settings",
	"PUT /settings":               "Update settings",
	"GET /settings/staging":       "Get staging path info",
	"PUT /settings/staging":       "Override staging path",
	"GET /settings/database":      "Get database location info",
	"PUT /settings/database":      "Set custom database snapshot path",
	"GET /settings/diagnostics":   "Download diagnostics bundle",
	"POST /settings/discord/test": "Send Discord webhook test",

	// Encryption
	"GET /settings/encryption":            "Encryption status",
	"POST /settings/encryption":           "Enable / disable encryption",
	"POST /settings/encryption/verify":    "Verify passphrase (rate-limited 10/min)",
	"GET /settings/encryption/passphrase": "Reveal stored passphrase",

	// API Key
	"GET /settings/api-key":           "API key status",
	"POST /settings/api-key/generate": "Generate API key (rate-limited 5/min)",
	"GET /settings/api-key/key":       "Reveal API key",
	"POST /settings/api-key/rotate":   "Rotate API key (rate-limited 5/min)",
	"DELETE /settings/api-key":        "Revoke API key",

	// Discovery
	"GET /containers":               "List Docker containers",
	"GET /containers/{name}/mounts": "Mount points for a container (used by the folder picker)",
	"GET /vms":                      "List libvirt VMs",
	"GET /folders":                  "List user share folders",
	"GET /plugins":                  "List Unraid plugins",
	"GET /zfs":                      "List ZFS datasets",
	"GET /browse":                   "Browse filesystem paths",
	"GET /path-exists":              "Safepath-gated existence check (used by the folder picker)",
	"GET /presets/exclusions":       "Built-in exclusion presets",

	// Replication
	"GET /replication":            "List replication peers",
	"POST /replication":           "Create replication peer",
	"POST /replication/test-url":  "Test a peer URL",
	"GET /replication/{id}":       "Get peer",
	"PUT /replication/{id}":       "Update peer",
	"DELETE /replication/{id}":    "Delete peer",
	"POST /replication/{id}/test": "Test peer connection",
	"POST /replication/{id}/sync": "Sync now",
	"GET /replication/{id}/jobs":  "List replicated jobs",

	// Activity & History
	"GET /activity":      "List activity log",
	"DELETE /activity":   "Purge activity log",
	"DELETE /history":    "Purge job run history",
	"GET /history/trend": "Backup size trend, bucketed by period",
	"GET /recovery/plan": "Disaster recovery plan",

	// Model Context Protocol — mounted as a catch-all handler, so chi
	// reports every HTTP method chi knows about for both /mcp and /mcp/*.
	"GET /mcp": "MCP server endpoint (Streamable HTTP)", "POST /mcp": "MCP server endpoint (Streamable HTTP)",
	"PUT /mcp": "MCP server endpoint (Streamable HTTP)", "DELETE /mcp": "MCP server endpoint (Streamable HTTP)",
	"PATCH /mcp": "MCP server endpoint (Streamable HTTP)", "HEAD /mcp": "MCP server endpoint (Streamable HTTP)",
	"OPTIONS /mcp": "MCP server endpoint (Streamable HTTP)", "CONNECT /mcp": "MCP server endpoint (Streamable HTTP)",
	"TRACE /mcp": "MCP server endpoint (Streamable HTTP)",
	"GET /mcp/*": "MCP server endpoint (Streamable HTTP)", "POST /mcp/*": "MCP server endpoint (Streamable HTTP)",
	"PUT /mcp/*": "MCP server endpoint (Streamable HTTP)", "DELETE /mcp/*": "MCP server endpoint (Streamable HTTP)",
	"PATCH /mcp/*": "MCP server endpoint (Streamable HTTP)", "HEAD /mcp/*": "MCP server endpoint (Streamable HTTP)",
	"OPTIONS /mcp/*": "MCP server endpoint (Streamable HTTP)", "CONNECT /mcp/*": "MCP server endpoint (Streamable HTTP)",
	"TRACE /mcp/*": "MCP server endpoint (Streamable HTTP)",
}

// routeDocsIgnore are routes intentionally excluded from the reference/coverage.
var routeDocsIgnore = map[string]bool{
	"GET /ws":          true, // websocket upgrade
	"GET /meta/routes": true, // this endpoint itself
}
