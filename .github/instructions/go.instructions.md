---
applyTo: "**/*.go"
---

# Go Code Conventions

Reference: [`AGENTS.md`](../../AGENTS.md) for full project context.

## Style

- Standard Go: `gofmt` and `goimports` enforced
- Zero tolerance for linting errors (`golangci-lint`)
- PascalCase for exported names, camelCase for unexported
- Group imports: stdlib, external, internal (separated by blank lines)

## Error Handling

- Always wrap errors with context: `fmt.Errorf("doing X: %w", err)`
- Return errors up the call stack; don't swallow them silently
- Use `errors.Is()` / `errors.As()` for error checking
- Log at the point of handling, not at every intermediate return

## Build Constraints

- CGO_ENABLED=0 always — pure Go only (`modernc.org/sqlite`)
- Platform-specific code uses build tags: `//go:build linux` + `//go:build !linux` stub
- Always provide a stub file for non-Linux platforms

## Concurrency

- Scheduler and WebSocket hub run in goroutines
- Context cancellation for graceful shutdown
- Never store context in a struct
