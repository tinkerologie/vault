# Vault UX Redesign — Consolidated Implementation Plan

**Date:** 2026-03-02
**Status:** Ready to implement
**Source:** [UX Redesign Design Doc](2026-03-02-ux-redesign-design.md) + Phase 1–4 plans
**Current state:** All 35 tasks across 4 phases are **pending**

---

## Executive Summary

The UX redesign is a **35-task effort across 4 sequential phases**, transforming Vault's web UI from functional-but-basic to a polished, user-friendly experience. The work is ~80% frontend (Svelte 5 + Tailwind) and ~20% backend (Go API endpoints).

### What already exists (baseline)

- Full CRUD API for jobs, storage, settings, replication, activity
- 8 pages: Dashboard, Jobs, Storage, History, Restore, Logs, Settings, Replication
- Core components: EmptyState, Toast, ConfirmDialog, Modal, Spinner, PathBrowser, ScheduleBuilder, ItemPicker
- `statusBadge()`, `formatBytes()`, `relTime()` utilities
- `scheduler.NextRun()` method (Go — exists but **not wired to any HTTP route**)

### What needs to be built

- **2 new backend endpoints** (next-runs API, health summary API)
- **1 backend modification** (history pagination)
- **8 new Svelte components** (Welcome, SetupWizard, HealthGauge, ActivityTimeline, Skeleton, SizeChart, CommandPalette, PullToRefresh)
- **CSS utility system** (`.btn-*`, `.badge-*` classes)
- **Utility functions** (`describeSchedule`, `relTimeUntil`)
- **Significant modifications** to all 8 existing pages

---

## Phase 1: Quick Wins (Foundation)

> **Goal:** Visual consistency, interactive stats, human-readable schedules, inline failure reasons, better empty states, loading skeletons, mobile fixes.
> **Scope:** 12 tasks — 1 backend, 11 frontend
> **Dependencies:** None (this is the foundation)

### Dependency graph

```
Task 1 (next-run API) ─────────┐
                                ├─→ Task 4 (clickable stats + next time)
Task 2 (CSS button classes) ───┐├─→ Task 5 (schedule display on job cards)
                               │├─→ Task 11 (apply buttons across pages)
Task 3 (utils update) ─────────┘
Task 6 (inline failures)  ─ standalone
Task 7 (toast persistence) ─ standalone
Task 8 (empty states)      ─ standalone
Task 9 (loading skeletons)  ─ standalone
Task 10 (mobile stats fix) ─ standalone
Task 12 (final verification) ─ depends on all above
```

### Task list

| # | Task | Type | Files | Blocked by |
|---|------|------|-------|------------|
| 1 | **Add next-run API endpoint** — Wire `scheduler.NextRun()` to `GET /jobs/{id}/next-run` and `GET /jobs/next-runs` (bulk). Add `NextRunResolver` callback pattern to JobHandler + Server. Add `getNextRuns()`/`getNextRun()` to `api.js`. | Backend + API client | `handlers/jobs.go`, `routes.go`, `server.go`, `api.js` | — |
| 2 | **Unify button styles into CSS classes** — Add `.btn`, `.btn-primary`, `.btn-secondary`, `.btn-danger`, `.btn-ghost`, `.btn-sm`, `.btn-icon` and `.badge-*` classes to `app.css` using Tailwind `@apply`. | Frontend CSS | `app.css` | — |
| 3 | **Update statusBadge + add schedule/time utils** — Update `statusBadge()` to return `.badge` classes. Add `describeSchedule(cron)` and `relTimeUntil(dateStr)` functions. | Frontend utils | `utils.js` | Task 2 |
| 4 | **Make Dashboard stats cards clickable + show next backup time** — Wrap Jobs/Storage cards in buttons with navigation. Fetch next-runs, show "Next: in Xh Xm". Add derived `soonestNextRun`. | Frontend | `Dashboard.svelte` | Tasks 1, 3 |
| 5 | **Show human-readable schedules on Job cards** — Import `describeSchedule`/`relTimeUntil` from utils. Fetch next-runs. Show "Daily at 02:00 (in 5h 30m)" on cards. | Frontend | `Jobs.svelte` | Tasks 1, 3 |
| 6 | **Show inline failure reasons in History** — Add `getFailureReason()` helper that parses run logs. Display error text inline in failed run rows. | Frontend | `History.svelte` | — |
| 7 | **Improve Toast to persist error notifications** — Error toasts don't auto-dismiss; require manual close. Make dismiss button more prominent on errors. | Frontend | `Toast.svelte` | — |
| 8 | **Improve empty states across all pages** — Add `subtitle` prop to EmptyState. Update 6 pages with contextual, helpful empty messages and action buttons. | Frontend | `EmptyState.svelte`, `Jobs.svelte`, `Storage.svelte`, `History.svelte`, `Logs.svelte`, `Replication.svelte`, `Restore.svelte` | — |
| 9 | **Add loading skeletons** — Create `Skeleton.svelte` with card/stats/table variants. Replace Spinner loading states on Dashboard, Jobs, History. | Frontend | New: `Skeleton.svelte`. Modify: `Dashboard.svelte`, `Jobs.svelte`, `History.svelte` | — |
| 10 | **Fix mobile stats card layout** — Replace wrapping grid with horizontal scroll + snap on mobile. Fix orphaned 5th card problem. | Frontend | `Dashboard.svelte` | — |
| 11 | **Apply unified button classes across pages** — Replace scattered inline button styles with `.btn-*` classes on Jobs, Storage, Dashboard, Replication pages. | Frontend | `Jobs.svelte`, `Storage.svelte`, `Dashboard.svelte`, `Replication.svelte` | Task 2 |
| 12 | **Final verification** — `npm run build`, `go test ./... -short`, `make lint`, visual review (12-point checklist). | Verification | — | All above |

### Recommended execution order (parallelizable groups)

1. **Batch A** (no deps): Tasks 1, 2, 6, 7, 8, 9, 10 — all independent
2. **Batch B** (after A): Task 3 (needs Task 2)
3. **Batch C** (after B): Tasks 4, 5, 11 (need Tasks 1+3 or 2)
4. **Final**: Task 12

---

## Phase 2: Core Flows

> **Goal:** Onboarding wizard, health score gauge, activity timeline, inline job editing, settings restructure.
> **Scope:** 8 tasks — 1 backend, 7 frontend
> **Dependencies:** Phase 1 complete (uses unified CSS classes, utils, next-run endpoint)

### Dependency graph

```
Task 1 (health API) ──→ Task 4 (health gauge)
Task 2 (welcome screen) ──→ Task 3 (setup wizard)
Task 5 (activity timeline) ─ standalone
Task 6 (inline job editing) ─ standalone
Task 7 (settings tabs) ─ standalone
Task 8 (verification) ─ depends on all above
```

### Task list

| # | Task | Type | Files | Blocked by |
|---|------|------|-------|------------|
| 1 | **Add health summary API** — New `GET /health/summary` endpoint returning health score (weighted: 40% protection + 60% success rate), total/protected items, success rate, last success time. | Backend | New: `handlers/health.go`. Modify: `routes.go`, `api.js` | — |
| 2 | **Create onboarding welcome screen** — Full-page Welcome component shown when no storage AND no jobs. 3-step visual guide + "Get Started" button → navigates to Storage. | Frontend | New: `Welcome.svelte`. Modify: `Dashboard.svelte` | — |
| 3 | **Create setup wizard modal** — 4-step modal: Add Storage (with test) → Create Job (smart defaults) → Run First Backup → Done (celebration). Persistent progress banner with localStorage dismiss. | Frontend | New: `SetupWizard.svelte`. Modify: `Dashboard.svelte` | Task 2 |
| 4 | **Add Dashboard health score gauge** — SVG circular gauge (0–100%) with color coding (green/amber/red). Summary text below. Fetch from health summary API. | Frontend | New: `HealthGauge.svelte`. Modify: `Dashboard.svelte` | Task 1 |
| 5 | **Replace Recent Runs with Activity Timeline** — Date-grouped timeline (Today/Yesterday/date). Cards with status badge, duration, size, inline failure reason. Running backups show progress bar. | Frontend | New: `ActivityTimeline.svelte`. Modify: `Dashboard.svelte` | — |
| 6 | **Add inline editing to Job cards** — Enable/disable toggle switch. Double-click job name to rename. Item count summary. All changes save via API without requiring modal. | Frontend | `Jobs.svelte` | — |
| 7 | **Restructure Settings into tabbed layout** — 4 tabs: General, Security, Notifications, Reference. Move theme toggle to sidebar footer for quick access. | Frontend | `Settings.svelte`, `App.svelte` | — |
| 8 | **Phase 2 verification** — Build, test, lint + 12-point visual checklist. | Verification | — | All above |

### Recommended execution order

1. **Batch A** (no deps): Tasks 1, 2, 5, 6, 7
2. **Batch B** (after A): Tasks 3, 4
3. **Final**: Task 8

---

## Phase 3: Advanced Features

> **Goal:** Restore wizard, history redesign with filtering/chart, storage health, logs improvements, command palette, bulk operations, quick restore.
> **Scope:** 9 tasks — 1 backend mod, 8 frontend
> **Dependencies:** Phase 1 + Phase 2 complete

### Dependency graph

```
Task 1 (restore wizard) ──→ Task 8 (quick restore)
Task 2 (history timeline) ──→ Task 3 (size chart)
Task 4 (storage health) ─ standalone
Task 5 (logs improvements) ─ standalone
Task 6 (command palette) ─ standalone
Task 7 (bulk operations) ─ standalone
Task 9 (verification) ─ depends on all above
```

### Task list

| # | Task | Type | Files | Blocked by |
|---|------|------|-------|------------|
| 1 | **Redesign Restore as 3-step wizard** — Step 1: searchable visual grid of items grouped by type. Step 2: timeline of restore points with "Recommended" highlight. Step 3: restore options + overwrite warning. Dedicated progress view post-restore. | Frontend | New: `RestoreWizard.svelte`. Modify: `Restore.svelte` | — |
| 2 | **Redesign History as date-grouped timeline** — Card-style entries grouped by date. Status filter buttons (All/Completed/Failed/Running). Search by item name. "Load more" pagination. Backend: add `offset` param to history query. | Both | `History.svelte`, `handlers/jobs.go`, `job_repo.go`, `api.js` | — |
| 3 | **Add backup size trends sparkline** — Inline SVG chart (no library) showing size over last 30 successful runs. Hover shows date + size. | Frontend | New: `SizeChart.svelte`. Modify: `History.svelte` | Task 2 |
| 4 | **Add storage health indicators** — Type-specific icons (folder/server/cloud/network). Persistent test connection results on card. Dependent job count. Last successful write timestamp (may need model addition). | Both | `Storage.svelte`, optionally `handlers/storage.go` | — |
| 5 | **Improve Logs page** — Log level filter (Error/Warning/Info). Red left border for errors, auto-expand. Copy-to-clipboard per entry. Auto-scroll toggle. Export as `.txt` file. | Frontend | `Logs.svelte` | — |
| 6 | **Add global search / command palette** — `Ctrl+K` overlay. Search jobs, storage, pages. Keyboard navigation (↑↓ Enter Esc). Recent searches. | Frontend | New: `CommandPalette.svelte`. Modify: `App.svelte` | — |
| 7 | **Add bulk operations to Jobs** — Checkbox selection on cards. Floating action bar: Enable All, Disable All, Run Selected, Delete. Select all toggle. | Frontend | `Jobs.svelte` | — |
| 8 | **Add quick restore from Dashboard** — "Restore" button per protected item. Navigate to Restore page with item pre-selected via URL param. | Frontend | `Dashboard.svelte`, `Restore.svelte` | Task 1 |
| 9 | **Phase 3 verification** — Build, test, lint + 18-point visual checklist. | Verification | — | All above |

### Recommended execution order

1. **Batch A** (no deps): Tasks 1, 2, 4, 5, 6, 7
2. **Batch B** (after A): Tasks 3, 8
3. **Final**: Task 9

---

## Phase 4: Polish

> **Goal:** Mobile bottom nav, pull-to-refresh, job duplication, theme toggle shortcut, accessibility.
> **Scope:** 6 tasks — all frontend
> **Dependencies:** Phase 1 + 2 + 3 complete

### Task list

| # | Task | Type | Files | Blocked by |
|---|------|------|-------|------------|
| 1 | **Add mobile bottom navigation bar** — Fixed bottom nav (`lg:hidden`) with 5 quick-access items (Home, Jobs, History, Restore, More). Active state highlighting. Bottom padding for main content. | Frontend | `App.svelte`, `app.css` | — |
| 2 | **Add pull-to-refresh** — Touch-based pull-to-refresh wrapper for mobile. Apply to Dashboard and History pages. Spinner animation during refresh. | Frontend | New: `PullToRefresh.svelte`. Modify: `Dashboard.svelte`, `History.svelte` | — |
| 3 | **Add job duplication** — "Duplicate" action on job cards. Clones all job settings with "(copy)" suffix, disabled by default, opens wizard at review step. | Frontend | `Jobs.svelte` | — |
| 4 | **Enhance theme toggle accessibility** — `Ctrl+Shift+L` keyboard shortcut to cycle themes. Tooltip showing current theme on sidebar button. | Frontend | `App.svelte` | — |
| 5 | **Improve action button accessibility** — Add `aria-label` and `title` to all icon-only buttons across Jobs, Storage, Replication pages. | Frontend | `Jobs.svelte`, `Storage.svelte`, `Replication.svelte` | — |
| 6 | **Phase 4 verification** — Full build + test + lint + 16-point visual checklist. | Verification | — | All above |

### Recommended execution order

All tasks 1–5 are independent → execute in parallel, then Task 6.

---

## Cross-Phase Summary

### By file impact (most-modified files)

| File | Phase 1 | Phase 2 | Phase 3 | Phase 4 | Total touches |
|------|---------|---------|---------|---------|---------------|
| `Dashboard.svelte` | 3 tasks | 4 tasks | 1 task | 1 task | **9** |
| `Jobs.svelte` | 3 tasks | 1 task | 1 task | 2 tasks | **7** |
| `History.svelte` | 2 tasks | — | 2 tasks | 1 task | **5** |
| `App.svelte` | — | 1 task | 1 task | 2 tasks | **4** |
| `api.js` | 1 task | 1 task | 1 task | — | **3** |
| `utils.js` | 1 task | — | — | — | **1** |
| `app.css` | 1 task | — | — | 1 task | **2** |

### By type

| Category | Count |
|----------|-------|
| New backend endpoints | 2 (next-runs, health summary) |
| Backend modifications | 1 (history pagination) |
| New Svelte components | 8 |
| Page modifications | All 8 pages |
| CSS system additions | Button + badge utilities |
| Utility functions | 2 new (`describeSchedule`, `relTimeUntil`) |
| Verification tasks | 4 (one per phase) |

### Estimated effort

| Phase | Tasks | Backend | Frontend | Effort |
|-------|-------|---------|----------|--------|
| Phase 1 | 12 | 1 endpoint | 11 UI tasks | ~2 days |
| Phase 2 | 8 | 1 endpoint | 7 UI tasks | ~2 days |
| Phase 3 | 9 | 1 mod | 8 UI tasks | ~3 days |
| Phase 4 | 6 | — | 6 UI tasks | ~1 day |
| **Total** | **35** | **3** | **32** | **~8 days** |

---

## How to Execute

Each phase has a detailed step-by-step plan in its own document:

- [Phase 1: Quick Wins](2026-03-02-phase1-quick-wins.md) — 12 tasks
- [Phase 2: Core Flows](2026-03-02-phase2-core-flows.md) — 8 tasks
- [Phase 3: Advanced Features](2026-03-02-phase3-advanced-features.md) — 9 tasks
- [Phase 4: Polish](2026-03-02-phase4-polish.md) — 6 tasks

**To start implementation**, work through Phase 1 task-by-task following the detailed steps in the plan doc. Each task includes:
- Exact file paths and code snippets
- Step-by-step instructions
- Commit messages following Conventional Commits
- Verification steps

**Key rules:**
- Complete each phase before starting the next (dependency chain)
- Within a phase, respect the `blockedBy` deps in the task JSON files
- Run `npm run build` + `go test ./... -short` + `make lint` at each phase end
- Follow existing patterns: `$state`/`$derived` runes, `api.js` client, Toast for feedback
