# Frontend Linting Setup & UX Polish

**Date:** 2026-03-05
**Status:** Approved

## Problem

- No frontend linter configured — Svelte build shows 10 a11y warnings with no enforcement
- No systematic UX review has been done to ensure professional visual quality

## Design

### Phase 1: ESLint Setup

Add ESLint with Svelte plugin to `web/`:

**Dependencies:** `eslint`, `eslint-plugin-svelte`, `@eslint/js`, `globals`

**Config:** `web/eslint.config.js`

- `@eslint/js` recommended rules
- `eslint-plugin-svelte` recommended rules (includes a11y)
- Browser globals
- Ignore `dist/` output

**Integration:**

- Add `"lint": "eslint src/"` to `web/package.json`
- Add `lint-web` target to Makefile

### Phase 2: Fix All Lint Issues

Fix all issues reported by ESLint:

- 6x `a11y_label_has_associated_control` — add `for`/`id` attributes or wrap inputs
- 2x `a11y_click_events_have_key_events` — add keyboard handlers or use `<button>`
- 1x `a11y_autofocus` — evaluate and fix or suppress with justification
- Any additional issues ESLint catches (unused vars, etc.)

### Phase 3: UX Visual Review with Playwright

Navigate every page at `http://192.168.20.21:24085` and evaluate:

- Layout & alignment — consistent spacing, proper grid alignment, no overflow
- Typography — clear hierarchy, readable sizes, consistent weights
- Color & contrast — readable text, consistent color usage
- Components — buttons, cards, modals polished and consistent
- Empty states — graceful when no data
- Responsive — works at different viewport widths
- Navigation — professional sidebar/header, clear active states

Document and fix any UX issues found.

## Task IDs

- #7: ESLint setup
- #8: Fix lint issues
- #9: Deploy and run Playwright UX review
- #10: Fix UX issues found
