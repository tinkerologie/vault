---
applyTo: "**/*_test.go"
---

# Testing Instructions

Reference: [`AGENTS.md`](../../AGENTS.md) for full project context.

## Pattern: Table-Driven Tests

```go
func TestMyFunction(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    string
        wantErr bool
    }{
        {"valid input", "abc", "result", false},
        {"empty input", "", "", true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := MyFunction(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("MyFunction() error = %v, wantErr %v", err, tt.wantErr)
            }
            if got != tt.want {
                t.Errorf("MyFunction() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

## API Handler Tests

Use `httptest` for handler testing:

```go
req := httptest.NewRequest("GET", "/api/v1/jobs", nil)
w := httptest.NewRecorder()
handler.List(w, req)
```

## Storage Tests

Use `t.TempDir()` for storage adapter tests — auto-cleaned up after test.

## DB Tests

Use in-memory SQLite for fast tests: `Open(":memory:")`

## Conventions

- Tests alongside source files (`*_test.go`)
- Use `t.Helper()` in test helper functions
- Use `t.Parallel()` where tests are independent

## Commands

```bash
make test                                        # All tests
make test-coverage                               # Coverage report
go test ./internal/db/... -run TestJobCreate -v  # Specific test
```
