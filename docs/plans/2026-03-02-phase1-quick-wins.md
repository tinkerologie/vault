# Phase 1: Quick Wins Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:executing-plans to implement this plan task-by-task.

**Goal:** Ship the highest-impact, lowest-risk UX improvements to the Vault web UI — visual consistency, interactive stats cards, human-readable schedules, inline failure reasons, better empty states, and mobile layout fixes.

**Architecture:** All changes are frontend-only except Task 1 (backend endpoint for next-run time). The frontend is Svelte 5 + Tailwind CSS. CSS variables for theming are in `web/src/app.css`. Shared utilities are in `web/src/lib/utils.js`. Existing patterns use `$state`, `$derived`, and `$effect` runes.

**Tech Stack:** Go (backend handler), Svelte 5, Tailwind CSS, robfig/cron/v3 (scheduler)

---

### Task 1: Add next-run API endpoint (backend)

**Files:**
- Modify: `internal/api/handlers/jobs.go`
- Modify: `internal/api/routes.go`
- Modify: `internal/api/server.go`
- Create: `internal/api/handlers/jobs_test.go` (if not exists, add test)

This task exposes the existing `Scheduler.NextRun()` method via a new API endpoint so the frontend can display "Next backup in X".

**Step 1: Add NextRunResolver type and field to JobHandler**

In `internal/api/handlers/jobs.go`, add a callback type and extend the handler:

```go
// After line 15 (existing ScheduleReloader type)
// NextRunResolver returns the next scheduled run time for a job.
type NextRunResolver = func(jobID int64) (string, bool)
```

Update the struct and constructor:

```go
type JobHandler struct {
	db          *db.DB
	runner      *runner.Runner
	schedReload ScheduleReloader
	nextRun     NextRunResolver
}

func NewJobHandler(database *db.DB, r *runner.Runner, reload ScheduleReloader) *JobHandler {
	return &JobHandler{db: database, runner: r, schedReload: reload}
}

// SetNextRunResolver sets the function used to look up the next scheduled run.
func (h *JobHandler) SetNextRunResolver(fn NextRunResolver) {
	h.nextRun = fn
}
```

**Step 2: Add the NextRun handler method**

Append to `internal/api/handlers/jobs.go`:

```go
func (h *JobHandler) NextRun(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if h.nextRun == nil {
		respondJSON(w, http.StatusOK, map[string]any{"scheduled": false})
		return
	}
	next, ok := h.nextRun(id)
	if !ok {
		respondJSON(w, http.StatusOK, map[string]any{"scheduled": false})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"scheduled": true, "next_run": next})
}
```

**Step 3: Add a bulk next-run endpoint for all jobs**

This avoids N+1 requests from the dashboard. Append to `internal/api/handlers/jobs.go`:

```go
func (h *JobHandler) AllNextRuns(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.db.ListJobs()
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result := make(map[string]any)
	for _, job := range jobs {
		if h.nextRun != nil {
			if next, ok := h.nextRun(job.ID); ok {
				result[strconv.FormatInt(job.ID, 10)] = next
			}
		}
	}
	respondJSON(w, http.StatusOK, result)
}
```

**Step 4: Register routes**

In `internal/api/routes.go`, inside the `/jobs` route group (after line 72), add:

```go
r.Get("/{id}/next-run", jobH.NextRun)
r.Get("/next-runs", jobH.AllNextRuns)
```

**Step 5: Wire up the scheduler in server.go**

In `internal/api/server.go`, find where `SetScheduleReloader` is called (it's called externally from `cmd/vault/main.go`). We need a similar pattern. Add to `server.go`:

```go
// SetNextRunResolver sets the function used by job handlers to look up next run times.
func (s *Server) SetNextRunResolver(fn func(jobID int64) (string, bool)) {
	// This is called after route setup, so we need to store it and
	// have the handler reference it. Since jobH is local to setupRoutes,
	// we store the resolver on Server and pass it through.
	s.nextRunResolver = fn
}
```

However, since `jobH` is local to `setupRoutes()`, the cleaner approach is to store `jobH` on the server or pass the resolver during setup. Check how `schedReload` is wired — it's passed as a constructor arg. For `nextRun`, add a field to Server:

In `internal/api/server.go`, add field to Server struct:

```go
nextRunResolver func(jobID int64) (string, bool)
```

Add the setter method and modify `setupRoutes` to call `jobH.SetNextRunResolver(s.nextRunResolver)` after creating jobH. Then in `cmd/vault/main.go`, call `srv.SetNextRunResolver(sched.NextRun)` alongside the existing `srv.SetScheduleReloader(sched.Reload)`.

**Step 6: Verify by running tests**

Run: `go test ./internal/api/... -v -short`
Expected: All existing tests pass, no regressions.

**Step 7: Add API client function in frontend**

In `web/src/lib/api.js`, add after the existing job functions (around line 51):

```js
getNextRuns: () => request('/jobs/next-runs'),
getNextRun: (id) => request(`/jobs/${id}/next-run`),
```

**Step 8: Commit**

```bash
git add internal/api/handlers/jobs.go internal/api/routes.go internal/api/server.go web/src/lib/api.js
git commit -m "feat: add next-run API endpoint for job scheduling display"
```

---

### Task 2: Unify button styles into CSS utility classes

**Files:**
- Modify: `web/src/app.css`

Currently button styles are scattered inline across all pages with slight variations. We'll add utility classes in `app.css` that every page can reference.

**Step 1: Add button utility classes to app.css**

Append before the closing of `web/src/app.css` (after line 75):

```css
/* Button system */
.btn {
  @apply px-4 py-2 text-sm font-medium rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed;
}
.btn-primary {
  @apply bg-vault text-white hover:bg-vault-dark;
}
.btn-secondary {
  @apply bg-surface-3 text-text-muted hover:bg-surface-4 hover:text-text;
}
.btn-danger {
  @apply bg-danger text-white hover:bg-danger/90;
}
.btn-ghost {
  @apply text-text-muted hover:text-text hover:bg-surface-3;
}
.btn-sm {
  @apply px-3 py-1.5 text-xs;
}
.btn-icon {
  @apply p-2 rounded-lg text-text-muted hover:bg-surface-3;
}

/* Status badge system */
.badge {
  @apply inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium;
}
.badge-success {
  @apply bg-success/15 text-success;
}
.badge-danger {
  @apply bg-danger/15 text-danger;
}
.badge-warning {
  @apply bg-warning/15 text-warning;
}
.badge-info {
  @apply bg-info/15 text-info;
}
.badge-vault {
  @apply bg-vault/15 text-vault;
}
.badge-neutral {
  @apply bg-surface-3 text-text-muted;
}
```

**Step 2: Verify Tailwind picks up the new classes**

Run: `cd web && npm run build`
Expected: Build succeeds with no errors.

**Step 3: Commit**

```bash
git add web/src/app.css
git commit -m "feat: add unified button and badge CSS utility classes"
```

---

### Task 3: Update statusBadge utility to use new badge classes

**Files:**
- Modify: `web/src/lib/utils.js`

**Step 1: Update statusBadge() to return the new class names**

In `web/src/lib/utils.js`, replace the `statusBadge` function (lines 51-61):

```js
export function statusBadge(status) {
  const map = {
    success: 'badge-success',
    completed: 'badge-success',
    running: 'badge-info',
    pending: 'badge-warning',
    failed: 'badge-danger',
    error: 'badge-danger',
  }
  return 'badge ' + (map[status?.toLowerCase()] || 'badge-neutral')
}
```

**Step 2: Add a describeSchedule function (moved from Jobs.svelte for reuse)**

Append to `web/src/lib/utils.js`:

```js
/** Convert a cron expression to human-readable text */
export function describeSchedule(cron) {
  if (!cron) return '—'
  const parts = cron.trim().split(/\s+/)
  if (parts.length !== 5) return cron
  const [min, hr, dom, , dow] = parts
  const time = `${hr.padStart(2, '0')}:${min.padStart(2, '0')}`
  if (dom !== '*' && dow === '*') return `Monthly on ${ordinal(parseInt(dom))} at ${time}`
  if (dow !== '*' && dom === '*') {
    const days = ['Sun','Mon','Tue','Wed','Thu','Fri','Sat']
    const dowParts = dow.split(',')
    if (dowParts.length === 1) return `Weekly on ${days[parseInt(dowParts[0])]} at ${time}`
    return `${dowParts.map(d => days[parseInt(d)]).join(', ')} at ${time}`
  }
  return `Daily at ${time}`
}

function ordinal(n) {
  const s = ['th', 'st', 'nd', 'rd']
  const v = n % 100
  return n + (s[(v - 20) % 10] || s[v] || s[0])
}

/** Format a next-run time string as relative time ("in 2h 15m") */
export function relTimeUntil(dateStr) {
  if (!dateStr) return null
  const d = new Date(dateStr)
  if (isNaN(d.getTime())) return null
  const diff = d.getTime() - Date.now()
  if (diff < 0) return 'overdue'
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return 'in < 1m'
  if (mins < 60) return `in ${mins}m`
  const hrs = Math.floor(mins / 60)
  const remMins = mins % 60
  if (hrs < 24) return remMins > 0 ? `in ${hrs}h ${remMins}m` : `in ${hrs}h`
  const days = Math.floor(hrs / 24)
  return `in ${days}d`
}
```

**Step 3: Verify no test breakage**

Run: `cd web && npm run build`
Expected: Build succeeds.

**Step 4: Commit**

```bash
git add web/src/lib/utils.js
git commit -m "feat: unify statusBadge classes, add describeSchedule and relTimeUntil utils"
```

---

### Task 4: Make Dashboard stats cards clickable + show next backup time

**Files:**
- Modify: `web/src/pages/Dashboard.svelte`

**Step 1: Add next-run data fetching**

In `Dashboard.svelte`, add to the imports (around line 3):

```js
import { relTimeUntil } from '../lib/utils.js'
```

Add a new state variable (around line 22):

```js
let nextRuns = $state({})
```

In the `loadDashboard` function, add `api.getNextRuns()` to the parallel Promise.all call and store the result:

```js
// Inside the existing Promise.all, add api.getNextRuns() and assign:
const [healthData, jobsData, storageData, containersData, vmsData, foldersData, nextRunsData] = await Promise.all([
  api.health(),
  api.listJobs(),
  api.listStorage(),
  api.listContainers().catch(() => []),
  api.listVMs().catch(() => []),
  api.listFolders().catch(() => []),
  api.getNextRuns().catch(() => ({})),
])
nextRuns = nextRunsData
```

**Step 2: Add a derived "soonest next run" value**

```js
let soonestNextRun = $derived(() => {
  const times = Object.values(nextRuns).map(t => new Date(t)).filter(d => !isNaN(d.getTime()))
  if (times.length === 0) return null
  return new Date(Math.min(...times.map(d => d.getTime()))).toISOString()
})
```

**Step 3: Make stats cards clickable by wrapping in buttons**

Find the stats grid section (around line 213-281). Each card is currently a `<div>`. Wrap each in a `<button>` with `onclick={() => navigate('/path')}` and add cursor-pointer + hover styles:

For the Server card: no navigation (informational only).
For the Jobs card: `navigate('/jobs')` and add next-run text.
For the Containers card: no navigation (informational only).
For the VMs card: no navigation (informational only).
For the Storage card: `navigate('/storage')`.

Replace each card div with an interactive version. Example for the Jobs card:

```svelte
<button onclick={() => navigate('/jobs')} class="bg-surface-2 border border-border rounded-xl p-4 flex flex-col gap-1 text-left hover:border-vault/30 hover:shadow-sm transition-all cursor-pointer">
  <div class="flex items-center justify-between">
    <p class="text-sm text-text-muted">Jobs</p>
    <p class="text-2xl font-bold">{jobs.length}</p>
    <!-- existing icon -->
  </div>
  <p class="text-xs text-text-dim">{enabledJobs} enabled</p>
  {#if soonestNextRun()}
    <p class="text-xs text-vault font-medium">Next: {relTimeUntil(soonestNextRun())}</p>
  {/if}
</button>
```

For non-navigable cards (Server, Containers, VMs), keep as `<div>` but keep visual style consistent.

For Storage card:

```svelte
<button onclick={() => navigate('/storage')} class="...same hover classes...">
  <!-- existing content -->
</button>
```

**Step 4: Verify visually**

Run: `cd web && npm run dev`
Navigate to Dashboard, verify cards are clickable and next-run time appears.

**Step 5: Commit**

```bash
git add web/src/pages/Dashboard.svelte
git commit -m "feat: make dashboard stats cards clickable, show next backup time"
```

---

### Task 5: Show human-readable schedules on Job cards

**Files:**
- Modify: `web/src/pages/Jobs.svelte`

**Step 1: Import describeSchedule from utils instead of local definition**

In `Jobs.svelte`, add to imports (line 3 area):

```js
import { describeSchedule, relTimeUntil } from '../lib/utils.js'
```

Remove the local `describeSchedule` and `ordinal` functions (lines 206-226) since they're now in utils.

**Step 2: Add next-run data fetching**

Add state and fetch next runs in `loadData`:

```js
let nextRuns = $state({})
```

In the existing `loadData` function, add:

```js
const nextRunsData = await api.getNextRuns().catch(() => ({}))
nextRuns = nextRunsData
```

**Step 3: Update job card metadata to show readable schedule and next run**

Find the metadata row in the job card template (around lines 262-275). The schedule currently shows:

```svelte
<span class="..."><svg>...</svg> {describeSchedule(job.schedule)}</span>
```

This should already work since `describeSchedule` exists locally. After importing from utils, it's the same. But also add next-run time:

```svelte
<span class="flex items-center gap-1 text-text-dim">
  <svg><!-- clock icon --></svg>
  {describeSchedule(job.schedule)}
  {#if nextRuns[job.id]}
    <span class="text-vault font-medium ml-1">({relTimeUntil(nextRuns[job.id])})</span>
  {/if}
</span>
```

**Step 4: Add last run status to card**

After the metadata row, add:

```svelte
{#if job.last_run_status}
  <span class="flex items-center gap-1 text-text-dim">
    <span class={statusBadge(job.last_run_status)}>{job.last_run_status}</span>
    {#if job.last_run_at}
      <span class="text-xs">{relTime(job.last_run_at)}</span>
    {/if}
  </span>
{/if}
```

Note: This requires `last_run_status` and `last_run_at` fields. If the List endpoint doesn't include these, we can fetch the most recent run per job separately, or enhance the List endpoint. For now, use the data available from the existing `listJobs()` response. If `last_run_status` isn't available in the response, skip this sub-step — it can be added in a later phase when we enrich the API.

**Step 5: Commit**

```bash
git add web/src/pages/Jobs.svelte web/src/lib/utils.js
git commit -m "feat: show human-readable schedules and next-run time on job cards"
```

---

### Task 6: Show inline failure reasons in History

**Files:**
- Modify: `web/src/pages/History.svelte`

**Step 1: Parse failure reason from run log data**

In `History.svelte`, add a helper function in the script section (after the existing helpers around line 60):

```js
function getFailureReason(run) {
  if (run.status !== 'failed' && run.status !== 'error') return null
  if (!run.log) return 'Unknown error'
  try {
    const items = JSON.parse(run.log)
    if (Array.isArray(items)) {
      const failed = items.filter(i => i.status === 'error' || i.status === 'failed')
      if (failed.length > 0 && failed[0].error) return failed[0].error
      if (failed.length > 0) return `${failed.length} item(s) failed`
    }
  } catch {
    // Plain text log
    const lines = run.log.split('\n').filter(l => l.toLowerCase().includes('error') || l.toLowerCase().includes('fail'))
    if (lines.length > 0) return lines[0].substring(0, 120)
  }
  return 'Backup failed — expand for details'
}
```

**Step 2: Add inline failure reason to table rows**

Find the table row template (around line 137-180). After the status badge cell, add a new row below for failed runs. The cleanest approach is to add the error inline in the status cell:

In the status `<td>` (around line 153-156), change from:

```svelte
<td class="px-4 py-3">
  <span class="px-2.5 py-1 rounded-full text-xs font-medium {statusBadge(run.status)}">{run.status}</span>
</td>
```

To:

```svelte
<td class="px-4 py-3">
  <span class={statusBadge(run.status)}>{run.status}</span>
  {#if getFailureReason(run)}
    <p class="text-xs text-danger mt-1 max-w-xs truncate" title={getFailureReason(run)}>{getFailureReason(run)}</p>
  {/if}
</td>
```

**Step 3: Verify visually**

Run: `cd web && npm run dev`
Navigate to History, verify failed runs show error reason inline.

**Step 4: Commit**

```bash
git add web/src/pages/History.svelte
git commit -m "feat: show inline failure reasons in history table"
```

---

### Task 7: Improve Toast to persist error notifications

**Files:**
- Modify: `web/src/components/Toast.svelte`

**Step 1: Make error toasts persist until dismissed**

In `Toast.svelte`, modify the `$effect` (lines 7-13) to only auto-dismiss non-error toasts:

```svelte
$effect(() => {
  if (key > 0) {
    show = true
    clearTimeout(timeout)
    if (type !== 'error') {
      timeout = setTimeout(() => { show = false }, 4000)
    }
  }
})
```

**Step 2: Make dismiss button more prominent for error toasts**

Update the dismiss button to be more visible when type is error. In the template (around line 27):

```svelte
<button onclick={() => { show = false; clearTimeout(timeout) }}
  class="ml-auto {type === 'error' ? 'opacity-100 hover:opacity-70 font-bold' : 'opacity-60 hover:opacity-100'}">
  ✕
</button>
```

**Step 3: Commit**

```bash
git add web/src/components/Toast.svelte
git commit -m "feat: error toasts persist until dismissed"
```

---

### Task 8: Improve empty states across all pages

**Files:**
- Modify: `web/src/components/EmptyState.svelte` (minor)
- Modify: `web/src/pages/Jobs.svelte`
- Modify: `web/src/pages/Storage.svelte`
- Modify: `web/src/pages/History.svelte`
- Modify: `web/src/pages/Logs.svelte`
- Modify: `web/src/pages/Replication.svelte`
- Modify: `web/src/pages/Restore.svelte`

**Step 1: Enhance EmptyState component with subtitle support**

In `EmptyState.svelte` (line 2), add a `subtitle` prop:

```svelte
let { icon = '', title = '', description = '', subtitle = '', actionLabel = '', onaction = null } = $props()
```

After the description paragraph (line 16), add:

```svelte
{#if subtitle}
  <p class="text-xs text-text-dim mt-2 max-w-sm italic">{subtitle}</p>
{/if}
```

**Step 2: Update each page's empty state to be more contextual and helpful**

In **Jobs.svelte** (around line 246), the EmptyState should guide users:

```svelte
<EmptyState
  icon="📋"
  title="No backup jobs yet"
  description="Create your first backup job to start protecting your containers and VMs."
  subtitle="Tip: You'll need at least one storage destination first."
  actionLabel="+ New Job"
  onaction={() => openModal()}
/>
```

In **Storage.svelte** (around line 241):

```svelte
<EmptyState
  icon="💾"
  title="No storage destinations"
  description="Add a storage destination to define where your backups are stored."
  subtitle="Supported: Local path, SFTP, SMB/CIFS"
  actionLabel="+ Add Storage"
  onaction={() => openModal()}
/>
```

In **History.svelte**, add an empty state when `filteredRuns.length === 0`. Currently there's a "X runs shown" footer but no proper empty state. After the table (around line 222), add:

```svelte
{#if !loading && filteredRuns.length === 0}
  <EmptyState
    icon="📜"
    title="No backup history"
    description="Backup runs will appear here after you run your first backup."
    actionLabel="Go to Jobs"
    onaction={() => navigate('/jobs')}
  />
{/if}
```

In **Logs.svelte** (around line 146), update the existing EmptyState:

```svelte
<EmptyState
  icon="📝"
  title="No activity yet"
  description="System activity and backup events will appear here as they happen."
/>
```

In **Replication.svelte** (around line 199), update:

```svelte
<EmptyState
  icon="🔄"
  title="No replication targets"
  description="Add a remote Vault server to replicate backups for disaster recovery."
  subtitle="Requires a running Vault instance on the remote server."
  actionLabel="+ Add Target"
  onaction={() => { showModal = true }}
/>
```

In **Restore.svelte**, update the "Select a job" empty state (the one shown when no job is selected). Find it and update:

```svelte
<EmptyState
  icon="🔄"
  title="Select a backup job"
  description="Choose a backup job above to browse its restore points and recover data."
/>
```

**Step 3: Commit**

```bash
git add web/src/components/EmptyState.svelte web/src/pages/Jobs.svelte web/src/pages/Storage.svelte web/src/pages/History.svelte web/src/pages/Logs.svelte web/src/pages/Replication.svelte web/src/pages/Restore.svelte
git commit -m "feat: improve empty states with contextual guidance across all pages"
```

---

### Task 9: Add loading skeletons to replace spinners on page loads

**Files:**
- Create: `web/src/components/Skeleton.svelte`
- Modify: `web/src/pages/Dashboard.svelte`
- Modify: `web/src/pages/Jobs.svelte`
- Modify: `web/src/pages/History.svelte`

**Step 1: Create the Skeleton component**

Create `web/src/components/Skeleton.svelte`:

```svelte
<script>
  let { lines = 3, type = 'card' } = $props()
</script>

{#if type === 'card'}
  <div class="animate-pulse space-y-4">
    {#each Array(lines) as _, i}
      <div class="bg-surface-2 border border-border rounded-xl p-5">
        <div class="flex items-center justify-between mb-3">
          <div class="h-5 bg-surface-4 rounded w-1/3"></div>
          <div class="h-5 bg-surface-4 rounded w-16"></div>
        </div>
        <div class="space-y-2">
          <div class="h-3 bg-surface-3 rounded w-2/3"></div>
          <div class="h-3 bg-surface-3 rounded w-1/2"></div>
        </div>
      </div>
    {/each}
  </div>
{:else if type === 'stats'}
  <div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-5 gap-4 animate-pulse">
    {#each Array(5) as _}
      <div class="bg-surface-2 border border-border rounded-xl p-4 h-24">
        <div class="h-3 bg-surface-4 rounded w-1/2 mb-2"></div>
        <div class="h-6 bg-surface-4 rounded w-1/3"></div>
      </div>
    {/each}
  </div>
{:else if type === 'table'}
  <div class="bg-surface-2 border border-border rounded-xl overflow-hidden animate-pulse">
    <div class="border-b border-border px-4 py-3 flex gap-8">
      {#each Array(5) as _}
        <div class="h-3 bg-surface-4 rounded w-20"></div>
      {/each}
    </div>
    {#each Array(lines) as _}
      <div class="border-b border-border px-4 py-4 flex gap-8">
        {#each Array(5) as _}
          <div class="h-3 bg-surface-3 rounded w-24"></div>
        {/each}
      </div>
    {/each}
  </div>
{/if}
```

**Step 2: Replace Spinner with Skeleton on Dashboard**

In `Dashboard.svelte`, replace the loading state (around line 137-138):

```svelte
<!-- Before: -->
<Spinner text="Loading dashboard..." />

<!-- After: -->
<Skeleton type="stats" />
<div class="mt-6"><Skeleton type="card" lines={2} /></div>
```

Import at top:

```js
import Skeleton from '../components/Skeleton.svelte'
```

**Step 3: Replace Spinner with Skeleton on Jobs page**

In `Jobs.svelte`, replace the loading state with:

```svelte
<Skeleton type="card" lines={3} />
```

**Step 4: Replace Spinner with Skeleton on History page**

In `History.svelte`, replace the loading state with:

```svelte
<Skeleton type="table" lines={5} />
```

**Step 5: Verify visually**

Run: `cd web && npm run dev`
Navigate through pages with loading states and verify skeletons appear.

**Step 6: Commit**

```bash
git add web/src/components/Skeleton.svelte web/src/pages/Dashboard.svelte web/src/pages/Jobs.svelte web/src/pages/History.svelte
git commit -m "feat: replace loading spinners with skeleton placeholders"
```

---

### Task 10: Fix mobile stats card layout

**Files:**
- Modify: `web/src/pages/Dashboard.svelte`

**Step 1: Change stats grid to horizontal scroll on mobile**

Find the stats grid container (around line 213). Currently it's:

```svelte
<div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-5 gap-4">
```

This causes the 5th card (Storage) to orphan on its own row at mobile width. Replace with a horizontal scroll on mobile:

```svelte
<div class="flex gap-4 overflow-x-auto pb-2 snap-x snap-mandatory lg:grid lg:grid-cols-5 lg:overflow-visible lg:pb-0">
```

And add `snap-start min-w-[140px] flex-shrink-0 lg:min-w-0 lg:flex-shrink` to each stat card.

**Step 2: Add scroll hint styling**

Add to `web/src/app.css` (in the scrollbar section):

```css
/* Hide scrollbar for mobile horizontal scroll containers */
.overflow-x-auto::-webkit-scrollbar {
  height: 0;
}
```

Actually, this would affect all horizontal scroll containers. Better to use a more targeted approach — add `scrollbar-hide` utility or just accept the thin scrollbar since it provides affordance.

**Step 3: Verify on mobile viewport**

Open dev tools, switch to mobile viewport (375px width), verify stats cards scroll horizontally without orphaned 5th card.

**Step 4: Commit**

```bash
git add web/src/pages/Dashboard.svelte
git commit -m "fix: use horizontal scroll for mobile stats cards instead of wrapping grid"
```

---

### Task 11: Apply unified button classes across key pages

**Files:**
- Modify: `web/src/pages/Jobs.svelte`
- Modify: `web/src/pages/Storage.svelte`
- Modify: `web/src/pages/Dashboard.svelte`
- Modify: `web/src/pages/Replication.svelte`

This task replaces the scattered inline button styles with the unified classes from Task 2. This is a find-and-replace task across pages.

**Step 1: Update Jobs.svelte buttons**

Replace primary button patterns like `px-5 py-2 text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors disabled:opacity-40` with `btn btn-primary`.

Replace secondary buttons like `px-4 py-2 text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4` with `btn btn-secondary`.

Replace danger/delete buttons with `btn btn-danger` or `btn btn-sm btn-danger`.

Key locations in Jobs.svelte:
- "New Job" button (around line 236): `btn btn-primary`
- Wizard Back/Cancel buttons (around line 560): `btn btn-secondary`
- Wizard Next/Create buttons (around line 575-605): `btn btn-primary`
- Delete dialog buttons (around line 640-653): `btn btn-secondary` and `btn btn-danger`
- Run Now, Edit, Delete icon buttons (around line 277-296): `btn-icon` or `btn btn-sm btn-ghost`

**Step 2: Update Storage.svelte buttons**

- "Add Storage" button: `btn btn-primary`
- Modal Cancel/Save buttons: `btn btn-secondary` / `btn btn-primary`
- Delete dialog buttons: `btn btn-secondary` / `btn btn-danger`
- Test Connection button: `btn btn-sm btn-secondary`
- Import dialog buttons: `btn btn-secondary` / `btn btn-primary`

**Step 3: Update Dashboard.svelte buttons**

- "Run Now" button: `btn btn-sm btn-vault` (where btn-vault uses `bg-vault/10 text-vault hover:bg-vault/20`)
- Getting Started action buttons

**Step 4: Update Replication.svelte buttons**

- "Add Target" button: `btn btn-primary`
- Sync Now: `btn btn-sm btn-vault`
- Test/Edit/Delete: `btn btn-sm btn-secondary` / `btn btn-sm btn-danger`

**Step 5: Verify visual consistency**

Run: `cd web && npm run dev`
Navigate through all pages and verify buttons look consistent.

**Step 6: Commit**

```bash
git add web/src/pages/Jobs.svelte web/src/pages/Storage.svelte web/src/pages/Dashboard.svelte web/src/pages/Replication.svelte
git commit -m "refactor: apply unified button classes across all pages"
```

---

### Task 12: Final verification and build

**Files:** None (verification only)

**Step 1: Run the web build**

```bash
cd web && npm run build
```

Expected: Build succeeds with no errors or warnings.

**Step 2: Run Go tests**

```bash
go test ./... -short
```

Expected: All tests pass.

**Step 3: Run lint**

```bash
make lint
```

Expected: No lint errors.

**Step 4: Manual visual review**

Open the running app and verify:
- [ ] Dashboard stats cards are clickable (Jobs → /jobs, Storage → /storage)
- [ ] "Next: in Xh Xm" appears on Dashboard Jobs card
- [ ] Job cards show "Daily at 02:00" instead of "—" or raw cron
- [ ] Job cards show "(in Xh Xm)" next to schedule
- [ ] History failed rows show error reason inline
- [ ] Error toasts persist until dismissed; success toasts auto-dismiss
- [ ] Empty states on all pages show helpful contextual messages
- [ ] Loading states show skeleton placeholders instead of spinners
- [ ] Mobile view: stats cards scroll horizontally (no orphaned card)
- [ ] Dark mode: all changes look correct
- [ ] Buttons across pages use consistent styling

**Step 5: Commit any remaining fixes**

```bash
git add -A && git commit -m "fix: phase 1 final adjustments"
```
