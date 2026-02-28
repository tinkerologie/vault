---
description: Step-by-step guide for adding a new storage backend adapter
tools: ["editor", "terminal"]
---

# Add a New Storage Adapter

Follow these steps to add a new storage backend to Vault.

## Step 1: Create the Adapter File

Create `internal/storage/mybackend.go`:

```go
package storage

import (
    "encoding/json"
    "fmt"
    "io"
)

type MyBackendConfig struct {
    Host     string `json:"host"`
    Username string `json:"username"`
    Password string `json:"password"`
    BasePath string `json:"base_path"`
}

type MyBackendAdapter struct {
    config MyBackendConfig
}

func NewMyBackendAdapter(configJSON []byte) (*MyBackendAdapter, error) {
    var cfg MyBackendConfig
    if err := json.Unmarshal(configJSON, &cfg); err != nil {
        return nil, fmt.Errorf("parsing config: %w", err)
    }
    return &MyBackendAdapter{config: cfg}, nil
}

// Implement all Adapter interface methods:
// Write, Read, Delete, List, Stat, TestConnection

var _ Adapter = (*MyBackendAdapter)(nil)
```

## Step 2: Register in Factory

In `internal/storage/factory.go`, add a case to `NewAdapter()`:

```go
case "mybackend":
    return NewMyBackendAdapter(configJSON)
```

## Step 3: Add Storage Type Constant

In `internal/config/types.go`, add the new storage type constant.

## Step 4: Write Tests

Create `internal/storage/mybackend_test.go`:

- Test all Adapter methods
- Test `TestConnection()` for both success and failure
- Use `t.TempDir()` if applicable
- Test config parsing with valid and invalid JSON

## Step 5: Verify

```bash
go test ./internal/storage/... -v
make lint
make pre-commit-run
```
