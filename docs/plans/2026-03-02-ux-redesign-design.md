# Vault UX Redesign - Design Document

**Date:** 2026-03-02
**Status:** Approved
**Approach:** Full Redesign (Approach C)
**Target users:** Both technical power users and average Unraid hobbyists

## Context

Vault is a backup daemon for Unraid servers with a Svelte 5 + Tailwind CSS web UI. The current UI is functional but lacks onboarding, has visual inconsistencies, and can be improved for both first-time and day-to-day usage. This design covers a comprehensive UX overhaul across all pages.

---

## 1. Dashboard Redesign

### 1a. Backup Health Score
- Circular gauge (0-100%) at top of Dashboard
- Calculated from: % items protected, recent success rate, time since last success, storage health
- Color: green (healthy) > amber (needs attention) > red (critical)
- One-line summary below: "All backups healthy" or "3 items unprotected, 2 failures in last 24h"

### 1b. Stats Cards (improved)
- Keep 5-card layout, make each **clickable** (navigates to relevant page)
- Add hover animation
- Jobs card: add "Next backup in 2h 15m"
- Replace Storage card with "Last Backup" card showing last success time
- Move storage info to Storage page

### 1c. Protection Status (improved)
- Keep 3-column breakdown (Containers/VMs/Folders)
- Add filter toggle: "Show all" / "Show unprotected only"
- Make items clickable to quick-add to backup job
- Surface duplicate VM entries as a data quality issue

### 1d. Recent Activity Timeline
- Replace "Recent Backup Runs" with timeline layout
- Date headers: "Today", "Yesterday", etc.
- Each entry: job name, status badge, duration, size, **failure reason inline**
- Running backups show live progress bar
- "View all" link to History

### 1e. Quick Actions Bar
- Floating action area: "Run Backup", "Add Storage", "Create Job"
- Contextual: "Fix Issues" if failures exist, "Complete Setup" if onboarding incomplete

---

## 2. Onboarding & First-Run Experience

### 2a. Welcome Screen
- Shown when no storage and no jobs exist
- Full-page: Vault logo + "Welcome to Vault" + description
- 3-step visual guide: Add Storage > Create Job > Run First Backup
- "Get Started" button launches setup wizard

### 2b. Setup Wizard
Modal wizard with 4 steps:
1. **Add Storage** - Simplified form with inline "Test Connection" (must pass to proceed)
2. **Create First Job** - Pre-selects all containers, suggests "Daily at 2 AM", names "Daily Backup"
3. **Run First Backup** - "Run Now" button or "I'll wait for the schedule"
4. **Done** - Celebration state with confetti/checkmark, links to Dashboard

### 2c. Persistent Progress Indicator
- Dismissable banner at top of Dashboard until all 3 steps complete
- "Setup: 1 of 3 complete" with progress dots
- Clicking resumes wizard where left off

### 2d. Contextual Empty States
Every page gets helpful empty states:
- Jobs: "Create your first backup job to protect your containers and VMs" + button
- Storage: "Add a storage destination before creating backup jobs" + button
- History: "No backup runs yet. Run your first backup from the Dashboard."

---

## 3. Jobs Page Improvements

### 3a. Enriched Job Cards
- Human-readable schedule: "Daily at 2:00 AM" (not "—")
- Enable/disable toggle directly on card
- Last run status + timestamp: "Last run: 1h ago (completed)"
- Next run time: "Next: Tomorrow at 2:00 AM"
- Item count summary: "14 containers, 1 VM"
- Action buttons: text labels on desktop, icon-only on mobile

### 3b. Inline Job Editing
- Click schedule text to edit inline
- Click job name to rename
- Toggle enabled/disabled with switch
- For complex changes, full wizard still available

### 3c. Job Wizard Improvements
- Step 2: Visual schedule preview ("Your backup will run: Mon 2am, Tue 2am...")
- Step 3: Estimated backup size based on container/VM sizes
- New "Duplicate Job" action on existing jobs

### 3d. Bulk Operations
- Checkbox selection on job cards
- Bulk actions: Enable All, Disable All, Run Selected, Delete Selected

---

## 4. History Page Redesign

### 4a. Date-Grouped Timeline View
- Group runs by date: "Today", "Yesterday", "March 1, 2026"
- Card-style entries (not table rows):
  - Job name, status badge, duration, total size
  - **Failure reason inline** (no expansion needed)
  - Success count: "13/15 items backed up"
  - Expandable per-item details

### 4b. Filtering & Search
- Job filter dropdown (keep)
- Status filter buttons: All | Completed | Failed | Running
- Date range picker
- Search by item name (e.g., "plex")

### 4c. Backup Size Trends Chart
- Sparkline chart at top showing backup size over time
- Toggle: "Total size" vs "Per-run size"
- Clickable data points jump to that run
- Helps spot unexpected growth

### 4d. Pagination
- "Load more" button (not traditional pagination)
- Show total: "Showing 20 of 156 runs"

---

## 5. Restore Flow Redesign

### 5a. Guided Restore Wizard
3-step flow replacing dropdown approach:

1. **What to restore** - Searchable visual grid of all backed-up items, grouped by type. Each shows icon, name, last backup date, restore point count.

2. **Which version** - Timeline of restore points for selected item:
   - Date, size, backup type badge
   - "Verified" badge if integrity passed
   - File/volume preview
   - Most recent success highlighted as "Recommended"

3. **Restore options** - Confirm destination (default: original), optional override with PathBrowser, encryption passphrase if needed, clear overwrite warning.

### 5b. Quick Restore from Dashboard
- "Restore" button per protected item in Protection Status section
- Jumps directly to version selection (step 2) for that item

### 5c. Restore Progress View
- Dedicated progress view (not just a toast)
- Per-item progress with status
- Estimated time remaining
- Collapsible log output

---

## 6. Storage Page Improvements

- **Modern icons** based on type (cloud/server/folder, not floppy disk)
- **Storage usage bar** (if API supports space queries)
- **Inline test connection results** (green checkmark / red X with message)
- **Dependent jobs count**: "Used by 2 jobs"
- **Health indicator**: last successful write timestamp

---

## 7. Logs Page Improvements

- **Log level filter** (Error/Warning/Info) alongside category filter
- **Priority error styling**: red left border, expanded by default
- **Copy log entry** button
- **Auto-scroll toggle** for live monitoring
- **Export logs** button (download .txt)

---

## 8. Settings Page Restructure

- **Tabbed layout**: General | Security | Notifications | Reference
- Move API Endpoints and Compression Guide to "Reference" tab
- Move theme toggle to **sidebar footer** or top-bar icon for quick access
- Keep current section content, just reorganize

---

## 9. Global Visual Consistency

### Button System
- **Primary**: Amber filled (main actions)
- **Secondary**: Outlined (alternative actions)
- **Destructive**: Red (delete, remove)
- **Ghost**: Text only (tertiary actions)
- Applied consistently across all pages

### Status Badges
- Unified pill style everywhere
- Success: green, Failed: red, Running: amber, Pending: gray

### Notifications
- Error toasts **persist until dismissed**
- Success toasts auto-dismiss at 4 seconds

### Loading States
- **Loading skeletons** instead of spinners for page loads
- Spinners retained for action buttons

### Card Design
- Consistent: subtle border, rounded-xl, hover:shadow transition

---

## 10. Global Search / Command Palette

- `Ctrl+K` or `/` opens search overlay
- Search across: jobs, storage, containers, VMs, restore points, settings
- Recent searches and quick actions
- Fully keyboard navigable

---

## 11. Mobile Improvements

- Stats cards: **horizontal scrollable row** (not wrapping grid)
- **Pull-to-refresh** on Dashboard and History
- **Bottom navigation bar** option (alternative to hamburger menu)
- Action buttons: icon-only with tooltips

---

## Implementation Priority

### Phase 1: Quick Wins (foundation)
1. Visual consistency (buttons, badges, toasts, loading skeletons)
2. Stats cards clickable + next backup time
3. Human-readable schedules on job cards
4. Inline failure reasons in History
5. Improved empty states
6. Mobile stats card layout fix

### Phase 2: Core Flows
7. Onboarding wizard + welcome screen
8. Dashboard health score
9. Dashboard activity timeline
10. Jobs inline editing + enriched cards
11. Settings tabbed restructure

### Phase 3: Advanced Features
12. Restore wizard redesign
13. History timeline view + filtering + chart
14. Storage improvements (usage bar, health)
15. Logs improvements
16. Global search / command palette
17. Job bulk operations
18. Quick restore from Dashboard

### Phase 4: Polish
19. Mobile bottom nav + pull-to-refresh
20. Theme toggle in sidebar
21. Log export
22. Job duplication
