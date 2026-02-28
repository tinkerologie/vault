---
description: Step-by-step guide for adding a new REST API endpoint
tools: ["editor", "terminal"]
---

# Add a New REST API Endpoint

Follow these steps to add a new REST API endpoint to Vault.

## Step 1: Determine Endpoint Type

- **Read-only (GET):** Returns data from the database
- **Control (POST/PUT/DELETE):** Executes an action

## Step 2: Add Handler

In `internal/api/handlers/`, either add to an existing handler file or create a new one.

### For GET (data retrieval)

```go
func (h *MyHandler) List(w http.ResponseWriter, r *http.Request) {
    data, err := h.db.ListMyResource()
    if err != nil {
        respondError(w, http.StatusInternalServerError, err.Error())
        return
    }
    respondJSON(w, http.StatusOK, data)
}
```

### For POST (create/action)

```go
func (h *MyHandler) Create(w http.ResponseWriter, r *http.Request) {
    var req MyRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        respondError(w, http.StatusBadRequest, "invalid JSON")
        return
    }
    id, err := h.db.CreateMyResource(req)
    if err != nil {
        respondError(w, http.StatusInternalServerError, err.Error())
        return
    }
    respondJSON(w, http.StatusCreated, map[string]int64{"id": id})
}
```

## Step 3: Register Route

In `internal/api/routes.go`, add the route in `setupRoutes()`:

```go
myH := handlers.NewMyHandler(s.db)
r.Route("/myresource", func(r chi.Router) {
    r.Get("/", myH.List)
    r.Post("/", myH.Create)
    r.Get("/{id}", myH.Get)
    r.Put("/{id}", myH.Update)
    r.Delete("/{id}", myH.Delete)
})
```

## Step 4: Test

Create handler tests using `httptest`:

```go
func TestMyHandler_List(t *testing.T) {
    db := setupTestDB(t)
    handler := handlers.NewMyHandler(db)
    req := httptest.NewRequest("GET", "/api/v1/myresource", nil)
    w := httptest.NewRecorder()
    handler.List(w, req)
    // Assert status code and response body
}
```

## Step 5: Verify

```bash
go test ./internal/api/... -v
make lint
make pre-commit-run
```
