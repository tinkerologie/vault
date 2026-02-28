---
applyTo: "internal/api/**/*.go"
---

# API Server Instructions

Reference: [`AGENTS.md`](../../AGENTS.md) for full project context.

## Router

Uses Chi v5 (`go-chi/chi/v5`), not gorilla/mux. Routes defined in `routes.go`.

## Handler Pattern

```go
func (h *JobHandler) List(w http.ResponseWriter, r *http.Request) {
    jobs, err := h.db.ListJobs()
    if err != nil {
        respondError(w, http.StatusInternalServerError, err.Error())
        return
    }
    respondJSON(w, http.StatusOK, jobs)
}
```

## Response Helpers

- Use `respondJSON()` for all JSON responses
- Use `respondError()` for error responses
- Both defined in `handlers/storage.go` (shared across handlers)

## Route Registration

Register new routes in `routes.go` using Chi's `r.Route()` grouping:

```go
r.Route("/api/v1", func(r chi.Router) {
    r.Route("/myresource", func(r chi.Router) {
        r.Get("/", handler.List)
        r.Post("/", handler.Create)
        r.Get("/{id}", handler.Get)
    })
})
```

URL params via `chi.URLParam(r, "id")`.

## WebSocket

- Hub in `internal/ws/` manages client connections
- Exposed at `GET /api/v1/ws`
- Broadcasts progress events to all connected clients

## Middleware

Chi middleware stack: Logger → Recoverer → Heartbeat (`/ping`).
