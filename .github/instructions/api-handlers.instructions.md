---
applyTo: "internal/api/**/*.go"
---

# API Instructions

Read `internal/api/routes.go`, `internal/api/middleware.go`, and the affected
handler before editing.

- Use Chi v5 and register routes in `internal/api/routes.go`.
- Use `respondJSON`, `respondError`, and `respondInternalError` from
  `internal/api/handlers/respond.go`.
- Preserve API-key authentication, rate limiting, body limits, CORS/PNA, and
  read-only replica guards.
- Treat loopback/proxy exemptions and destructive-route guards as security
  boundaries; do not broaden them casually.
- Decode and validate request bodies and URL parameters before side effects.
- Pass `r.Context()` through cancellable database, runner, storage, and network
  work.
- Inject existing collaborators through handler constructors. Do not create
  parallel runner, scheduler, database, or WebSocket state in a handler.
- Never expose secrets or raw sensitive configuration in responses or logs.
- Add `httptest` coverage for success, validation, authorization/guard behavior,
  and meaningful error paths.

The current route catalog is the source. Do not maintain a second endpoint list
in instruction files.
