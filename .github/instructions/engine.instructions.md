---
applyTo: "internal/engine/**/*.go"
---

# Engine Instructions

Reference: [`AGENTS.md`](../../AGENTS.md) for full project context.

## Handler Interface

All backup/restore handlers implement `engine.Handler`:

```go
type Handler interface {
    Backup(item BackupItem, dest string, progress ProgressFunc) (*BackupResult, error)
    Restore(item BackupItem, source string, progress ProgressFunc) error
    ListItems() ([]BackupItem, error)
}
```

## Build Tags

- `vm.go` and `fileutil.go`: `//go:build linux` (real libvirt + file operations)
- `vm_stub.go`: `//go:build !linux` (stubs for macOS/Windows)
- Always add a stub when creating Linux-only code

## Container Handler

Uses Docker Engine SDK (`github.com/docker/docker`):

1. Stop container
2. Export image as tar
3. Tar bind-mount volumes
4. Restart container

Progress reported via `ProgressFunc` callback.

## VM Handler

Uses libvirt (`libvirt.org/go/libvirt`) on Linux only:

- Connects to `qemu:///system`
- Copies disk images and NVRAM files
- Uses `copyFileWithProgress` from `fileutil.go`

## Error Handling

- Wrap all errors with operation context
- Use `_ = file.Close()` for cleanup in error paths
- Report meaningful progress percentages
