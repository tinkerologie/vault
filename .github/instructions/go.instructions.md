---
applyTo: "**/*.go"
---

# Go Instructions

- Use `gofmt` and `goimports`.
- Follow existing package naming and error-handling style.
- Wrap errors with operation context using `%w`; use `errors.Is` or `errors.As`
  when callers need classification.
- Log once at the handling boundary, not at every return.
- Propagate `context.Context`; do not store request contexts in long-lived
  structs.
- Keep `CGO_ENABLED=0` compatibility.
- Use build tags and stubs for platform-specific implementations.
- Find every caller before changing a shared function, interface, or behavior.
- Prefer the standard library and existing project helpers over new
  dependencies or abstractions.
- Run targeted tests, then `make test` and `make lint` when the change warrants
  the full Go gate.
