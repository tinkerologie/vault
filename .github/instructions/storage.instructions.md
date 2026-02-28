---
applyTo: "internal/storage/**/*.go"
---

# Storage Instructions

Reference: [`AGENTS.md`](../../AGENTS.md) for full project context.

## Adapter Interface

All storage backends implement `storage.Adapter`:

```go
type Adapter interface {
    Write(path string, reader io.Reader) error
    Read(path string) (io.ReadCloser, error)
    Delete(path string) error
    List(prefix string) ([]FileInfo, error)
    Stat(path string) (FileInfo, error)
    TestConnection() error
}
```

## Factory Pattern

`factory.go` contains `NewAdapter(storageType, configJSON)` which dispatches to concrete adapters. Each adapter has its own config struct parsed from JSON.

When adding a new adapter:

1. Create `mybackend.go` with config struct and constructor
2. Implement all `Adapter` interface methods
3. Add case to `NewAdapter()` switch in `factory.go`
4. Add compile-time interface check: `var _ Adapter = (*MyBackendAdapter)(nil)`

## Config Storage

Storage destination config is stored as a JSON blob in the `storage_destinations.config` DB column. Each adapter parses its own config struct from this JSON.

## TestConnection

Every adapter must implement `TestConnection()` that verifies:

- Connectivity to the backend
- Read/write permissions
- Cleans up any test artifacts

## Cleanup

- Use `_ = file.Close()` in error paths
- SMB: always `Umount` shares and `Logoff` sessions in defer
- SFTP: close both the sftp client and ssh connection
