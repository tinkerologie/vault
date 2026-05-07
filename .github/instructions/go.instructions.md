---
applyTo: "**/*.go"
---

# Go Code Conventions

Reference: [`AGENTS.md`](../../AGENTS.md) for full project context.

## Style

- Standard Go: `gofmt` and `goimports` enforced
- Linting must pass (`golangci-lint`)
- PascalCase for exported names, camelCase for unexported
- Group imports: stdlib, external, internal (separated by blank lines)

## Quick Reference (Use One Category Per Prompt)

| Category | Key Rules |
| --- | --- |
| Style | `gofmt` + `goimports`; `golangci-lint` passes; naming + import grouping |
| Error Handling | Wrap with `%w`; return (don’t swallow); `errors.Is/As`; log at handling boundary |
| Build Constraints | `CGO_ENABLED=0`; Linux build tags + non-Linux stub |
| Concurrency | Goroutines for scheduler/hub; context cancellation; no context in structs |

## Authoring Workflow (Reduce Constraint Overload)

- Apply conventions in focused passes instead of all at once:
	1. Style and imports
	2. Error handling
	3. Build constraints
	4. Concurrency
- When writing prompts/instructions, keep one primary category per prompt to reduce missed rules and inconsistent application.

## Error Handling

- Always wrap errors with context: `fmt.Errorf("doing X: %w", err)`
- Return errors up the call stack; don't swallow them silently
- If input data is invalid or malformed, return a descriptive error and log the issue at the handling boundary
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
