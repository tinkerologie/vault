---
applyTo: "**/*_test.go,web/**/*.test.js,tests/**/*.spec.ts,playwright.config.ts"
---

# Test Instructions

- Test behavior and failure boundaries, not private implementation details.
- Keep tests deterministic and isolated; avoid fixed sleeps and external
  services when a local fake covers the contract.
- Use table-driven Go tests when multiple cases share one behavior.
- Use `httptest` for HTTP handlers and `t.TempDir()` for filesystem state.
- Use `t.Parallel()` only after confirming the test has no shared globals,
  environment, ports, database, or process state.
- Use Vitest for `web/src/**/*.test.js`.
- Use the root `playwright.config.ts` and `tests/` for browser tests.
- Prefer accessible Playwright locators and state-based waits.
- Do not create, run, restore, or delete live Vault data without explicit user
  approval.

Common checks:

```bash
make test
make test-short
make test-coverage
npm --prefix web test
npx playwright test
```
