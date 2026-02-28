---
description: Step-by-step guide for adding a new backup engine handler
tools: ["editor", "terminal"]
---

# Add a New Engine Handler

Follow these steps to add a new backup/restore handler (e.g., for a new backup type).

## Step 1: Implement the Handler Interface

Create `internal/engine/myhandler.go`:

```go
package engine

type MyHandler struct {
    // handler state
}

func NewMyHandler() *MyHandler {
    return &MyHandler{}
}

func (h *MyHandler) Backup(item BackupItem, dest string, progress ProgressFunc) (*BackupResult, error) {
    progress(item.Name, 0, "starting backup")
    // Backup logic here
    progress(item.Name, 100, "backup complete")
    return &BackupResult{
        ItemName: item.Name,
        Success:  true,
    }, nil
}

func (h *MyHandler) Restore(item BackupItem, source string, progress ProgressFunc) error {
    progress(item.Name, 0, "starting restore")
    // Restore logic here
    progress(item.Name, 100, "restore complete")
    return nil
}

func (h *MyHandler) ListItems() ([]BackupItem, error) {
    // List available items
    return nil, nil
}
```

## Step 2: Add Build Tags (if platform-specific)

If the handler requires platform-specific APIs:

- Add `//go:build linux` to the real implementation
- Create `myhandler_stub.go` with `//go:build !linux` that returns stub/error responses

## Step 3: Wire to Execution Path

Connect the handler to the job execution system so it gets called for the appropriate backup type.

## Step 4: Test

Write tests covering:

- Successful backup and restore
- Error handling (permission denied, disk full, connection lost)
- Progress callback is called with meaningful values
- ListItems returns expected results

## Step 5: Verify

```bash
go test ./internal/engine/... -v
make lint
make pre-commit-run
```
