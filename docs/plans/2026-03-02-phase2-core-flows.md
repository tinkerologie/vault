# Phase 2: Core Flows Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:executing-plans to implement this plan task-by-task.

**Goal:** Add the onboarding wizard, dashboard health score, activity timeline, jobs inline editing, and settings tabbed restructure — the core user experience flows that make Vault approachable for new users and efficient for returning users.

**Architecture:** Primarily frontend work (Svelte 5 + Tailwind). One backend addition: a `/api/v1/health/summary` endpoint that returns aggregated health data. All new components follow existing patterns: `$state`/`$derived` runes, `api.js` client calls, Toast for feedback.

**Tech Stack:** Svelte 5, Tailwind CSS, Go (one new endpoint)

**Depends on:** Phase 1 complete (unified CSS classes, utils, next-run endpoint)

---

### Task 1: Add backend health summary endpoint

**Files:**

- Create: `internal/api/handlers/health.go`
- Modify: `internal/api/routes.go`

Provides a single endpoint returning aggregated data for the health score: total items, protected items, recent run success rate, last successful backup time, storage health.

**Step 1: Create health handler**

Create `internal/api/handlers/health.go`:

```go
package handlers

import (
 "net/http"
 "time"

 "github.com/ruaandeysel/vault/internal/db"
)

type HealthHandler struct {
 db *db.DB
}

func NewHealthHandler(database *db.DB) *HealthHandler {
 return &HealthHandler{db: database}
}

func (h *HealthHandler) Summary(w http.ResponseWriter, r *http.Request) {
 jobs, _ := h.db.ListJobs()

 var totalItems, protectedItems int
 var recentSuccess, recentFailed int
 var lastSuccessTime *time.Time

 for _, job := range jobs {
  items, _ := h.db.GetJobItems(job.ID)
  totalItems += len(items)
  if job.Enabled {
   protectedItems += len(items)
  }

  runs, _ := h.db.GetJobHistory(job.ID, 10)
  for _, run := range runs {
   if run.Status == "success" || run.Status == "completed" {
    recentSuccess++
    if run.CompletedAt != nil && (lastSuccessTime == nil || run.CompletedAt.After(*lastSuccessTime)) {
     lastSuccessTime = run.CompletedAt
    }
   } else if run.Status == "failed" || run.Status == "error" {
    recentFailed++
   }
  }
 }

 totalRuns := recentSuccess + recentFailed
 successRate := 0
 if totalRuns > 0 {
  successRate = (recentSuccess * 100) / totalRuns
 }

 protectionPct := 0
 if totalItems > 0 {
  protectionPct = (protectedItems * 100) / totalItems
 }

 // Health score: weighted average of protection % and success rate
 healthScore := (protectionPct*40 + successRate*60) / 100

 result := map[string]any{
  "health_score":     healthScore,
  "total_items":      totalItems,
  "protected_items":  protectedItems,
  "protection_pct":   protectionPct,
  "success_rate":     successRate,
  "recent_success":   recentSuccess,
  "recent_failed":    recentFailed,
  "last_success_at":  lastSuccessTime,
 }
 respondJSON(w, http.StatusOK, result)
}
```

**Step 2: Register route**

In `internal/api/routes.go`, inside the authenticated group, add:

```go
healthH := NewHealthHandler(s.db)
r.Get("/health/summary", healthH.Summary)
```

Note: The existing `/health` is public. This `/health/summary` is authenticated since it exposes backup details.

**Step 3: Add API client function**

In `web/src/lib/api.js`:

```js
getHealthSummary: () => request('/health/summary'),
```

**Step 4: Run tests and commit**

```bash
go test ./internal/api/... -v -short
git add internal/api/handlers/health.go internal/api/routes.go web/src/lib/api.js
git commit -m "feat: add health summary API endpoint for dashboard score"
```

---

### Task 2: Create onboarding welcome screen

**Files:**

- Create: `web/src/components/Welcome.svelte`
- Modify: `web/src/pages/Dashboard.svelte`

**Step 1: Create Welcome component**

Create `web/src/components/Welcome.svelte`:

```svelte
<script>
  let { onstart = () => {} } = $props()
</script>

<div class="flex flex-col items-center justify-center min-h-[60vh] text-center px-4">
  <div class="text-6xl mb-4">🔒</div>
  <h1 class="text-3xl font-bold mb-2">Welcome to Vault</h1>
  <p class="text-text-muted max-w-md mb-8">
    Protect your Unraid server with automated backups of Docker containers, VMs, and folders.
  </p>

  <div class="flex flex-col sm:flex-row gap-6 mb-10 text-left">
    <div class="flex items-start gap-3">
      <span class="flex items-center justify-center w-8 h-8 rounded-full bg-vault text-white text-sm font-bold flex-shrink-0">1</span>
      <div>
        <p class="font-medium">Add Storage</p>
        <p class="text-sm text-text-dim">Choose where backups are saved</p>
      </div>
    </div>
    <div class="flex items-start gap-3">
      <span class="flex items-center justify-center w-8 h-8 rounded-full bg-vault/20 text-vault text-sm font-bold flex-shrink-0">2</span>
      <div>
        <p class="font-medium">Create a Job</p>
        <p class="text-sm text-text-dim">Pick what to back up and when</p>
      </div>
    </div>
    <div class="flex items-start gap-3">
      <span class="flex items-center justify-center w-8 h-8 rounded-full bg-vault/20 text-vault text-sm font-bold flex-shrink-0">3</span>
      <div>
        <p class="font-medium">Run Backup</p>
        <p class="text-sm text-text-dim">Protect your data in minutes</p>
      </div>
    </div>
  </div>

  <button onclick={onstart} class="btn btn-primary text-base px-8 py-3">
    Get Started
  </button>
</div>
```

**Step 2: Integrate into Dashboard**

In `Dashboard.svelte`, import Welcome and show it when no storage AND no jobs exist:

```svelte
import Welcome from '../components/Welcome.svelte'
```

In the template, before the stats grid:

```svelte
{#if !loading && storage.length === 0 && jobs.length === 0}
  <Welcome onstart={() => navigate('/storage')} />
{:else}
  <!-- existing dashboard content -->
{/if}
```

**Step 3: Commit**

```bash
git add web/src/components/Welcome.svelte web/src/pages/Dashboard.svelte
git commit -m "feat: add welcome screen for first-time users"
```

---

### Task 3: Create setup wizard modal

**Files:**

- Create: `web/src/components/SetupWizard.svelte`
- Modify: `web/src/pages/Dashboard.svelte`

A 4-step modal wizard: Add Storage > Create Job > Run First Backup > Done. Reuses existing form patterns from Storage and Jobs pages.

**Step 1: Create SetupWizard component**

The wizard manages its own state and calls the same API endpoints as the individual pages. Each step is self-contained:

- Step 1: Storage form (name, type, config) + inline test connection (must pass to proceed)
- Step 2: Job form with pre-populated defaults (name="Daily Backup", all containers selected, schedule="0 2 \* \* \*", compression="zstd")
- Step 3: "Run Now" button or "I'll wait for the schedule" link
- Step 4: Success celebration with confetti animation + "Go to Dashboard" button

Component accepts `onclose` and `oncomplete` callbacks.

**Step 2: Wire wizard to Dashboard**

Show a "Complete Setup" banner on Dashboard when setup is partially complete (has storage but no jobs, or has jobs but none have run). Clicking it opens the wizard at the appropriate step.

**Step 3: Add persistent setup progress banner**

Add a dismissable banner component that tracks setup state:

- Step 1 complete: storage exists
- Step 2 complete: at least one job exists
- Step 3 complete: at least one successful run exists

Store dismissal in localStorage.

**Step 4: Commit**

```bash
git add web/src/components/SetupWizard.svelte web/src/pages/Dashboard.svelte
git commit -m "feat: add setup wizard for guided first-time configuration"
```

---

### Task 4: Add Dashboard health score gauge

**Files:**

- Create: `web/src/components/HealthGauge.svelte`
- Modify: `web/src/pages/Dashboard.svelte`

**Step 1: Create HealthGauge component**

SVG-based circular gauge showing 0-100% health score. Uses CSS variables for theming.

```svelte
<script>
  let { score = 0, summary = '' } = $props()

  let color = $derived(
    score >= 80 ? 'var(--color-success)' :
    score >= 50 ? 'var(--color-warning)' :
    'var(--color-danger)'
  )

  // SVG arc calculation for circular progress
  let circumference = 2 * Math.PI * 45 // radius = 45
  let dashOffset = $derived(circumference - (score / 100) * circumference)
</script>

<div class="flex items-center gap-6 bg-surface-2 border border-border rounded-xl p-6">
  <div class="relative w-28 h-28 flex-shrink-0">
    <svg viewBox="0 0 100 100" class="w-full h-full -rotate-90">
      <circle cx="50" cy="50" r="45" fill="none" stroke="var(--color-border)" stroke-width="8" />
      <circle cx="50" cy="50" r="45" fill="none" stroke={color}
        stroke-width="8" stroke-linecap="round"
        stroke-dasharray={circumference} stroke-dashoffset={dashOffset}
        class="transition-all duration-1000 ease-out" />
    </svg>
    <div class="absolute inset-0 flex items-center justify-center">
      <span class="text-2xl font-bold">{score}%</span>
    </div>
  </div>
  <div>
    <h3 class="text-lg font-semibold">Backup Health</h3>
    <p class="text-sm text-text-muted mt-1">{summary}</p>
  </div>
</div>
```

**Step 2: Integrate into Dashboard**

Fetch health summary in `loadDashboard()`, pass score and summary text to HealthGauge. Place above the stats grid.

Summary text logic:

- score >= 80: "All backups healthy"
- score >= 50: Build string from issues: "X items unprotected, Y failures recently"
- score < 50: "Attention needed — X items unprotected, Y recent failures"

**Step 3: Commit**

```bash
git add web/src/components/HealthGauge.svelte web/src/pages/Dashboard.svelte
git commit -m "feat: add health score gauge to dashboard"
```

---

### Task 5: Replace Recent Backup Runs with Activity Timeline

**Files:**

- Create: `web/src/components/ActivityTimeline.svelte`
- Modify: `web/src/pages/Dashboard.svelte`

**Step 1: Create ActivityTimeline component**

Replace the simple list of recent runs with a date-grouped timeline:

```svelte
<script>
  import { statusBadge, relTime, formatBytes } from '../lib/utils.js'

  let { runs = [], maxItems = 8 } = $props()

  // Group by date
  let grouped = $derived(() => {
    const groups = {}
    const today = new Date().toDateString()
    const yesterday = new Date(Date.now() - 86400000).toDateString()

    for (const run of runs.slice(0, maxItems)) {
      const d = new Date(run.started_at).toDateString()
      const label = d === today ? 'Today' : d === yesterday ? 'Yesterday' : d
      if (!groups[label]) groups[label] = []
      groups[label].push(run)
    }
    return Object.entries(groups)
  })
</script>
```

Each run entry shows: job name, status badge, duration, size, and **failure reason inline** (reuse `getFailureReason` from Phase 1 History task — extract to utils if not already). Running backups show a progress bar.

**Step 2: Integrate into Dashboard**

Replace the "Recent Backup Runs" card with `<ActivityTimeline runs={recentRuns} />`.

**Step 3: Commit**

```bash
git add web/src/components/ActivityTimeline.svelte web/src/pages/Dashboard.svelte
git commit -m "feat: replace recent runs with date-grouped activity timeline"
```

---

### Task 6: Add inline editing to Job cards

**Files:**

- Modify: `web/src/pages/Jobs.svelte`

**Step 1: Add enable/disable toggle to job cards**

Add a toggle switch directly on each job card that calls `api.updateJob()` with just the `enabled` field toggled. No modal needed.

```svelte
<button
  onclick|stopPropagation={() => toggleJob(job)}
  class="relative w-10 h-5 rounded-full transition-colors {job.enabled ? 'bg-vault' : 'bg-surface-4'}"
  title={job.enabled ? 'Disable job' : 'Enable job'}
>
  <span class="absolute top-0.5 left-0.5 w-4 h-4 rounded-full bg-white transition-transform {job.enabled ? 'translate-x-5' : ''}" />
</button>
```

Add the toggle handler:

```js
async function toggleJob(job) {
  const updated = { ...job, enabled: !job.enabled };
  await api.updateJob(job.id, updated);
  await loadData();
  toast = {
    message: `Job ${updated.enabled ? "enabled" : "disabled"}`,
    type: "success",
    key: Date.now(),
  };
}
```

**Step 2: Add inline name editing**

Double-click on job name to edit inline. Show an input field that saves on Enter or blur:

```svelte
{#if editingNameId === job.id}
  <input
    type="text" bind:value={editName}
    onkeydown={(e) => { if (e.key === 'Enter') saveJobName(job) }}
    onblur={() => saveJobName(job)}
    class="text-lg font-semibold bg-transparent border-b border-vault outline-none"
    autofocus
  />
{:else}
  <h3 ondblclick={() => startNameEdit(job)} class="text-lg font-semibold cursor-text" title="Double-click to rename">
    {job.name}
  </h3>
{/if}
```

**Step 3: Enrich card metadata**

Add item count summary to each card. Fetch job items alongside jobs:

```svelte
<span class="text-text-dim text-xs">{job.item_count} items</span>
```

This requires either enriching the List endpoint to include item counts, or fetching items per job client-side. Client-side approach: `Promise.all(jobs.map(j => api.getJob(j.id)))` to get items — but this is N+1. Better to add an `item_count` to the list response or accept the simplicity of showing what's in the existing response.

**Step 4: Commit**

```bash
git add web/src/pages/Jobs.svelte
git commit -m "feat: add inline toggle and name editing to job cards"
```

---

### Task 7: Restructure Settings into tabbed layout

**Files:**

- Modify: `web/src/pages/Settings.svelte`

**Step 1: Add tab state and navigation**

Add tab state and render tabs at the top:

```js
let activeTab = $state("general");
const tabs = [
  { id: "general", label: "General" },
  { id: "security", label: "Security" },
  { id: "notifications", label: "Notifications" },
  { id: "reference", label: "Reference" },
];
```

Tab bar UI:

```svelte
<div class="flex gap-1 border-b border-border mb-6">
  {#each tabs as tab}
    <button
      onclick={() => activeTab = tab.id}
      class="px-4 py-2 text-sm font-medium border-b-2 transition-colors {activeTab === tab.id ? 'border-vault text-vault' : 'border-transparent text-text-muted hover:text-text'}"
    >
      {tab.label}
    </button>
  {/each}
</div>
```

**Step 2: Reorganize existing sections into tabs**

- **General tab**: Appearance (theme toggle), Server Information, About Vault
- **Security tab**: Encryption, API Access
- **Notifications tab**: Unraid Notifications
- **Reference tab**: Compression Guide, API Endpoints

Wrap each section group in `{#if activeTab === 'xxx'}...{/if}`.

**Step 3: Move theme toggle to sidebar**

In `App.svelte`, add a small sun/moon icon button in the sidebar footer (near the "Connected" indicator) that cycles through light/system/dark. This gives quick theme access without navigating to Settings.

**Step 4: Commit**

```bash
git add web/src/pages/Settings.svelte web/src/App.svelte
git commit -m "feat: restructure settings into tabbed layout, add theme toggle to sidebar"
```

---

### Task 8: Phase 2 final verification

**Files:** None (verification only)

**Step 1: Build and test**

```bash
cd web && npm run build
go test ./... -short
make lint
```

**Step 2: Visual verification checklist**

- [ ] First-time user sees Welcome screen (clear storage + jobs to test)
- [ ] Setup wizard walks through storage → job → first run
- [ ] Setup progress banner shows/dismisses correctly
- [ ] Health gauge displays and animates on Dashboard
- [ ] Health score changes color based on value
- [ ] Activity timeline groups runs by date with inline errors
- [ ] Job cards have working enable/disable toggle
- [ ] Double-click renames job inline
- [ ] Settings tabs work (General/Security/Notifications/Reference)
- [ ] Theme toggle in sidebar works
- [ ] All changes look correct in dark mode
- [ ] Mobile layout is intact

**Step 3: Commit any fixes**

```bash
git add -A && git commit -m "fix: phase 2 final adjustments"
```
