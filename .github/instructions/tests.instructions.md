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
- Use Vitest for `web/src/**/*.test.js`. Pure logic and rune stores in
  `web/src/lib` run in the Node `unit` project.
- Use a `*.component.test.js` file under `web/src/pages` or
  `web/src/components` (jsdom `component` project, Testing Library) for a
  page's wiring: guards, effects, lifecycle and failed fetches. Drive it
  through rendered events and store or API updates; never export private
  handlers. Test failed fetches through the real store with a mocked
  `api.js`. Stub layout with `web/src/test-support/scroll-box.js`.
- Use Playwright only for real layout, smooth scrolling, pixel anchoring and
  multi-page flows.
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
(cd web && npx vitest run --project unit)
(cd web && npx vitest run --project component)
npx playwright test
```
